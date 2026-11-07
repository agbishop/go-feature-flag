package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thomaspoignant/go-feature-flag/audience"
	"github.com/thomaspoignant/go-feature-flag/cmd/relayproxy/config"
	"github.com/thomaspoignant/go-feature-flag/cmdhelpers/audienceconf"
	"github.com/thomaspoignant/go-feature-flag/cmdhelpers/retrieverconf"
	"go.uber.org/zap"
)

const audiencesBlock = `
audiences:
  provider:
    kind: redis
    options:
      addrs: ["redis:6379"]
      username: reader
      password: secret
      db: 2
      dialTimeout: 150
      poolSize: 20
      tls: { enabled: true, insecureSkipVerify: false, serverName: redis.internal }
    prefix: "goff:"
  cache: { size: 1000, ttl: 5s }
  lookupTimeout: 50ms
  failMode: unknown
  defaults: { layout: set }
  definitions:
    beta_testers: { prefix: "segments:", normalize: trimLower, hash: sha256, encoding: hex }
    "big_*": { layout: keys }
    churned: { failMode: empty }
`

func writeConfig(t *testing.T, body string) *pflag.FlagSet {
	t.Helper()
	file := filepath.Join(t.TempDir(), "goff.yaml")
	require.NoError(t, os.WriteFile(file, []byte(body), 0o600))
	f := pflag.NewFlagSet("config", pflag.ContinueOnError)
	f.String("config", "", "Location of your config file")
	require.NoError(t, f.Parse([]string{fmt.Sprintf("--config=%s", file)}))
	return f
}

func TestParseConfig_Audiences(t *testing.T) {
	base := "pollingInterval: 1000\nretriever:\n  kind: file\n  path: ./flags.yaml\n"
	t.Run("default mode", func(t *testing.T) {
		got, err := config.New(writeConfig(t, base+audiencesBlock), zap.NewNop(), "1.X.X")
		require.NoError(t, err)
		require.NotNil(t, got.Audiences)
		assert.NoError(t, got.IsValid())
		a := got.Audiences
		assert.Equal(t, audienceconf.RedisProvider, a.Provider.Kind)
		assert.Equal(t, "goff:", a.Provider.Prefix)
		require.NotNil(t, a.Provider.Options)
		assert.Equal(t, []string{"redis:6379"}, a.Provider.Options.Addrs)
		assert.Equal(t, "reader", a.Provider.Options.Username)
		assert.Equal(t, 2, a.Provider.Options.DB)
		assert.Equal(t, int64(150), a.Provider.Options.DialTimeoutMs)
		require.NotNil(t, a.Provider.Options.TLS)
		assert.True(t, a.Provider.Options.TLS.Enabled)
		assert.Equal(t, "redis.internal", a.Provider.Options.TLS.ServerName)
		assert.Equal(t, 1000, a.Cache.Size)
		assert.Equal(t, 5*time.Second, a.Cache.TTL)
		assert.Equal(t, 50*time.Millisecond, a.LookupTimeout)
		assert.Equal(t, audience.FailModeUnknown, a.FailMode)
		assert.Equal(t, audience.LayoutSet, a.Defaults.Layout)
		assert.Equal(t, audience.ListSettings{
			Prefix: "segments:", Normalize: "trimLower", Hash: "sha256", Encoding: "hex",
		}, a.Definitions["beta_testers"])
		assert.Equal(t, "keys", a.Definitions["big_*"].Layout)
		assert.Equal(t, "empty", a.Definitions["churned"].FailMode)
	})

	t.Run("flagset", func(t *testing.T) {
		body := "flagsets:\n  - name: fs1\n    apiKeys: [k1]\n    retriever:\n      kind: file\n      path: ./f.yaml\n" +
			"    audiences:\n      provider:\n        kind: redis\n        options: { addrs: [\"redis:6379\"] }\n"
		got, err := config.New(writeConfig(t, "pollingInterval: 1000\n"+body), zap.NewNop(), "1.X.X")
		require.NoError(t, err)
		require.Len(t, got.FlagSets, 1)
		require.NotNil(t, got.FlagSets[0].Audiences)
		assert.NoError(t, got.IsValid())
	})
}

func TestConfig_IsValid_Audiences(t *testing.T) {
	fileRetriever := &retrieverconf.RetrieverConf{Kind: retrieverconf.FileRetriever, Path: "./f.yaml"}
	validAud := &audienceconf.AudiencesConf{Provider: audienceconf.ProviderConf{
		Kind: audienceconf.RedisProvider, Options: &audienceconf.RedisOptions{Addrs: []string{"r:1"}},
	}}
	badAud := &audienceconf.AudiencesConf{Provider: audienceconf.ProviderConf{Kind: "nope"}}

	tests := []struct {
		name    string
		conf    *config.Config
		wantErr bool
	}{
		{
			name: "default mode valid",
			conf: &config.Config{CommonFlagSet: config.CommonFlagSet{Retriever: fileRetriever, Audiences: validAud}},
		},
		{
			name:    "default mode invalid",
			conf:    &config.Config{CommonFlagSet: config.CommonFlagSet{Retriever: fileRetriever, Audiences: badAud}},
			wantErr: true,
		},
		{
			name: "flagset valid",
			conf: &config.Config{FlagSets: []config.FlagSet{{
				Name: "a", APIKeys: []string{"k"},
				CommonFlagSet: config.CommonFlagSet{Retriever: fileRetriever, Audiences: validAud},
			}}},
		},
		{
			name: "flagset invalid",
			conf: &config.Config{FlagSets: []config.FlagSet{{
				Name: "a", APIKeys: []string{"k"},
				CommonFlagSet: config.CommonFlagSet{Retriever: fileRetriever, Audiences: badAud},
			}}},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.conf.IsValid()
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "audiences")
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestConfig_Audiences_FlagsetsAndDurations(t *testing.T) {
	fileRetriever := &retrieverconf.RetrieverConf{Kind: retrieverconf.FileRetriever, Path: "./f.yaml"}
	validAud := &audienceconf.AudiencesConf{Provider: audienceconf.ProviderConf{
		Kind: audienceconf.RedisProvider, Options: &audienceconf.RedisOptions{Addrs: []string{"r:1"}},
	}}
	t.Run("top-level audiences with flagsets are rejected", func(t *testing.T) {
		c := &config.Config{
			CommonFlagSet: config.CommonFlagSet{Audiences: validAud},
			FlagSets: []config.FlagSet{{
				Name: "a", APIKeys: []string{"k"}, CommonFlagSet: config.CommonFlagSet{Retriever: fileRetriever},
			}},
		}
		err := c.IsValid()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "audiences")
		assert.Contains(t, err.Error(), "flagset")
	})

	base := "pollingInterval: 1000\nretriever:\n  kind: file\n  path: ./flags.yaml\n" +
		"audiences:\n  provider:\n    kind: redis\n    options: { addrs: [\"r:1\"] }\n"
	tests := []struct {
		name    string
		extra   string
		wantErr string
		timeout time.Duration
		ttl     time.Duration
	}{
		{
			name: "duration strings", extra: "  lookupTimeout: 50ms\n  cache: { size: 10, ttl: 5s }\n",
			timeout: 50 * time.Millisecond, ttl: 5 * time.Second,
		},
		{
			name: "bare int timeout is rejected with an explanation", extra: "  lookupTimeout: 50\n",
			wantErr: "duration string like 50ms",
		},
		{
			name: "bare int ttl is rejected with an explanation", extra: "  cache: { size: 10, ttl: 5 }\n",
			wantErr: "duration string like 5s",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := config.New(writeConfig(t, base+tt.extra), zap.NewNop(), "1.X.X")
			require.NoError(t, err)
			err = got.IsValid()
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.timeout, got.Audiences.LookupTimeout)
			assert.Equal(t, tt.ttl, got.Audiences.Cache.TTL)
		})
	}
}

func TestConfigAudiencesAccessors(t *testing.T) {
	a := &audienceconf.AudiencesConf{}
	tests := []struct {
		name    string
		run     func(c *config.Config) error
		check   func(t *testing.T, c *config.Config)
		wantErr bool
	}{
		{
			name:  "set and get the default audiences",
			run:   func(c *config.Config) error { c.SetAudiences(a); return nil },
			check: func(t *testing.T, c *config.Config) { assert.Same(t, a, c.GetAudiences()) },
		},
		{
			name:  "set the audiences of a flagset",
			run:   func(c *config.Config) error { return c.SetFlagSetAudiences("fs1", a) },
			check: func(t *testing.T, c *config.Config) { assert.Same(t, a, c.FlagSets[0].Audiences) },
		},
		{
			name:    "unknown flagset",
			run:     func(c *config.Config) error { return c.SetFlagSetAudiences("nope", a) },
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &config.Config{FlagSets: []config.FlagSet{{Name: "fs1"}}}
			err := tt.run(c)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			tt.check(t, c)
		})
	}
}
