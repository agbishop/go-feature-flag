package ffclient_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ffclient "github.com/thomaspoignant/go-feature-flag"
	"github.com/thomaspoignant/go-feature-flag/audience"
	"github.com/thomaspoignant/go-feature-flag/audience/audiencetest"
	"github.com/thomaspoignant/go-feature-flag/ffcontext"
	"github.com/thomaspoignant/go-feature-flag/modules/core/flag"
	"github.com/thomaspoignant/go-feature-flag/utils/fflog"
)

// lifecycleProvider is a fake provider with a controllable Init and Exists.
type lifecycleProvider struct {
	*audiencetest.FakeProvider
	initFn   func(ctx context.Context) error
	existsFn func() (map[string]bool, error)
}

func (p *lifecycleProvider) Init(ctx context.Context, _ *fflog.FFLogger) error {
	if p.initFn != nil {
		return p.initFn(ctx)
	}
	return nil
}

func (p *lifecycleProvider) Exists(
	ctx context.Context, lists map[string]audience.ListSettings,
) (map[string]bool, error) {
	if p.existsFn != nil {
		return p.existsFn()
	}
	return p.FakeProvider.Exists(ctx, lists)
}

func TestAudiences_ProviderLifecycle(t *testing.T) {
	tests := []struct {
		name       string
		cfg        func() *ffclient.AudiencesConfig
		lookupCfg  audience.Config
		wantStatus string
		wantLog    string
	}{
		{
			name:       "no provider configured",
			cfg:        func() *ffclient.AudiencesConfig { return &ffclient.AudiencesConfig{} },
			wantStatus: flag.AudienceStatusStoreError,
			wantLog:    "audiences are configured without a provider",
		},
		{
			name: "init fails",
			cfg: func() *ffclient.AudiencesConfig {
				return &ffclient.AudiencesConfig{Provider: &lifecycleProvider{
					FakeProvider: audiencetest.NewFakeProvider(),
					initFn:       func(context.Context) error { return errors.New("no store") },
				}}
			},
			wantStatus: flag.AudienceStatusHealthy,
			wantLog:    "impossible to initialize the audience provider",
		},
		{
			name: "init panics",
			cfg: func() *ffclient.AudiencesConfig {
				return &ffclient.AudiencesConfig{Provider: &lifecycleProvider{
					FakeProvider: audiencetest.NewFakeProvider(),
					initFn:       func(context.Context) error { panic("boom") },
				}}
			},
			wantStatus: flag.AudienceStatusHealthy,
			wantLog:    "panicked while initializing",
		},
		{
			name: "slow init: lookups fail at their deadline",
			cfg: func() *ffclient.AudiencesConfig {
				return &ffclient.AudiencesConfig{
					Config: audience.Config{LookupTimeout: 20 * time.Millisecond},
					Provider: &lifecycleProvider{
						FakeProvider: audiencetest.NewFakeProvider(),
						initFn: func(ctx context.Context) error {
							<-ctx.Done()
							return ctx.Err()
						},
					},
				}
			},
			wantStatus: flag.AudienceStatusStoreError,
		},
		{
			name: "existence check fails",
			cfg: func() *ffclient.AudiencesConfig {
				return &ffclient.AudiencesConfig{Provider: &lifecycleProvider{
					FakeProvider: audiencetest.NewFakeProvider(),
					existsFn:     func() (map[string]bool, error) { return nil, errors.New("exists failed") },
				}}
			},
			wantStatus: flag.AudienceStatusHealthy,
			wantLog:    "impossible to check that the audiences used by the flags exist",
		},
		{
			name: "existence is unknown for a list",
			cfg: func() *ffclient.AudiencesConfig {
				return &ffclient.AudiencesConfig{Provider: &lifecycleProvider{
					FakeProvider: audiencetest.NewFakeProvider(),
					existsFn:     func() (map[string]bool, error) { return map[string]bool{}, nil },
				}}
			},
			wantStatus: flag.AudienceStatusHealthy,
			wantLog:    "impossible to tell if an audience exists in the store",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := &syncBuffer{}
			g := newAudienceClient(t, tt.cfg(), logs)
			res, err := g.RawVariationWithContext(
				context.Background(), "beta-flag", ffcontext.NewEvaluationContext("bob"), false)
			require.NoError(t, err)
			assert.Equal(t, false, res.Value)
			assert.NotEqual(t, flag.ReasonError, res.Reason)
			if tt.wantStatus == flag.AudienceStatusStoreError {
				assert.Equal(t, tt.wantStatus, res.Metadata[flag.AudienceStatusMetadataKey])
			}
			if tt.wantLog != "" {
				assert.Eventually(t, func() bool { return containsLog(logs, tt.wantLog) },
					2*time.Second, 10*time.Millisecond)
			}
		})
	}
}

func containsLog(logs *syncBuffer, s string) bool {
	return len(s) > 0 && strings.Contains(logs.String(), s)
}
