package audience

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
)

// Allowed values of the settings.
const (
	LayoutSet  = "set"
	LayoutKeys = "keys"

	NormalizeNone      = "none"
	NormalizeTrimLower = "trimLower"

	HashNone   = "none"
	HashSHA256 = "sha256"

	EncodingHex = "hex"
	EncodingRaw = "raw"

	// FailModeUnknown makes a failed lookup an unknown term (rules using it are skipped).
	FailModeUnknown = "unknown"
	// FailModeEmpty makes a failed lookup a non member.
	FailModeEmpty = "empty"

	// DefaultLookupTimeout is used when Config.LookupTimeout is zero.
	DefaultLookupTimeout = 50 * time.Millisecond
)

// Config is the audience configuration (everything but the provider kind and its options).
type Config struct {
	// Cache configures the LRU of lookup results.
	Cache CacheConfig `mapstructure:"cache" koanf:"cache" json:"cache" yaml:"cache"`
	// LookupTimeout is the deadline for all the lookups of one request (default 50ms).
	LookupTimeout time.Duration `mapstructure:"lookupTimeout" koanf:"lookuptimeout" json:"lookupTimeout" yaml:"lookupTimeout"` //nolint:lll
	// FailMode is "unknown" (default) or "empty".
	FailMode string `mapstructure:"failMode" koanf:"failmode" json:"failMode" yaml:"failMode"`
	// Defaults apply to every list without a definition, and fill the unset fields of definitions.
	Defaults ListSettings `mapstructure:"defaults" koanf:"defaults" json:"defaults" yaml:"defaults"`
	// Definitions are keyed by list name or glob (path.Match syntax).
	Definitions map[string]ListSettings `mapstructure:"definitions" koanf:"definitions" json:"definitions" yaml:"definitions"` //nolint:lll
}

// CacheConfig configures the LRU of lookup results.
type CacheConfig struct {
	// Size is the maximum number of entries, 0 disables the cache.
	Size int `mapstructure:"size" koanf:"size" json:"size" yaml:"size"`
	// TTL is how long a result is kept, it must be positive when the cache is enabled.
	TTL time.Duration `mapstructure:"ttl" koanf:"ttl" json:"ttl" yaml:"ttl"`
}

// ListSettings describes how a list is stored. Empty fields inherit from the level above
// (definition, then Config.Defaults, then the built-in defaults).
type ListSettings struct {
	// Prefix namespaces the keys, empty means the provider's own prefix.
	Prefix string `mapstructure:"prefix" koanf:"prefix" json:"prefix" yaml:"prefix"`
	// Layout is "set" (default) or "keys".
	Layout string `mapstructure:"layout" koanf:"layout" json:"layout" yaml:"layout"`
	// Normalize is "none" (default) or "trimLower", applied before hashing.
	Normalize string `mapstructure:"normalize" koanf:"normalize" json:"normalize" yaml:"normalize"`
	// Hash is "none" (default) or "sha256".
	Hash string `mapstructure:"hash" koanf:"hash" json:"hash" yaml:"hash"`
	// Encoding is "hex" (default) or "raw" (32 byte digest, requires hash sha256).
	Encoding string `mapstructure:"encoding" koanf:"encoding" json:"encoding" yaml:"encoding"`
	// FailMode overrides Config.FailMode for the list.
	FailMode string `mapstructure:"failMode" koanf:"failmode" json:"failMode" yaml:"failMode"`
}

// merge returns s where empty fields are taken from base.
func (s ListSettings) merge(base ListSettings) ListSettings {
	pick := func(v, b string) string {
		if v != "" {
			return v
		}
		return b
	}
	return ListSettings{
		Prefix:    pick(s.Prefix, base.Prefix),
		Layout:    pick(s.Layout, base.Layout),
		Normalize: pick(s.Normalize, base.Normalize),
		Hash:      pick(s.Hash, base.Hash),
		Encoding:  pick(s.Encoding, base.Encoding),
		FailMode:  pick(s.FailMode, base.FailMode),
	}
}

func builtinSettings() ListSettings {
	return ListSettings{
		Layout:    LayoutSet,
		Normalize: NormalizeNone,
		Hash:      HashNone,
		Encoding:  EncodingHex,
		FailMode:  FailModeUnknown,
	}
}

func oneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

// validate checks a partially filled settings (empty fields are valid).
func (s ListSettings) validate(where string) error {
	checks := []struct {
		field, value string
		allowed      []string
	}{
		{"layout", s.Layout, []string{LayoutSet, LayoutKeys}},
		{"normalize", s.Normalize, []string{NormalizeNone, NormalizeTrimLower}},
		{"hash", s.Hash, []string{HashNone, HashSHA256}},
		{"encoding", s.Encoding, []string{EncodingHex, EncodingRaw}},
		{"failMode", s.FailMode, []string{FailModeUnknown, FailModeEmpty}},
	}
	for _, c := range checks {
		if c.value != "" && !oneOf(c.value, c.allowed...) {
			return fmt.Errorf("%s: invalid %s %q (allowed: %s)", where, c.field, c.value, strings.Join(c.allowed, ", "))
		}
	}
	return nil
}

// IsValid checks the configuration.
func (c *Config) IsValid() error {
	if c.Cache.Size < 0 {
		return fmt.Errorf("audiences: cache.size must not be negative, got %d", c.Cache.Size)
	}
	if c.Cache.Size > 0 && c.Cache.TTL <= 0 {
		return fmt.Errorf("audiences: cache.ttl must be positive when cache.size is set, " +
			"use a duration string like 5s")
	}
	if c.Cache.Size > 0 && c.Cache.TTL < time.Millisecond {
		return fmt.Errorf("audiences: cache.ttl is %s, it must be at least 1ms "+
			"(use a duration string like 5s, a bare number is read as nanoseconds)", c.Cache.TTL)
	}
	if c.Cache.TTL < 0 {
		return fmt.Errorf("audiences: cache.ttl must not be negative")
	}
	if c.LookupTimeout < 0 {
		return fmt.Errorf("audiences: lookupTimeout must not be negative")
	}
	if c.LookupTimeout != 0 && c.LookupTimeout < time.Millisecond {
		return fmt.Errorf("audiences: lookupTimeout is %s, it must be at least 1ms "+
			"(use a duration string like 50ms, a bare number is read as nanoseconds)", c.LookupTimeout)
	}
	if c.FailMode != "" && !oneOf(c.FailMode, FailModeUnknown, FailModeEmpty) {
		return fmt.Errorf("audiences: invalid failMode %q (allowed: unknown, empty)", c.FailMode)
	}
	if err := c.Defaults.validate("audiences.defaults"); err != nil {
		return err
	}
	for _, name := range sortedKeys(c.Definitions) {
		if _, err := path.Match(name, ""); err != nil {
			return fmt.Errorf("audiences.definitions[%q]: invalid glob pattern: %w", name, err)
		}
		def := c.Definitions[name]
		where := fmt.Sprintf("audiences.definitions[%q]", name)
		if err := def.validate(where); err != nil {
			return err
		}
		eff := def.merge(c.Defaults).merge(builtinSettings())
		if eff.Encoding == EncodingRaw && eff.Hash != HashSHA256 {
			return fmt.Errorf("%s: encoding raw requires hash sha256", where)
		}
	}
	eff := c.Defaults.merge(builtinSettings())
	if eff.Encoding == EncodingRaw && eff.Hash != HashSHA256 {
		return fmt.Errorf("audiences.defaults: encoding raw requires hash sha256")
	}
	return nil
}

// Resolve returns the effective settings of a list: built-in defaults, overridden by
// Config.Defaults (and Config.FailMode), overridden by the matching definition. An exact
// name beats a glob; among globs the longest pattern wins, then the lexically smallest.
func (c *Config) Resolve(list string) ListSettings {
	base := c.Defaults
	if base.FailMode == "" {
		base.FailMode = c.FailMode
	}
	base = base.merge(builtinSettings())
	if def, ok := c.Definitions[list]; ok {
		return def.merge(base)
	}
	for _, pattern := range globOrder(c.Definitions) {
		if ok, err := path.Match(pattern, list); err == nil && ok {
			return c.Definitions[pattern].merge(base)
		}
	}
	return base
}

// globOrder returns the definition keys, longest first then lexical (deterministic).
func globOrder(defs map[string]ListSettings) []string {
	keys := sortedKeys(defs)
	sort.SliceStable(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	return keys
}

func sortedKeys(m map[string]ListSettings) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (c *Config) lookupTimeout() time.Duration {
	if c.LookupTimeout > 0 {
		return c.LookupTimeout
	}
	return DefaultLookupTimeout
}
