package audience_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thomaspoignant/go-feature-flag/audience"
	"github.com/thomaspoignant/go-feature-flag/audience/audiencetest"
	"github.com/thomaspoignant/go-feature-flag/modules/core/ffcontext"
	"github.com/thomaspoignant/go-feature-flag/modules/core/flag"
)

type recMetrics struct {
	mu      sync.Mutex
	lookups map[string]int
	cache   map[string]int
	unknown map[string]int
	obs     map[string]string
}

func newRec() *recMetrics {
	return &recMetrics{lookups: map[string]int{}, cache: map[string]int{}, unknown: map[string]int{}, obs: map[string]string{}}
}
func (m *recMetrics) IncLookup(l, r string) { m.mu.Lock(); m.lookups[l+"/"+r]++; m.mu.Unlock() }
func (m *recMetrics) IncCache(l, o string)  { m.mu.Lock(); m.cache[l+"/"+o]++; m.mu.Unlock() }
func (m *recMetrics) IncUnknown(l, r string) {
	m.mu.Lock()
	m.unknown[l+"/"+r]++
	m.mu.Unlock()
}

func (m *recMetrics) ObserveLookupDuration(l, layout string, _ time.Duration) {
	m.mu.Lock()
	m.obs[l] = layout
	m.mu.Unlock()
}

func term(list, attr string) flag.AudienceTerm {
	return flag.AudienceTerm{List: list, Attribute: attr, Key: flag.AudienceAttributeKey(list, attr)}
}

func look(list, v string) flag.AudienceLookup { return flag.AudienceLookup{List: list, Value: v} }

func ctxFor(key string) ffcontext.Context {
	return ffcontext.NewEvaluationContextBuilder(key).AddCustom("email", "Bob@x.com").AddCustom("age", 42).Build()
}

func newFake(t *testing.T) *audiencetest.FakeProvider {
	t.Helper()
	f := audiencetest.NewFakeProvider()
	require.NoError(t, f.Init(context.Background(), nil))
	return f
}

func TestResolver_Resolve(t *testing.T) {
	tests := []struct {
		name  string
		cfg   audience.Config
		terms []flag.AudienceTerm
		want  flag.AudienceMembership
	}{
		{
			name:  "member and not member",
			terms: []flag.AudienceTerm{term("beta", "targetingKey"), term("vip", "targetingKey")},
			want: flag.AudienceMembership{
				look("beta", "bob"): {Member: true},
				look("vip", "bob"):  {Member: false},
			},
		},
		{
			name:  "nested custom attribute and duplicates",
			terms: []flag.AudienceTerm{term("beta", "email"), term("beta", "email"), term("beta", "key")},
			want: flag.AudienceMembership{
				look("beta", "Bob@x.com"): {Member: false},
				look("beta", "bob"):       {Member: true},
			},
		},
		{
			name:  "missing and non string attributes have no entry",
			terms: []flag.AudienceTerm{term("beta", "nope"), term("beta", "age"), term("beta", "a.b")},
			want:  flag.AudienceMembership{},
		},
		{
			name: "values are normalized and hashed before the lookup",
			cfg: audience.Config{Definitions: map[string]audience.ListSettings{
				"emails": {Normalize: "trimLower", Hash: "sha256"},
			}},
			terms: []flag.AudienceTerm{term("emails", "email")},
			want:  flag.AudienceMembership{look("emails", "Bob@x.com"): {Member: true}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			ctx := context.Background()
			require.NoError(t, f.Add(ctx, "beta", audience.ListSettings{}, "bob"))
			hs := audience.ListSettings{Normalize: "trimLower", Hash: "sha256"}
			require.NoError(t, f.Add(ctx, "emails", hs, audience.EncodeMember("bob@x.com", hs)))
			r := audience.NewResolver(f, tt.cfg, nil, nil)
			assert.Equal(t, tt.want, r.Resolve(ctx, tt.terms, ctxFor("bob"), nil))
		})
	}

	t.Run("no terms does not call the provider", func(t *testing.T) {
		f := newFake(t)
		got := audience.NewResolver(f, audience.Config{}, nil, nil).Resolve(context.Background(), nil, ctxFor("bob"), nil)
		assert.NotNil(t, got)
		assert.Empty(t, got)
		assert.Zero(t, f.Calls())
	})

	t.Run("one Contains call for all the lists and the provider gets the settings", func(t *testing.T) {
		f := newFake(t)
		cfg := audience.Config{Definitions: map[string]audience.ListSettings{"a": {Layout: "keys", Prefix: "p:"}}}
		r := audience.NewResolver(f, cfg, nil, nil)
		r.Resolve(context.Background(), []flag.AudienceTerm{term("a", "key"), term("b", "key"), term("c", "key")}, ctxFor("bob"), nil)
		assert.Equal(t, 1, f.Calls())
		l := f.LastLookups()
		require.Len(t, l, 3)
		assert.Equal(t, "keys", l[0].Settings.Layout)
		assert.Equal(t, "p:", l[0].Settings.Prefix)
		assert.Equal(t, "bob", l[0].Member)
	})
}

// TestResolver_NeverMissingEntry is the contract with the core evaluation: every (list, value)
// pair of the terms with a string attribute has an entry, whatever happens to the store.
func TestResolver_NeverMissingEntry(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name    string
		cfg     audience.Config
		setup   func(f *audiencetest.FakeProvider)
		nilProv bool
		timeout bool
		wantA   flag.AudienceResult
		wantB   flag.AudienceResult
	}{
		{
			name:  "healthy",
			setup: func(*audiencetest.FakeProvider) {},
			wantA: flag.AudienceResult{Member: false}, wantB: flag.AudienceResult{Member: false},
		},
		{
			name:  "whole call fails",
			setup: func(f *audiencetest.FakeProvider) { f.FailAll(boom) },
			wantA: flag.AudienceResult{Err: true}, wantB: flag.AudienceResult{Err: true},
		},
		{
			name:  "whole call fails with failMode empty",
			cfg:   audience.Config{FailMode: "empty"},
			setup: func(f *audiencetest.FakeProvider) { f.FailAll(boom) },
			wantA: flag.AudienceResult{}, wantB: flag.AudienceResult{},
		},
		{
			name:  "one list fails",
			setup: func(f *audiencetest.FakeProvider) { f.FailList("a", boom) },
			wantA: flag.AudienceResult{Err: true}, wantB: flag.AudienceResult{},
		},
		{
			name: "per list failMode override",
			cfg: audience.Config{
				Definitions: map[string]audience.ListSettings{"a": {FailMode: "empty"}},
			},
			setup: func(f *audiencetest.FakeProvider) { f.FailAll(boom) },
			wantA: flag.AudienceResult{}, wantB: flag.AudienceResult{Err: true},
		},
		{
			name:    "timeout",
			cfg:     audience.Config{LookupTimeout: 20 * time.Millisecond},
			setup:   func(f *audiencetest.FakeProvider) { f.SetDelay(2 * time.Second) },
			wantA:   flag.AudienceResult{Err: true},
			wantB:   flag.AudienceResult{Err: true},
			timeout: true,
		},
		{
			name:  "timeout with failMode empty",
			cfg:   audience.Config{LookupTimeout: 20 * time.Millisecond, FailMode: "empty"},
			setup: func(f *audiencetest.FakeProvider) { f.SetDelay(2 * time.Second) },
			wantA: flag.AudienceResult{}, wantB: flag.AudienceResult{},
		},
		{
			name: "no provider", nilProv: true,
			wantA: flag.AudienceResult{Err: true}, wantB: flag.AudienceResult{Err: true},
		},
		{
			name: "no provider and failMode empty", nilProv: true, cfg: audience.Config{FailMode: "empty"},
			wantA: flag.AudienceResult{}, wantB: flag.AudienceResult{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			if tt.setup != nil {
				tt.setup(f)
			}
			var p audience.Provider = f
			if tt.nilProv {
				p = nil
			}
			m := newRec()
			r := audience.NewResolver(p, tt.cfg, m, nil)
			terms := []flag.AudienceTerm{term("a", "key"), term("b", "key"), term("a", "nope")}
			got := r.Resolve(context.Background(), terms, ctxFor("bob"), nil)
			require.Contains(t, got, look("a", "bob"))
			require.Contains(t, got, look("b", "bob"))
			assert.Len(t, got, 2)
			assert.Equal(t, tt.wantA, got[look("a", "bob")])
			assert.Equal(t, tt.wantB, got[look("b", "bob")])
			assert.Equal(t, 1, m.unknown["a/missing_attribute"])
			if tt.timeout {
				assert.Equal(t, 1, m.unknown["a/timeout"])
				assert.Equal(t, 1, m.unknown["b/timeout"])
			}
		})
	}

	t.Run("wrong number of results is an error for all pairs", func(t *testing.T) {
		r := audience.NewResolver(shortProvider{audiencetest.NewFakeProvider()}, audience.Config{}, nil, nil)
		got := r.Resolve(context.Background(), []flag.AudienceTerm{term("a", "key"), term("b", "key")}, ctxFor("bob"), nil)
		assert.Equal(t, flag.AudienceMembership{
			look("a", "bob"): {Err: true}, look("b", "bob"): {Err: true},
		}, got)
	})
	t.Run("panicking provider", func(t *testing.T) {
		r := audience.NewResolver(panicProvider{audiencetest.NewFakeProvider()}, audience.Config{}, nil, nil)
		got := r.Resolve(context.Background(), []flag.AudienceTerm{term("a", "key")}, ctxFor("bob"), nil)
		assert.Equal(t, flag.AudienceMembership{look("a", "bob"): {Err: true}}, got)
	})
	t.Run("cancelled caller context", func(t *testing.T) {
		f := newFake(t)
		f.SetDelay(time.Second)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		got := audience.NewResolver(f, audience.Config{}, nil, nil).
			Resolve(ctx, []flag.AudienceTerm{term("a", "key")}, ctxFor("bob"), nil)
		assert.Equal(t, flag.AudienceMembership{look("a", "bob"): {Err: true}}, got)
	})
}

type shortProvider struct{ *audiencetest.FakeProvider }

func (shortProvider) Contains(context.Context, []audience.Lookup) ([]audience.Result, error) {
	return []audience.Result{{}}, nil
}

type panicProvider struct{ *audiencetest.FakeProvider }

func (panicProvider) Contains(context.Context, []audience.Lookup) ([]audience.Result, error) {
	panic("kaboom")
}

func TestResolver_PerResultErrorIsHandledIndividually(t *testing.T) {
	f := newFake(t)
	require.NoError(t, f.Add(context.Background(), "ok", audience.ListSettings{}, "bob"))
	f.FailList("ko", errors.New("nope"))
	r := audience.NewResolver(f, audience.Config{Cache: audience.CacheConfig{Size: 10, TTL: time.Minute}}, nil, nil)
	got := r.Resolve(context.Background(), []flag.AudienceTerm{term("ok", "key"), term("ko", "key")}, ctxFor("bob"), nil)
	assert.Equal(t, flag.AudienceResult{Member: true}, got[look("ok", "bob")])
	assert.Equal(t, flag.AudienceResult{Err: true}, got[look("ko", "bob")])
}

func TestResolver_Cache(t *testing.T) {
	cacheCfg := audience.Config{Cache: audience.CacheConfig{Size: 10, TTL: time.Minute}}
	terms := []flag.AudienceTerm{term("beta", "key")}
	ctx := context.Background()

	t.Run("hit after miss, with metrics", func(t *testing.T) {
		f := newFake(t)
		require.NoError(t, f.Add(ctx, "beta", audience.ListSettings{}, "bob"))
		m := newRec()
		r := audience.NewResolver(f, cacheCfg, m, nil)
		for i := 0; i < 3; i++ {
			got := r.Resolve(ctx, terms, ctxFor("bob"), nil)
			assert.Equal(t, flag.AudienceResult{Member: true}, got[look("beta", "bob")])
		}
		assert.Equal(t, 1, f.Calls())
		assert.Equal(t, 1, m.cache["beta/miss"])
		assert.Equal(t, 2, m.cache["beta/hit"])
		assert.Equal(t, 3, m.lookups["beta/member"])
		assert.Equal(t, "set", m.obs["beta"])
	})
	t.Run("negative answers are cached too", func(t *testing.T) {
		f := newFake(t)
		r := audience.NewResolver(f, cacheCfg, nil, nil)
		r.Resolve(ctx, terms, ctxFor("bob"), nil)
		got := r.Resolve(ctx, terms, ctxFor("bob"), nil)
		assert.Equal(t, flag.AudienceResult{}, got[look("beta", "bob")])
		assert.Equal(t, 1, f.Calls())
	})
	t.Run("different values and lists are different entries", func(t *testing.T) {
		f := newFake(t)
		r := audience.NewResolver(f, cacheCfg, nil, nil)
		r.Resolve(ctx, terms, ctxFor("bob"), nil)
		r.Resolve(ctx, terms, ctxFor("alice"), nil)
		r.Resolve(ctx, []flag.AudienceTerm{term("other", "key")}, ctxFor("bob"), nil)
		assert.Equal(t, 3, f.Calls())
	})
	t.Run("only the misses are sent to the provider", func(t *testing.T) {
		f := newFake(t)
		r := audience.NewResolver(f, cacheCfg, nil, nil)
		r.Resolve(ctx, terms, ctxFor("bob"), nil)
		r.Resolve(ctx, []flag.AudienceTerm{term("beta", "key"), term("vip", "key")}, ctxFor("bob"), nil)
		require.Len(t, f.LastLookups(), 1)
		assert.Equal(t, "vip", f.LastLookups()[0].List)
	})
	t.Run("ttl expiry shows membership changes", func(t *testing.T) {
		f := newFake(t)
		r := audience.NewResolver(f, audience.Config{Cache: audience.CacheConfig{Size: 10, TTL: time.Minute}}, nil, nil)
		now := time.Unix(1000, 0)
		audience.SetClock(r, func() time.Time { return now })
		got := r.Resolve(ctx, terms, ctxFor("bob"), nil)
		assert.False(t, got[look("beta", "bob")].Member)
		require.NoError(t, f.Add(ctx, "beta", audience.ListSettings{}, "bob"))
		now = now.Add(59 * time.Second)
		assert.False(t, r.Resolve(ctx, terms, ctxFor("bob"), nil)[look("beta", "bob")].Member, "still cached")
		now = now.Add(2 * time.Second)
		assert.True(t, r.Resolve(ctx, terms, ctxFor("bob"), nil)[look("beta", "bob")].Member, "expired")
		assert.Equal(t, 2, f.Calls())
	})
	t.Run("errors are never cached", func(t *testing.T) {
		f := newFake(t)
		require.NoError(t, f.Add(ctx, "beta", audience.ListSettings{}, "bob"))
		f.FailAll(errors.New("down"))
		r := audience.NewResolver(f, cacheCfg, nil, nil)
		assert.True(t, r.Resolve(ctx, terms, ctxFor("bob"), nil)[look("beta", "bob")].Err)
		f.FailAll(nil)
		assert.Equal(t, flag.AudienceResult{Member: true}, r.Resolve(ctx, terms, ctxFor("bob"), nil)[look("beta", "bob")])
		// a failMode empty answer is not cached either.
		f2 := newFake(t)
		require.NoError(t, f2.Add(ctx, "beta", audience.ListSettings{}, "bob"))
		f2.FailList("beta", errors.New("down"))
		cfg := cacheCfg
		cfg.FailMode = "empty"
		r2 := audience.NewResolver(f2, cfg, nil, nil)
		assert.False(t, r2.Resolve(ctx, terms, ctxFor("bob"), nil)[look("beta", "bob")].Member)
		f2.FailList("beta", nil)
		assert.True(t, r2.Resolve(ctx, terms, ctxFor("bob"), nil)[look("beta", "bob")].Member)
	})
	t.Run("size 0 disables the cache and its metrics", func(t *testing.T) {
		f := newFake(t)
		m := newRec()
		r := audience.NewResolver(f, audience.Config{}, m, nil)
		r.Resolve(ctx, terms, ctxFor("bob"), nil)
		r.Resolve(ctx, terms, ctxFor("bob"), nil)
		assert.Equal(t, 2, f.Calls())
		assert.Empty(t, m.cache)
	})
}

func TestResolver_Metrics(t *testing.T) {
	f := newFake(t)
	ctx := context.Background()
	require.NoError(t, f.Add(ctx, "in", audience.ListSettings{}, "bob"))
	f.FailList("ko", errors.New("down"))
	m := newRec()
	cfg := audience.Config{Definitions: map[string]audience.ListSettings{"in": {Layout: "keys"}}}
	r := audience.NewResolver(f, cfg, m, nil)
	r.Resolve(ctx, []flag.AudienceTerm{term("in", "key"), term("out", "key"), term("ko", "key"), term("in", "zzz")}, ctxFor("bob"), nil)
	assert.Equal(t, map[string]int{"in/member": 1, "out/not_member": 1, "ko/unknown": 1}, m.lookups)
	assert.Equal(t, map[string]int{"ko/store_error": 1, "in/missing_attribute": 1}, m.unknown)
	assert.Equal(t, map[string]string{"in": "keys", "out": "set", "ko": "set"}, m.obs)
}

func TestResolver_SingleflightDedup(t *testing.T) {
	f := newFake(t)
	f.SetDelay(100 * time.Millisecond)
	require.NoError(t, f.Add(context.Background(), "beta", audience.ListSettings{}, "bob"))
	r := audience.NewResolver(f, audience.Config{LookupTimeout: 5 * time.Second}, nil, nil)
	const n = 20
	var wg sync.WaitGroup
	results := make([]flag.AudienceMembership, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i] = r.Resolve(context.Background(), []flag.AudienceTerm{term("beta", "key")}, ctxFor("bob"), nil)
		}(i)
	}
	close(start)
	wg.Wait()
	for i, res := range results {
		assert.Equal(t, flag.AudienceResult{Member: true}, res[look("beta", "bob")], fmt.Sprint(i))
	}
	assert.Less(t, f.Calls(), n/2, "concurrent identical lookups must be coalesced")
}

func TestResolver_SingleflightLeaderCancelDoesNotFailFollowers(t *testing.T) {
	f := newFake(t)
	f.SetDelay(150 * time.Millisecond)
	require.NoError(t, f.Add(context.Background(), "beta", audience.ListSettings{}, "bob"))
	r := audience.NewResolver(f, audience.Config{LookupTimeout: 5 * time.Second}, nil, nil)
	leaderCtx, cancel := context.WithCancel(context.Background())
	done := make(chan flag.AudienceMembership)
	go func() { done <- r.Resolve(leaderCtx, []flag.AudienceTerm{term("beta", "key")}, ctxFor("bob"), nil) }()
	time.Sleep(30 * time.Millisecond)
	follower := make(chan flag.AudienceMembership)
	go func() {
		follower <- r.Resolve(context.Background(), []flag.AudienceTerm{term("beta", "key")}, ctxFor("bob"), nil)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	assert.True(t, (<-done)[look("beta", "bob")].Err)
	assert.Equal(t, flag.AudienceResult{Member: true}, (<-follower)[look("beta", "bob")])
}

func TestResolver_ResolveAll(t *testing.T) {
	f := newFake(t)
	ctx := context.Background()
	require.NoError(t, f.Add(ctx, "beta", audience.ListSettings{}, "bob"))
	q1 := `targetingKey in audience("beta")`
	q2 := `targetingKey in audience("vip") or email in audience("beta")`
	q3 := `plan eq "pro"`
	flags := map[string]flag.Flag{
		"f1":    &flag.InternalFlag{Rules: &[]flag.Rule{{Query: &q1}}},
		"f2":    &flag.InternalFlag{Rules: &[]flag.Rule{{Query: &q2}}},
		"f3":    &flag.InternalFlag{Rules: &[]flag.Rule{{Query: &q3}}},
		"other": nil,
	}
	r := audience.NewResolver(f, audience.Config{}, nil, nil)
	got := r.ResolveAll(ctx, flags, ctxFor("bob"), nil)
	assert.Equal(t, 1, f.Calls(), "one Contains for all the flags")
	assert.Equal(t, flag.AudienceMembership{
		look("beta", "bob"):       {Member: true},
		look("vip", "bob"):        {},
		look("beta", "Bob@x.com"): {},
	}, got)
}

func TestNopMetrics(t *testing.T) {
	var m audience.Metrics = audience.NopMetrics{}
	assert.NotPanics(t, func() {
		m.IncLookup("a", "member")
		m.IncCache("a", "hit")
		m.IncUnknown("a", "timeout")
		m.ObserveLookupDuration("a", "set", time.Second)
	})
}

func TestNewResolverFrom(t *testing.T) {
	ctx := context.Background()
	cache := audience.CacheConfig{Size: 10, TTL: time.Minute}
	terms := []flag.AudienceTerm{term("beta", "key"), term("vip", "key")}
	tests := []struct {
		name        string
		prevCfg     audience.Config
		newCfg      audience.Config
		nilPrev     bool
		expirePrev  bool // the entries of prev are expired when the new resolver is built
		wantLookups int  // lookups sent to the store when resolving after the swap
	}{
		{name: "same config keeps every entry", prevCfg: audience.Config{Cache: cache}, newCfg: audience.Config{Cache: cache}},
		{
			name:    "ttl change alone keeps the entries",
			prevCfg: audience.Config{Cache: cache},
			newCfg:  audience.Config{Cache: audience.CacheConfig{Size: 10, TTL: time.Hour}},
		},
		{
			name:    "a list with another prefix is dropped",
			prevCfg: audience.Config{Cache: cache},
			newCfg: audience.Config{
				Cache:       cache,
				Definitions: map[string]audience.ListSettings{"beta": {Prefix: "p:"}},
			},
			wantLookups: 1,
		},
		{
			name:    "a list with another layout is dropped",
			prevCfg: audience.Config{Cache: cache},
			newCfg: audience.Config{
				Cache:       cache,
				Definitions: map[string]audience.ListSettings{"vip": {Layout: audience.LayoutKeys}},
			},
			wantLookups: 1,
		},
		{
			name:        "another default hash drops every list",
			prevCfg:     audience.Config{Cache: cache},
			newCfg:      audience.Config{Cache: cache, Defaults: audience.ListSettings{Hash: audience.HashSHA256}},
			wantLookups: 2,
		},
		{
			name:        "another cache size drops everything",
			prevCfg:     audience.Config{Cache: cache},
			newCfg:      audience.Config{Cache: audience.CacheConfig{Size: 5, TTL: time.Minute}},
			wantLookups: 2,
		},
		{
			name:        "cache disabled in the new config",
			prevCfg:     audience.Config{Cache: cache},
			newCfg:      audience.Config{},
			wantLookups: 2,
		},
		{name: "no previous resolver", prevCfg: audience.Config{Cache: cache}, newCfg: audience.Config{Cache: cache}, nilPrev: true, wantLookups: 2},
		{
			name: "expired entries are not carried over", prevCfg: audience.Config{Cache: cache},
			newCfg: audience.Config{Cache: cache}, expirePrev: true, wantLookups: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			require.NoError(t, f.Add(ctx, "beta", audience.ListSettings{}, "bob"))
			prev := audience.NewResolver(f, tt.prevCfg, nil, nil)
			prev.Resolve(ctx, terms, ctxFor("bob"), nil)
			require.Equal(t, 2, f.LookupCount())
			if tt.expirePrev {
				audience.SetClock(prev, func() time.Time { return time.Now().Add(time.Hour) })
			}
			var from *audience.Resolver
			if tt.nilPrev {
				from = audience.NewResolverFrom(nil, f, tt.newCfg, nil, nil)
			} else {
				from = audience.NewResolverFrom(prev, f, tt.newCfg, nil, nil)
			}
			// the previous resolver is closed by the caller after the swap: no impact on the new one
			prev.Close()
			got := from.Resolve(ctx, terms, ctxFor("bob"), nil)
			assert.Equal(t, 2+tt.wantLookups, f.LookupCount())
			if tt.newCfg.Defaults.Hash == "" { // a hash makes the stored (clear) id unreachable
				assert.Equal(t, flag.AudienceResult{Member: true}, got[look("beta", "bob")])
			}
			assert.Equal(t, flag.AudienceResult{}, got[look("vip", "bob")])
		})
	}
}
