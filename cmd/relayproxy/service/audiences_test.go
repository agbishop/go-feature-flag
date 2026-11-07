package service_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thomaspoignant/go-feature-flag/audience"
	"github.com/thomaspoignant/go-feature-flag/cmd/relayproxy/config"
	"github.com/thomaspoignant/go-feature-flag/cmd/relayproxy/service"
	"github.com/thomaspoignant/go-feature-flag/cmdhelpers/audienceconf"
	"github.com/thomaspoignant/go-feature-flag/cmdhelpers/retrieverconf"
	"github.com/thomaspoignant/go-feature-flag/ffcontext"
	"github.com/thomaspoignant/go-feature-flag/modules/core/flag"
	"go.uber.org/zap"
)

const audienceTestFlags = `
aud-flag:
  variations: { on: true, off: false }
  defaultRule: { variation: off }
  targeting:
    - name: beta
      query: targetingKey in audience("beta")
      variation: on
`

// unreachable redis: connection refused immediately.
func unreachableAudiences(prefix string) *audienceconf.AudiencesConf {
	return &audienceconf.AudiencesConf{Provider: audienceconf.ProviderConf{
		Kind:    audienceconf.RedisProvider,
		Prefix:  prefix,
		Options: &audienceconf.RedisOptions{Addrs: []string{"127.0.0.1:1"}, DialTimeoutMs: 200},
	}}
}

func writeAudienceFlags(t *testing.T) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "flags.yaml")
	require.NoError(t, os.WriteFile(file, []byte(audienceTestFlags), 0o600))
	return file
}

func TestNewGoFeatureFlagClient_Audiences(t *testing.T) {
	file := writeAudienceFlags(t)
	tests := []struct {
		name       string
		audiences  *audienceconf.AudiencesConf
		wantStatus string
		wantErr    bool
	}{
		{name: "not configured", wantStatus: flag.AudienceStatusNotConfigured},
		{
			// readiness never depends on the store: the client starts, lookups report an error.
			name: "store unreachable", audiences: unreachableAudiences("goff:"),
			wantStatus: flag.AudienceStatusStoreError,
		},
		{
			name:      "invalid provider",
			audiences: &audienceconf.AudiencesConf{Provider: audienceconf.ProviderConf{Kind: "nope"}},
			wantErr:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := &config.FlagSet{
				Name: "fs",
				CommonFlagSet: config.CommonFlagSet{
					Retriever:       &retrieverconf.RetrieverConf{Kind: retrieverconf.FileRetriever, Path: file},
					PollingInterval: 10000,
					Audiences:       tt.audiences,
				},
			}
			client, err := service.NewGoFeatureFlagClient(fs, zap.NewNop(), nil)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			defer client.Close()
			res, err := client.RawVariation("aud-flag", ffcontext.NewEvaluationContext("bob"), false)
			require.NoError(t, err)
			assert.Equal(t, false, res.Value)
			assert.Equal(t, tt.wantStatus, res.Metadata[flag.AudienceStatusMetadataKey])
		})
	}
}

func TestAudiencePrefixCollisions(t *testing.T) {
	redisRetriever := func(prefix string) retrieverconf.RetrieverConf {
		return retrieverconf.RetrieverConf{Kind: retrieverconf.RedisRetriever, RedisPrefix: prefix}
	}
	withDef := unreachableAudiences("goff:")
	withDef.Definitions = map[string]audience.ListSettings{"beta": {Prefix: "segments:"}}
	tests := []struct {
		name string
		fs   *config.FlagSet
		want []string
	}{
		{name: "nil flagset"},
		{name: "no audiences", fs: &config.FlagSet{}},
		{
			name: "same prefix as retriever",
			fs: &config.FlagSet{CommonFlagSet: config.CommonFlagSet{
				Retriever: func() *retrieverconf.RetrieverConf { r := redisRetriever("goff:"); return &r }(),
				Audiences: unreachableAudiences("goff:"),
			}},
			want: []string{"goff:"},
		},
		{
			name: "definition prefix collides with one of the retrievers",
			fs: &config.FlagSet{CommonFlagSet: config.CommonFlagSet{
				Retrievers: &[]retrieverconf.RetrieverConf{redisRetriever("flags:"), redisRetriever("segments:")},
				Audiences:  withDef,
			}},
			want: []string{"segments:"},
		},
		{
			name: "different prefixes",
			fs: &config.FlagSet{CommonFlagSet: config.CommonFlagSet{
				Retrievers: &[]retrieverconf.RetrieverConf{redisRetriever("flags:")},
				Audiences:  unreachableAudiences("goff:"),
			}},
		},
		{
			name: "non redis retriever is ignored",
			fs: &config.FlagSet{CommonFlagSet: config.CommonFlagSet{
				Retrievers: &[]retrieverconf.RetrieverConf{{Kind: retrieverconf.FileRetriever}},
				Audiences:  unreachableAudiences(""),
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, service.AudiencePrefixCollisions(tt.fs))
		})
	}
}

func TestFlagsetManager_OnConfigChange_AudiencesChange(t *testing.T) {
	file := writeAudienceFlags(t)
	build := func(prefix string) *config.Config {
		return &config.Config{FlagSets: []config.FlagSet{{
			Name: "fs", APIKeys: []string{"k"},
			CommonFlagSet: config.CommonFlagSet{
				Retriever: &retrieverconf.RetrieverConf{Kind: retrieverconf.FileRetriever, Path: file},
				Audiences: unreachableAudiences(prefix),
			},
		}}}
	}
	manager, logs := setupManager(t, build("a:"))
	// an audiences-only change is applied live, it is not a rejected flagset modification.
	manager.OnConfigChange(build("b:"))
	assert.Equal(t, 0, logs.FilterMessageSnippet("modifying a flagset is not supported at runtime").Len())
	// audiences together with another change: rejected, it needs a restart.
	changed := build("c:")
	changed.FlagSets[0].PollingInterval = 99999
	manager.OnConfigChange(changed)
	assert.Equal(t, 1, logs.FilterMessageSnippet("modifying a flagset is not supported at runtime").Len())
}
