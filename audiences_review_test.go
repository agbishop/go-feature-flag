package ffclient_test

import (
	"context"
	"os"
	"path/filepath"
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
	"github.com/thomaspoignant/go-feature-flag/utils/fflog"
)

// slowInitProvider blocks Init until release is closed or its context is done.
type slowInitProvider struct {
	*audiencetest.FakeProvider
	release  chan struct{}
	initErr  error
	mu       sync.Mutex
	existsOf [][]string
}

func (p *slowInitProvider) Init(ctx context.Context, l *fflog.FFLogger) error {
	select {
	case <-p.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	if p.initErr != nil {
		return p.initErr
	}
	return p.FakeProvider.Init(ctx, l)
}

func (p *slowInitProvider) Exists(ctx context.Context, lists map[string]audience.ListSettings) (map[string]bool, error) {
	names := make([]string, 0, len(lists))
	for n := range lists {
		names = append(names, n)
	}
	p.mu.Lock()
	p.existsOf = append(p.existsOf, names)
	p.mu.Unlock()
	return p.FakeProvider.Exists(ctx, lists)
}

func TestAudiences_BackgroundInit(t *testing.T) {
	tests := []struct {
		name       string
		cfg        audience.Config
		release    bool
		wantStatus string
		wantValue  any
	}{
		{"not ready: unknown term", audience.Config{}, false, flag.AudienceStatusStoreError, false},
		{
			"not ready with failMode empty: non member",
			audience.Config{FailMode: "empty"},
			false,
			flag.AudienceStatusHealthy, false,
		},
		{"ready: lookups work", audience.Config{}, true, flag.AudienceStatusHealthy, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &slowInitProvider{FakeProvider: audiencetest.NewFakeProvider(), release: make(chan struct{})}
			require.NoError(t, p.Add(context.Background(), "beta", audience.ListSettings{}, "bob"))
			start := time.Now()
			g := newAudienceClient(t, &ffclient.AudiencesConfig{Provider: p, Config: tt.cfg}, nil)
			assert.Less(t, time.Since(start), 2*time.Second, "New must not wait for the provider")
			if tt.release {
				close(p.release)
			}
			res, err := g.RawVariation("beta-flag", ffcontext.NewEvaluationContext("bob"), false)
			require.NoError(t, err)
			assert.Equal(t, tt.wantValue, res.Value)
			assert.Equal(t, tt.wantStatus, res.Metadata[flag.AudienceStatusMetadataKey])
		})
	}
	t.Run("lookups recover once the provider is ready", func(t *testing.T) {
		p := &slowInitProvider{FakeProvider: audiencetest.NewFakeProvider(), release: make(chan struct{})}
		require.NoError(t, p.Add(context.Background(), "beta", audience.ListSettings{}, "bob"))
		g := newAudienceClient(t, &ffclient.AudiencesConfig{Provider: p}, nil)
		res, _ := g.RawVariation("beta-flag", ffcontext.NewEvaluationContext("bob"), false)
		assert.Equal(t, flag.AudienceStatusStoreError, res.Metadata[flag.AudienceStatusMetadataKey])
		close(p.release)
		assert.Eventually(t, func() bool {
			r, _ := g.RawVariation("beta-flag", ffcontext.NewEvaluationContext("bob"), false)
			return r.Value == true
		}, 2*time.Second, 10*time.Millisecond)
	})
	t.Run("close cancels a hanging Init", func(t *testing.T) {
		p := &slowInitProvider{FakeProvider: audiencetest.NewFakeProvider(), release: make(chan struct{})}
		g := newAudienceClient(t, &ffclient.AudiencesConfig{Provider: p}, nil)
		done := make(chan struct{})
		go func() { g.Close(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("Close is blocked by the provider Init")
		}
	})
}

func TestAudiences_NilEvaluationContext(t *testing.T) {
	g := newAudienceClient(t, &ffclient.AudiencesConfig{Provider: audiencetest.NewFakeProvider()}, nil)
	assert.NotPanics(t, func() {
		res, err := g.RawVariation("beta-flag", nil, false)
		require.NoError(t, err)
		assert.Equal(t, false, res.Value)
	})
	assert.NotPanics(t, func() { g.GetFlagStates(nil, nil) })
}

func TestAudiences_EnrichmentIsUsedForTheLookup(t *testing.T) {
	const flags = `
tenant-flag:
  variations: { on: true, off: false }
  defaultRule: { variation: off }
  targeting:
    - query: tenant in audience("big")
      variation: on
env-flag:
  variations: { on: true, off: false }
  defaultRule: { variation: off }
  targeting:
    - query: env in audience("envs")
      variation: on
`
	tests := []struct {
		name     string
		flagName string
		user     ffcontext.Context
		want     any
	}{
		{"enrichment only attribute", "tenant-flag", ffcontext.NewEvaluationContext("u"), true},
		{
			"enrichment overrides the request value", "tenant-flag",
			ffcontext.NewEvaluationContextBuilder("u").AddCustom("tenant", "small").Build(), true,
		},
		{"automatic env", "env-flag", ffcontext.NewEvaluationContext("u"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := audiencetest.NewFakeProvider()
			require.NoError(t, fake.Add(context.Background(), "big", audience.ListSettings{}, "acme"))
			require.NoError(t, fake.Add(context.Background(), "envs", audience.ListSettings{}, "prod"))
			file := filepath.Join(t.TempDir(), "flags.yaml")
			require.NoError(t, os.WriteFile(file, []byte(flags), 0o600))
			g, err := ffclient.New(ffclient.Config{
				PollingInterval:             time.Hour,
				Retriever:                   &fileretriever.Retriever{Path: file},
				Audiences:                   &ffclient.AudiencesConfig{Provider: fake},
				EvaluationContextEnrichment: map[string]any{"tenant": "acme"},
				Environment:                 "prod",
			})
			require.NoError(t, err)
			defer g.Close()
			res, err := g.RawVariation(tt.flagName, tt.user, false)
			require.NoError(t, err)
			assert.Equal(t, tt.want, res.Value)
			assert.Equal(t, flag.AudienceStatusHealthy, res.Metadata[flag.AudienceStatusMetadataKey])
			// the bulk path agrees
			states := g.GetFlagStates(tt.user, []string{tt.flagName})
			assert.Equal(t, tt.want, states.GetFlags()[tt.flagName].Value)
		})
	}
}

func TestAudiences_ConfirmedListsAreNotCheckedAgain(t *testing.T) {
	p := &slowInitProvider{FakeProvider: audiencetest.NewFakeProvider(), release: make(chan struct{})}
	close(p.release)
	require.NoError(t, p.Add(context.Background(), "beta", audience.ListSettings{}, "bob"))
	file := filepath.Join(t.TempDir(), "flags.yaml")
	require.NoError(t, os.WriteFile(file, []byte(audienceFlags), 0o600))
	g, err := ffclient.New(ffclient.Config{
		PollingInterval: 50 * time.Millisecond,
		Retriever:       &fileretriever.Retriever{Path: file},
		Audiences:       &ffclient.AudiencesConfig{Provider: p},
	})
	require.NoError(t, err)
	defer g.Close()
	calls := func() [][]string {
		p.mu.Lock()
		defer p.mu.Unlock()
		return append([][]string(nil), p.existsOf...)
	}
	require.Eventually(t, func() bool { return len(calls()) >= 1 }, 2*time.Second, 10*time.Millisecond)
	assert.ElementsMatch(t, []string{"beta", "vip"}, calls()[0])

	time.Sleep(300 * time.Millisecond) // let the initial checks settle and confirm beta
	settled := len(calls())

	// change beta-flag only: beta is confirmed, nothing to check.
	// change other-flag: vip is still missing, it is checked again, alone.
	changed := audienceFlags
	changed = replaceOnce(changed, "beta-flag:\n  variations: { on: true, off: false }", "beta-flag:\n  variations: { on: true, off: false, other: true }")
	changed = replaceOnce(changed, "other-flag:\n  variations: { on: true, off: false }", "other-flag:\n  variations: { on: true, off: false, other: true }")
	require.NoError(t, os.WriteFile(file, []byte(changed), 0o600))
	require.Eventually(t, func() bool { return len(calls()) > settled }, 3*time.Second, 20*time.Millisecond)
	for _, c := range calls()[settled:] {
		assert.Equal(t, []string{"vip"}, c)
	}
}

func replaceOnce(s, old, repl string) string {
	for i := 0; i+len(old) <= len(s); i++ {
		if s[i:i+len(old)] == old {
			return s[:i] + repl + s[i+len(old):]
		}
	}
	return s
}
