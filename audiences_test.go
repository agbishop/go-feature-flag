package ffclient_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ffclient "github.com/thomaspoignant/go-feature-flag"
	"github.com/thomaspoignant/go-feature-flag/audience"
	"github.com/thomaspoignant/go-feature-flag/audience/audiencetest"
	"github.com/thomaspoignant/go-feature-flag/ffcontext"
	"github.com/thomaspoignant/go-feature-flag/modules/core/flag"
	"github.com/thomaspoignant/go-feature-flag/retriever/fileretriever"
)

const audienceFlags = `
beta-flag:
  variations: { on: true, off: false }
  defaultRule: { variation: off }
  targeting:
    - name: beta
      query: targetingKey in audience("beta")
      variation: on
other-flag:
  variations: { on: true, off: false }
  defaultRule: { variation: off }
  targeting:
    - name: vip
      query: targetingKey in audience("vip")
      variation: on
plain-flag:
  variations: { on: true, off: false }
  defaultRule: { variation: on }
`

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newAudienceClient(
	t *testing.T, aud *ffclient.AudiencesConfig, logs *syncBuffer,
) *ffclient.GoFeatureFlag {
	t.Helper()
	file := filepath.Join(t.TempDir(), "flags.yaml")
	require.NoError(t, os.WriteFile(file, []byte(audienceFlags), 0o600))
	cfg := ffclient.Config{
		PollingInterval: time.Hour,
		Retriever:       &fileretriever.Retriever{Path: file},
		Audiences:       aud,
	}
	if logs != nil {
		cfg.LeveledLogger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	g, err := ffclient.New(cfg)
	require.NoError(t, err)
	t.Cleanup(g.Close)
	return g
}

func TestAudiences_RawVariationWithContext(t *testing.T) {
	tests := []struct {
		name       string
		noAudience bool
		setup      func(f *audiencetest.FakeProvider)
		cfg        audience.Config
		user       string
		timeoutCtx time.Duration
		wantValue  any
		wantStatus string
	}{
		{
			name: "member", user: "bob", wantValue: true, wantStatus: flag.AudienceStatusHealthy,
		},
		{
			name: "non member", user: "alice", wantValue: false, wantStatus: flag.AudienceStatusHealthy,
		},
		{
			name:  "store error: rule skipped",
			setup: func(f *audiencetest.FakeProvider) { f.FailAll(errors.New("boom")) },
			user:  "bob", wantValue: false, wantStatus: flag.AudienceStatusStoreError,
		},
		{
			name:  "store error with failMode empty: non member, healthy",
			setup: func(f *audiencetest.FakeProvider) { f.FailAll(errors.New("boom")) },
			cfg:   audience.Config{FailMode: audience.FailModeEmpty},
			user:  "bob", wantValue: false, wantStatus: flag.AudienceStatusHealthy,
		},
		{
			name:  "timeout",
			setup: func(f *audiencetest.FakeProvider) { f.SetDelay(2 * time.Second) },
			cfg:   audience.Config{LookupTimeout: 20 * time.Millisecond},
			user:  "bob", wantValue: false, wantStatus: flag.AudienceStatusStoreError,
		},
		{
			name:  "request context cancelled",
			setup: func(f *audiencetest.FakeProvider) { f.SetDelay(2 * time.Second) },
			user:  "bob", timeoutCtx: 10 * time.Millisecond, wantValue: false, wantStatus: flag.AudienceStatusStoreError,
		},
		{
			name: "not configured", noAudience: true, user: "bob", wantValue: false,
			wantStatus: flag.AudienceStatusNotConfigured,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := audiencetest.NewFakeProvider()
			require.NoError(t, fake.Add(context.Background(), "beta", audience.ListSettings{}, "bob"))
			if tt.setup != nil {
				tt.setup(fake)
			}
			var aud *ffclient.AudiencesConfig
			if !tt.noAudience {
				aud = &ffclient.AudiencesConfig{Provider: fake, Config: tt.cfg}
			}
			g := newAudienceClient(t, aud, nil)

			ctx := context.Background()
			if tt.timeoutCtx > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tt.timeoutCtx)
				defer cancel()
			}
			res, err := g.RawVariationWithContext(ctx, "beta-flag", ffcontext.NewEvaluationContext(tt.user), false)
			require.NoError(t, err)
			assert.Equal(t, tt.wantValue, res.Value)
			assert.Equal(t, tt.wantStatus, res.Metadata[flag.AudienceStatusMetadataKey])
			assert.False(t, res.Cacheable)
			assert.NotEqual(t, flag.ReasonError, res.Reason)
		})
	}
}

func TestAudiences_FlagWithoutAudienceNeverCallsProvider(t *testing.T) {
	fake := audiencetest.NewFakeProvider()
	g := newAudienceClient(t, &ffclient.AudiencesConfig{Provider: fake}, nil)
	res, err := g.RawVariation("plain-flag", ffcontext.NewEvaluationContext("bob"), false)
	require.NoError(t, err)
	assert.Equal(t, true, res.Value)
	assert.NotContains(t, res.Metadata, flag.AudienceStatusMetadataKey)
	assert.Equal(t, 0, fake.Calls())
}

func TestAudiences_BulkSingleContainsCall(t *testing.T) {
	tests := []struct {
		name  string
		flags []string
	}{
		{name: "all flags"},
		{name: "selected flags", flags: []string{"beta-flag", "other-flag", "plain-flag"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := audiencetest.NewFakeProvider()
			require.NoError(t, fake.Add(context.Background(), "beta", audience.ListSettings{}, "bob"))
			g := newAudienceClient(t, &ffclient.AudiencesConfig{Provider: fake}, nil)
			before := fake.Calls()
			states := g.GetFlagStatesWithContext(context.Background(), ffcontext.NewEvaluationContext("bob"), tt.flags)
			assert.Equal(t, 1, fake.Calls()-before)
			assert.Equal(t, 2, fake.LookupCount())
			assert.Equal(t, true, states.GetFlags()["beta-flag"].Value)
			assert.Equal(t, false, states.GetFlags()["other-flag"].Value)
			assert.Equal(t, flag.AudienceStatusHealthy,
				states.GetFlags()["beta-flag"].Metadata[flag.AudienceStatusMetadataKey])

			all := g.AllFlagsStateWithContext(context.Background(), ffcontext.NewEvaluationContext("bob"))
			assert.Equal(t, true, all.GetFlags()["beta-flag"].Value)
		})
	}
}

func TestAudiences_BulkNotConfigured(t *testing.T) {
	g := newAudienceClient(t, nil, nil)
	states := g.GetFlagStates(ffcontext.NewEvaluationContext("bob"), nil)
	assert.Equal(t, flag.AudienceStatusNotConfigured,
		states.GetFlags()["beta-flag"].Metadata[flag.AudienceStatusMetadataKey])
}

func TestAudiences_ExistenceWarning(t *testing.T) {
	fake := audiencetest.NewFakeProvider()
	require.NoError(t, fake.Add(context.Background(), "beta", audience.ListSettings{}, "bob"))
	logs := &syncBuffer{}
	g := newAudienceClient(t, &ffclient.AudiencesConfig{Provider: fake}, logs)
	// the check is asynchronous
	assert.Eventually(t, func() bool {
		return strings.Contains(logs.String(), "audience=vip")
	}, 2*time.Second, 10*time.Millisecond)
	assert.NotContains(t, logs.String(), "audience=beta")
	assert.NotContains(t, logs.String(), "bob")
	g.Close()
}

func TestAudiences_InvalidConfig(t *testing.T) {
	file := filepath.Join(t.TempDir(), "flags.yaml")
	require.NoError(t, os.WriteFile(file, []byte(audienceFlags), 0o600))
	_, err := ffclient.New(ffclient.Config{
		Retriever: &fileretriever.Retriever{Path: file},
		Audiences: &ffclient.AudiencesConfig{
			Provider: audiencetest.NewFakeProvider(),
			Config:   audience.Config{FailMode: "nope"},
		},
	})
	assert.Error(t, err)
}

func TestAudiences_CloseShutsDownProvider(t *testing.T) {
	fake := audiencetest.NewFakeProvider()
	g := newAudienceClient(t, &ffclient.AudiencesConfig{Provider: fake}, nil)
	assert.Eventually(t, func() bool { return fake.Status() == "READY" }, 2*time.Second, 5*time.Millisecond)
	g.Close()
	assert.Equal(t, "NOT_READY", fake.Status())
}
