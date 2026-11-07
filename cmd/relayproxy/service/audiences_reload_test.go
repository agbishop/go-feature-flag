package service_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ffclient "github.com/thomaspoignant/go-feature-flag"
	"github.com/thomaspoignant/go-feature-flag/audience"
	"github.com/thomaspoignant/go-feature-flag/cmd/relayproxy/config"
	"github.com/thomaspoignant/go-feature-flag/cmd/relayproxy/service"
	"github.com/thomaspoignant/go-feature-flag/cmdhelpers/audienceconf"
	"github.com/thomaspoignant/go-feature-flag/cmdhelpers/retrieverconf"
	"github.com/thomaspoignant/go-feature-flag/ffcontext"
	"github.com/thomaspoignant/go-feature-flag/modules/core/flag"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func audienceStatus(t *testing.T, c *ffclient.GoFeatureFlag) string {
	t.Helper()
	res, err := c.RawVariation("aud-flag", ffcontext.NewEvaluationContext("bob"), false)
	require.NoError(t, err)
	s, _ := res.Metadata[flag.AudienceStatusMetadataKey].(string)
	return s
}

func TestFlagsetManager_OnConfigChange_Audiences(t *testing.T) {
	file := writeAudienceFlags(t)
	common := func(a *audienceconf.AudiencesConf, polling int) config.CommonFlagSet {
		return config.CommonFlagSet{
			Retriever:       &retrieverconf.RetrieverConf{Kind: retrieverconf.FileRetriever, Path: file},
			PollingInterval: polling,
			Audiences:       a,
		}
	}
	// the store is unreachable: the status tells which configuration is running (failMode
	// unknown: STORE_ERROR, failMode empty: HEALTHY).
	base := func() *audienceconf.AudiencesConf { return unreachableAudiences("goff:") }
	failEmpty := func() *audienceconf.AudiencesConf {
		a := base()
		a.FailMode = audience.FailModeEmpty
		return a
	}
	otherStore := func() *audienceconf.AudiencesConf {
		a := failEmpty()
		a.Provider.Options.Addrs = []string{"127.0.0.1:2"}
		return a
	}

	tests := []struct {
		name         string
		flagsets     bool
		initial      *audienceconf.AudiencesConf
		next         *audienceconf.AudiencesConf
		nextPolling  int
		wantApplied  bool
		wantStatus   string
		wantInfo     string
		wantErrorLog string
	}{
		{
			name: "default mode: only audiences changed", initial: base(), next: failEmpty(), nextPolling: 10000,
			wantApplied: true, wantStatus: flag.AudienceStatusHealthy, wantInfo: "audiences updated without restart",
		},
		{
			name: "flagsets mode: only audiences changed", flagsets: true, initial: base(), next: failEmpty(),
			nextPolling: 10000, wantApplied: true, wantStatus: flag.AudienceStatusHealthy,
			wantInfo: "audiences updated without restart",
		},
		{
			name: "default mode: another store", initial: base(), next: otherStore(), nextPolling: 10000,
			wantApplied: true, wantStatus: flag.AudienceStatusHealthy, wantInfo: "audiences updated without restart",
		},
		{
			name: "flagsets mode: another store", flagsets: true, initial: base(), next: otherStore(),
			nextPolling: 10000, wantApplied: true, wantStatus: flag.AudienceStatusHealthy,
			wantInfo: "audiences updated without restart",
		},
		{
			name: "flagsets mode: audiences and polling changed are rejected", flagsets: true, initial: base(),
			next: failEmpty(), nextPolling: 20000, wantStatus: flag.AudienceStatusStoreError,
			wantErrorLog: "modifying a flagset is not supported at runtime",
		},
		{
			name: "flagsets mode: audiences enabled at runtime is rejected", flagsets: true, initial: nil,
			next: failEmpty(), nextPolling: 10000, wantStatus: flag.AudienceStatusNotConfigured,
			wantErrorLog: "enabling or disabling the audiences is not supported",
		},
		{
			name: "default mode: audiences disabled at runtime is rejected", initial: base(), next: nil,
			nextPolling: 10000, wantStatus: flag.AudienceStatusStoreError,
			wantErrorLog: "enabling or disabling the audiences is not supported",
		},
		{
			name: "default mode: nothing changed", initial: base(), next: base(), nextPolling: 10000,
			wantStatus: flag.AudienceStatusStoreError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg, newCfg *config.Config
			if tt.flagsets {
				cfg = &config.Config{FlagSets: []config.FlagSet{
					{Name: "fs", APIKeys: []string{"k"}, CommonFlagSet: common(tt.initial, 10000)},
				}}
				newCfg = &config.Config{FlagSets: []config.FlagSet{
					{Name: "fs", APIKeys: []string{"k"}, CommonFlagSet: common(tt.next, tt.nextPolling)},
				}}
			} else {
				cfg = &config.Config{CommonFlagSet: common(tt.initial, 10000)}
				newCfg = &config.Config{CommonFlagSet: common(tt.next, tt.nextPolling)}
			}
			obs, logs := observer.New(zap.InfoLevel)
			manager, err := service.NewFlagsetManager(cfg, zap.New(obs), nil, nil)
			require.NoError(t, err)
			t.Cleanup(manager.Close)
			client := manager.Default()
			if tt.flagsets {
				client, err = manager.FlagSet("k")
				require.NoError(t, err)
			}

			manager.OnConfigChange(newCfg)

			assert.Eventually(t, func() bool { return audienceStatus(t, client) == tt.wantStatus },
				3*time.Second, 20*time.Millisecond)
			var infos, errs []string
			for _, e := range logs.All() {
				switch e.Level {
				case zap.InfoLevel:
					infos = append(infos, e.Message)
				case zap.ErrorLevel:
					if strings.HasPrefix(e.Message, "impossible to initialize the audience provider") {
						continue // the stores of the test are unreachable
					}
					errs = append(errs, e.Message)
				}
				// never log the store address nor credentials
				for _, f := range e.Context {
					assert.NotContains(t, f.String, "127.0.0.1")
				}
			}
			if tt.wantInfo != "" {
				assert.Contains(t, infos, "Configuration changed: "+tt.wantInfo)
			}
			if tt.wantErrorLog != "" {
				found := false
				for _, m := range errs {
					found = found || strings.HasPrefix(m, tt.wantErrorLog)
				}
				assert.True(t, found, "error %q not logged, got %v", tt.wantErrorLog, errs)
			} else {
				assert.Empty(t, errs)
			}

			// the configuration in use follows what is applied
			var got *audienceconf.AudiencesConf
			if tt.flagsets {
				got = cfg.GetFlagSets()[0].Audiences
			} else {
				got = cfg.GetAudiences()
			}
			if tt.wantApplied {
				assert.Equal(t, tt.next, got)
			} else {
				assert.Equal(t, tt.initial, got)
			}
		})
	}
}
