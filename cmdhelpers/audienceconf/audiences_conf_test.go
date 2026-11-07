package audienceconf_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thomaspoignant/go-feature-flag/audience"
	"github.com/thomaspoignant/go-feature-flag/audience/redisprovider"
	"github.com/thomaspoignant/go-feature-flag/cmdhelpers/audienceconf"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func validRedis() audienceconf.ProviderConf {
	return audienceconf.ProviderConf{
		Kind:    audienceconf.RedisProvider,
		Options: &audienceconf.RedisOptions{Addrs: []string{"redis:6379"}},
	}
}

func TestAudiencesConf_IsValid(t *testing.T) {
	tests := []struct {
		name    string
		conf    *audienceconf.AudiencesConf
		wantErr string
	}{
		{name: "nil is valid", conf: nil},
		{name: "valid redis", conf: &audienceconf.AudiencesConf{Provider: validRedis()}},
		{
			name:    "missing kind",
			conf:    &audienceconf.AudiencesConf{Provider: audienceconf.ProviderConf{}},
			wantErr: "provider.kind is required",
		},
		{
			name:    "unknown kind",
			conf:    &audienceconf.AudiencesConf{Provider: audienceconf.ProviderConf{Kind: "nope"}},
			wantErr: "invalid provider kind",
		},
		{
			name:    "redis without options",
			conf:    &audienceconf.AudiencesConf{Provider: audienceconf.ProviderConf{Kind: audienceconf.RedisProvider}},
			wantErr: "options.addrs is required",
		},
		{
			name: "redis with empty addr",
			conf: &audienceconf.AudiencesConf{Provider: audienceconf.ProviderConf{
				Kind: audienceconf.RedisProvider, Options: &audienceconf.RedisOptions{Addrs: []string{" "}},
			}},
			wantErr: "empty address",
		},
		{
			name: "invalid audience config",
			conf: &audienceconf.AudiencesConf{
				Provider: validRedis(),
				Config:   audience.Config{FailMode: "nope"},
			},
			wantErr: "failMode",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.conf.IsValid()
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestAudiencesConf_EffectiveConfigAndPrefixes(t *testing.T) {
	tests := []struct {
		name         string
		conf         audienceconf.AudiencesConf
		wantDefault  string
		wantPrefixes []string
		wantBeta     string
	}{
		{
			name:         "provider prefix is the default",
			conf:         audienceconf.AudiencesConf{Provider: audienceconf.ProviderConf{Prefix: "goff:"}},
			wantDefault:  "goff:",
			wantPrefixes: []string{"goff:"},
			wantBeta:     "goff:",
		},
		{
			name: "defaults and definitions override",
			conf: audienceconf.AudiencesConf{
				Provider: audienceconf.ProviderConf{Prefix: "goff:"},
				Config: audience.Config{
					Definitions: map[string]audience.ListSettings{"beta": {Prefix: "segments:"}},
				},
			},
			wantDefault:  "goff:",
			wantPrefixes: []string{"goff:", "segments:"},
			wantBeta:     "segments:",
		},
		{
			name: "defaults prefix wins over the provider prefix",
			conf: audienceconf.AudiencesConf{
				Provider: audienceconf.ProviderConf{Prefix: "goff:"},
				Config:   audience.Config{Defaults: audience.ListSettings{Prefix: "d:"}},
			},
			wantDefault:  "d:",
			wantPrefixes: []string{"d:"},
			wantBeta:     "d:",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.conf.EffectiveConfig()
			assert.Equal(t, tt.wantDefault, cfg.Defaults.Prefix)
			assert.Equal(t, tt.wantBeta, cfg.Resolve("beta").Prefix)
			assert.Equal(t, tt.wantPrefixes, tt.conf.Prefixes())
		})
	}
}

func TestRedisOptions_ToUniversalOptions(t *testing.T) {
	o := &audienceconf.RedisOptions{
		Addrs: []string{"a:1", "b:2"}, MasterName: "m", Username: "u", Password: "p", DB: 3,
		DialTimeoutMs: 100, ReadTimeoutMs: 200, WriteTimeoutMs: 300, PoolSize: 7, MinIdleConns: 2,
		TLS: &audienceconf.RedisTLS{Enabled: true, InsecureSkipVerify: true, ServerName: "srv"},
	}
	got := o.ToUniversalOptions()
	assert.Equal(t, []string{"a:1", "b:2"}, got.Addrs)
	assert.Equal(t, "m", got.MasterName)
	assert.Equal(t, 3, got.DB)
	assert.Equal(t, 100*time.Millisecond, got.DialTimeout)
	assert.Equal(t, 200*time.Millisecond, got.ReadTimeout)
	assert.Equal(t, 300*time.Millisecond, got.WriteTimeout)
	assert.Equal(t, 7, got.PoolSize)
	require.NotNil(t, got.TLSConfig)
	assert.True(t, got.TLSConfig.InsecureSkipVerify)
	assert.Equal(t, "srv", got.TLSConfig.ServerName)

	noTLS := (&audienceconf.RedisOptions{Addrs: []string{"a:1"}}).ToUniversalOptions()
	assert.Nil(t, noTLS.TLSConfig)
}

func TestInitProvider(t *testing.T) {
	p, err := audienceconf.InitProvider(&audienceconf.AudiencesConf{Provider: validRedis()})
	require.NoError(t, err)
	rp, ok := p.(*redisprovider.Provider)
	require.True(t, ok)
	assert.Equal(t, []string{"redis:6379"}, rp.Options.Addrs)

	_, err = audienceconf.InitProvider(nil)
	assert.Error(t, err)
	_, err = audienceconf.InitProvider(&audienceconf.AudiencesConf{})
	assert.Error(t, err)
}

func TestRedisOptions_PasswordIsRedacted(t *testing.T) {
	const secret = "s3cr3t-pa55"
	opts := audienceconf.RedisOptions{Addrs: []string{"r:1"}, Username: "u", Password: secret}
	conf := &audienceconf.AudiencesConf{Provider: audienceconf.ProviderConf{
		Kind: audienceconf.RedisProvider, Options: &opts,
	}}
	tests := []struct {
		name string
		out  func() string
	}{
		{"json value", func() string { b, _ := json.Marshal(opts); return string(b) }},
		{"json pointer", func() string { b, _ := json.Marshal(&opts); return string(b) }},
		{"json whole conf (as zap.Any does)", func() string { b, _ := json.Marshal(conf); return string(b) }}, //nolint:musttag
		{"%v", func() string { return fmt.Sprintf("%v", opts) }},
		{"%+v pointer", func() string { return fmt.Sprintf("%+v", &opts) }},
		{"%#v", func() string { return fmt.Sprintf("%#v", opts) }},
		{"String", opts.String},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := tt.out()
			assert.NotContains(t, out, secret)
			assert.Contains(t, out, "r:1")
		})
	}
	t.Run("zap encodes the masked value", func(t *testing.T) {
		enc := zapcore.NewMapObjectEncoder()
		zap.Any("conf", conf).AddTo(enc)
		assert.NotContains(t, fmt.Sprint(enc.Fields), secret)
	})
	t.Run("the password is kept for the connection", func(t *testing.T) {
		assert.Equal(t, secret, opts.ToUniversalOptions().Password)
		assert.Equal(t, secret, opts.Password)
	})
	t.Run("no password stays empty", func(t *testing.T) {
		b, _ := json.Marshal(audienceconf.RedisOptions{Addrs: []string{"r:1"}})
		assert.NotContains(t, string(b), "password")
	})
}

func TestAudiencesConf_SameProvider(t *testing.T) {
	mk := func(kind audienceconf.ProviderKind, addr, prefix string) *audienceconf.AudiencesConf {
		return &audienceconf.AudiencesConf{Provider: audienceconf.ProviderConf{
			Kind: kind, Prefix: prefix, Options: &audienceconf.RedisOptions{Addrs: []string{addr}},
		}}
	}
	tests := []struct {
		name string
		a, b *audienceconf.AudiencesConf
		want bool
	}{
		{name: "both nil", want: true},
		{name: "one nil", a: mk("redis", "a:1", ""), want: false},
		{name: "same", a: mk("redis", "a:1", ""), b: mk("redis", "a:1", ""), want: true},
		{name: "prefix is not the provider", a: mk("redis", "a:1", "x:"), b: mk("redis", "a:1", "y:"), want: true},
		{name: "other address", a: mk("redis", "a:1", ""), b: mk("redis", "a:2", ""), want: false},
		{name: "other kind", a: mk("redis", "a:1", ""), b: mk("valkey", "a:1", ""), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.a.SameProvider(tt.b))
		})
	}
}
