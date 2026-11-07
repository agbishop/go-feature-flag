package audience_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thomaspoignant/go-feature-flag/audience"
	"github.com/thomaspoignant/go-feature-flag/modules/core/ffcontext"
	"github.com/thomaspoignant/go-feature-flag/modules/core/flag"
	"github.com/thomaspoignant/go-feature-flag/utils/fflog"
)

func TestResolver_EnrichmentAndNilContext(t *testing.T) {
	ctx := context.Background()
	tenantCtx := func(v string) ffcontext.Context {
		return ffcontext.NewEvaluationContextBuilder("bob").AddCustom("tenant", v).Build()
	}
	tests := []struct {
		name       string
		evalCtx    ffcontext.Context
		enrichment map[string]any
		attribute  string
		wantLookup string // "" means no entry
	}{
		{"enrichment only attribute", ffcontext.NewEvaluationContext("bob"), map[string]any{"tenant": "big"}, "tenant", "big"},
		{"enrichment overrides the request", tenantCtx("small"), map[string]any{"tenant": "big"}, "tenant", "big"},
		{"request value without enrichment", tenantCtx("small"), nil, "tenant", "small"},
		{"automatic env", ffcontext.NewEvaluationContext("bob"), map[string]any{"env": "prod"}, "env", "prod"},
		{"nil context uses the targeting key of an empty context", nil, nil, "targetingKey", ""},
		{"nil context with enrichment", nil, map[string]any{"tenant": "big"}, "tenant", "big"},
		{"missing attribute", ffcontext.NewEvaluationContext("bob"), nil, "tenant", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			require.NoError(t, f.Add(ctx, "big", audience.ListSettings{}, "big"))
			r := audience.NewResolver(f, audience.Config{}, nil, nil)
			terms := []flag.AudienceTerm{term("big", tt.attribute)}
			var got flag.AudienceMembership
			require.NotPanics(t, func() { got = r.Resolve(ctx, terms, tt.evalCtx, tt.enrichment) })
			if tt.wantLookup == "" && tt.attribute == "tenant" {
				assert.Empty(t, got)
				return
			}
			assert.Contains(t, got, look("big", tt.wantLookup))
			assert.Equal(t, tt.wantLookup == "big", got[look("big", tt.wantLookup)].Member)
		})
	}
	t.Run("ResolveAll and nil context", func(t *testing.T) {
		f := newFake(t)
		r := audience.NewResolver(f, audience.Config{}, nil, nil)
		require.NotPanics(t, func() { r.ResolveAll(ctx, map[string]flag.Flag{}, nil, nil) })
	})
	t.Run("the enrichment does not modify the context", func(t *testing.T) {
		c := tenantCtx("small")
		f := newFake(t)
		audience.NewResolver(f, audience.Config{}, nil, nil).
			Resolve(ctx, []flag.AudienceTerm{term("big", "tenant")}, c, map[string]any{"tenant": "big"})
		assert.Equal(t, "small", c.GetCustom()["tenant"])
	})
}

func TestResolver_FailModeEmptyWithCancelledContext(t *testing.T) {
	f := newFake(t)
	require.NoError(t, f.Add(context.Background(), "beta", audience.ListSettings{}, "bob"))
	tests := []struct {
		name string
		mode string
		want flag.AudienceResult
	}{
		{"unknown", "unknown", flag.AudienceResult{Err: true}},
		{"empty", "empty", flag.AudienceResult{Member: false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := audience.NewResolver(f, audience.Config{FailMode: tt.mode}, nil, nil)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			got := r.Resolve(ctx, []flag.AudienceTerm{term("beta", "key")}, ctxFor("bob"), nil)
			assert.Equal(t, tt.want, got[look("beta", "bob")])
		})
	}
}

func TestResolver_NoGoroutineLeak(t *testing.T) {
	runtime.GC()
	before := runtime.NumGoroutine()
	for i := 0; i < 20; i++ {
		r := audience.NewResolver(newFake(t), audience.Config{Cache: audience.CacheConfig{Size: 10, TTL: time.Minute}}, nil, nil)
		r.Resolve(context.Background(), []flag.AudienceTerm{term("beta", "key")}, ctxFor("bob"), nil)
		r.Close()
		r.Close() // idempotent
	}
	assert.Eventually(t, func() bool { runtime.GC(); return runtime.NumGoroutine() <= before+2 },
		2*time.Second, 20*time.Millisecond, "goroutines before=%d after=%d", before, runtime.NumGoroutine())
}

func TestResolver_CheckExistsUsesItsOwnTimeout(t *testing.T) {
	tests := []struct {
		name          string
		existsTimeout time.Duration
		delay         time.Duration
		wantErr       bool
	}{
		{"slower than the lookup timeout but within the exists timeout", 2 * time.Second, 150 * time.Millisecond, false},
		{"slower than the exists timeout", 50 * time.Millisecond, 2 * time.Second, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			f.SetDelay(tt.delay)
			r := audience.NewResolver(f, audience.Config{LookupTimeout: 10 * time.Millisecond}, nil, nil)
			audience.SetExistsTimeout(r, tt.existsTimeout)
			_, err := r.CheckExists(context.Background(), []string{"beta"})
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }

func (s *syncBuf) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func TestResolver_StoreErrorLogging(t *testing.T) {
	buf := &syncBuf{}
	logger := &fflog.FFLogger{LeveledLogger: slog.New(slog.NewTextHandler(buf, nil))}
	f := newFake(t)
	require.NoError(t, f.Add(context.Background(), "beta", audience.ListSettings{}, "secret-member-id"))
	r := audience.NewResolver(f, audience.Config{}, nil, logger)
	now := time.Unix(1000, 0)
	audience.SetClock(r, func() time.Time { return now })
	terms := []flag.AudienceTerm{term("beta", "key")}
	resolve := func() { r.Resolve(context.Background(), terms, ctxFor("secret-member-id"), nil) }
	count := func(s string) int { return strings.Count(buf.String(), s) }

	steps := []struct {
		name     string
		do       func()
		warns    int
		infos    int
		describe string
	}{
		{"first failure warns", func() { f.FailAll(errors.New("connection refused")); resolve() }, 1, 0, ""},
		{"second failure is silent", func() { now = now.Add(10 * time.Second); resolve() }, 1, 0, ""},
		{"after the interval warns again", func() { now = now.Add(time.Minute); resolve() }, 2, 0, ""},
		{"recovery logs info", func() { f.FailAll(nil); resolve() }, 2, 1, ""},
		{"no recovery log twice", func() { resolve() }, 2, 1, ""},
		{"a new failure warns immediately", func() { f.FailAll(errors.New("boom")); resolve() }, 3, 1, ""},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			st.do()
			assert.Equal(t, st.warns, count("level=WARN"), buf.String())
			assert.Equal(t, st.infos, count("level=INFO"), buf.String())
		})
	}
	assert.Contains(t, buf.String(), "connection refused")
	assert.NotContains(t, buf.String(), "secret-member-id")
	assert.NotContains(t, buf.String(), "beta")
}

func TestResolver_CancelledRequestIsNotLoggedAsStoreFailure(t *testing.T) {
	buf := &syncBuf{}
	logger := &fflog.FFLogger{LeveledLogger: slog.New(slog.NewTextHandler(buf, nil))}
	r := audience.NewResolver(newFake(t), audience.Config{}, nil, logger)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.Resolve(ctx, []flag.AudienceTerm{term("beta", "key")}, ctxFor("bob"), nil)
	assert.Empty(t, buf.String())
}
