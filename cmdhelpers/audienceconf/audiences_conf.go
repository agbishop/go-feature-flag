// Package audienceconf holds the serializable configuration of the audiences (the audience()
// query term) for the relay proxy, and the factories building the audience providers.
package audienceconf

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/thomaspoignant/go-feature-flag/audience"
	"github.com/thomaspoignant/go-feature-flag/audience/redisprovider"
)

// ProviderKind is the kind of audience store.
type ProviderKind string

const (
	// RedisProvider reads the audiences from Redis (or Valkey, which speaks the same protocol).
	RedisProvider ProviderKind = "redis"
)

// AudiencesConf is the configuration of the audiences.
type AudiencesConf struct {
	// Provider is the audience store.
	Provider ProviderConf `mapstructure:"provider" koanf:"provider"`

	// Config holds cache, lookupTimeout, failMode, defaults and definitions.
	audience.Config `mapstructure:",squash" koanf:",squash"`
}

// ProviderConf configures the audience store.
type ProviderConf struct {
	// Kind selects the implementation (redis).
	Kind ProviderKind `mapstructure:"kind" koanf:"kind"`
	// Options are the options of the underlying client (kind redis).
	Options *RedisOptions `mapstructure:"options" koanf:"options"`
	// Prefix is the default prefix of the lists, it can be overridden by defaults and definitions.
	Prefix string `mapstructure:"prefix" koanf:"prefix"`
}

// RedisOptions is a serializable view of redis.UniversalOptions.
// Durations are in milliseconds, as in the redis retriever configuration.
type RedisOptions struct {
	// Addrs is the list of host:port. One address is a single node, several is a Cluster
	// (or Sentinel when MasterName is set).
	Addrs []string `mapstructure:"addrs" koanf:"addrs" json:"addrs"`
	// MasterName enables Sentinel mode.
	MasterName string `mapstructure:"masterName" koanf:"masterName" json:"masterName,omitempty"`
	Username   string `mapstructure:"username" koanf:"username" json:"username,omitempty"`
	Password   string `mapstructure:"password" koanf:"password" json:"password,omitempty"` //nolint:gosec // G117
	DB         int    `mapstructure:"db" koanf:"db" json:"db,omitempty"`
	// DialTimeout, ReadTimeout and WriteTimeout in milliseconds.
	DialTimeoutMs  int64 `mapstructure:"dialTimeout" koanf:"dialTimeout" json:"dialTimeout,omitempty"`
	ReadTimeoutMs  int64 `mapstructure:"readTimeout" koanf:"readTimeout" json:"readTimeout,omitempty"`
	WriteTimeoutMs int64 `mapstructure:"writeTimeout" koanf:"writeTimeout" json:"writeTimeout,omitempty"`
	// PoolSize is the maximum number of connections per node.
	PoolSize     int `mapstructure:"poolSize" koanf:"poolSize" json:"poolSize,omitempty"`
	MinIdleConns int `mapstructure:"minIdleConns" koanf:"minIdleConns" json:"minIdleConns,omitempty"`
	// TLS configures the connection encryption.
	TLS *RedisTLS `mapstructure:"tls" koanf:"tls" json:"tls,omitempty"`
}

// redactedPassword replaces the password in the serialized/printed forms of RedisOptions.
const redactedPassword = "********"

// redisOptionsPlain has the fields of RedisOptions and none of its methods.
type redisOptionsPlain RedisOptions

// MarshalJSON masks the password, so that the configuration can be logged (zap.Any, debug
// dumps) without leaking it. The decoding of the configuration (koanf/mapstructure) does not
// go through JSON and is not affected.
func (o RedisOptions) MarshalJSON() ([]byte, error) {
	plain := redisOptionsPlain(o)
	if plain.Password != "" {
		plain.Password = redactedPassword
	}
	return json.Marshal(plain)
}

// String implements fmt.Stringer with the password masked (used by %v and %+v).
func (o RedisOptions) String() string {
	b, err := o.MarshalJSON()
	if err != nil {
		return "RedisOptions{}"
	}
	return string(b)
}

// GoString implements fmt.GoStringer with the password masked (used by %#v).
func (o RedisOptions) GoString() string { return o.String() }

// RedisTLS is the TLS configuration of the Redis connection.
type RedisTLS struct {
	Enabled            bool   `mapstructure:"enabled" koanf:"enabled" json:"enabled,omitempty"`
	InsecureSkipVerify bool   `mapstructure:"insecureSkipVerify" koanf:"insecureSkipVerify" json:"insecureSkipVerify,omitempty"` //nolint:lll
	ServerName         string `mapstructure:"serverName" koanf:"serverName" json:"serverName,omitempty"`
}

// ToUniversalOptions converts the options to the go-redis ones.
func (o RedisOptions) ToUniversalOptions() *redis.UniversalOptions {
	opts := &redis.UniversalOptions{
		Addrs:        append([]string(nil), o.Addrs...),
		MasterName:   o.MasterName,
		Username:     o.Username,
		Password:     o.Password,
		DB:           o.DB,
		PoolSize:     o.PoolSize,
		MinIdleConns: o.MinIdleConns,
	}
	if o.DialTimeoutMs != 0 {
		opts.DialTimeout = time.Duration(o.DialTimeoutMs) * time.Millisecond
	}
	if o.ReadTimeoutMs != 0 {
		opts.ReadTimeout = time.Duration(o.ReadTimeoutMs) * time.Millisecond
	}
	if o.WriteTimeoutMs != 0 {
		opts.WriteTimeout = time.Duration(o.WriteTimeoutMs) * time.Millisecond
	}
	if o.TLS != nil && o.TLS.Enabled {
		opts.TLSConfig = &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: o.TLS.InsecureSkipVerify, //nolint:gosec // explicit opt-in by the operator
			ServerName:         o.TLS.ServerName,
		}
	}
	return opts
}

// providerFactory builds a provider from its configuration.
type providerFactory func(*ProviderConf) (audience.Provider, error)

// providerFactories maps the kinds to their factories.
var providerFactories = map[ProviderKind]providerFactory{
	RedisProvider: createRedisProvider,
}

func createRedisProvider(c *ProviderConf) (audience.Provider, error) {
	return &redisprovider.Provider{Options: c.Options.ToUniversalOptions()}, nil
}

func (p *ProviderConf) validate() error {
	if p.Kind == "" {
		return fmt.Errorf("audiences: provider.kind is required (supported: %s)", supportedKinds())
	}
	if _, ok := providerFactories[p.Kind]; !ok {
		return fmt.Errorf("audiences: invalid provider kind %q (supported: %s)", p.Kind, supportedKinds())
	}
	if p.Kind == RedisProvider {
		if p.Options == nil || len(p.Options.Addrs) == 0 {
			return fmt.Errorf("audiences: provider.options.addrs is required for the %q provider", p.Kind)
		}
		for _, a := range p.Options.Addrs {
			if strings.TrimSpace(a) == "" {
				return fmt.Errorf("audiences: provider.options.addrs must not contain an empty address")
			}
		}
	}
	return nil
}

func supportedKinds() string {
	kinds := make([]string, 0, len(providerFactories))
	for k := range providerFactories {
		kinds = append(kinds, string(k))
	}
	sort.Strings(kinds)
	return strings.Join(kinds, ", ")
}

// IsValid checks the provider and the audience configuration.
func (c *AudiencesConf) IsValid() error {
	if c == nil {
		return nil
	}
	if err := c.Provider.validate(); err != nil {
		return err
	}
	return c.Config.IsValid()
}

// SameProvider tells if both configurations use the same store (same kind and options), so that
// a running provider can be kept when only the rest of the configuration (cache, definitions...)
// changes. The prefix is not part of the provider, it is applied by the resolver.
func (c *AudiencesConf) SameProvider(o *AudiencesConf) bool {
	if c == nil || o == nil {
		return c == o
	}
	return c.Provider.Kind == o.Provider.Kind && reflect.DeepEqual(c.Provider.Options, o.Provider.Options)
}

// Prefixes returns every distinct key prefix used by the audiences (provider, defaults and
// definitions).
func (c *AudiencesConf) Prefixes() []string {
	if c == nil {
		return nil
	}
	set := map[string]struct{}{c.EffectiveConfig().Defaults.Prefix: {}}
	for _, d := range c.Definitions {
		if d.Prefix != "" {
			set[d.Prefix] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// EffectiveConfig returns the audience.Config where the provider prefix is the default prefix
// of the lists (defaults and definitions still override it).
func (c *AudiencesConf) EffectiveConfig() audience.Config {
	cfg := c.Config
	if cfg.Defaults.Prefix == "" {
		cfg.Defaults.Prefix = c.Provider.Prefix
	}
	return cfg
}

// InitProvider builds the (not yet initialized) provider.
func InitProvider(c *AudiencesConf) (audience.Provider, error) {
	if c == nil {
		return nil, fmt.Errorf("audiences configuration is empty")
	}
	if err := c.Provider.validate(); err != nil {
		return nil, err
	}
	return providerFactories[c.Provider.Kind](&c.Provider)
}
