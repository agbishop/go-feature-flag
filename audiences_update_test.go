package ffclient_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ffclient "github.com/thomaspoignant/go-feature-flag"
	"github.com/thomaspoignant/go-feature-flag/audience"
	"github.com/thomaspoignant/go-feature-flag/audience/audiencetest"
	"github.com/thomaspoignant/go-feature-flag/ffcontext"
	"github.com/thomaspoignant/go-feature-flag/modules/core/flag"
	"github.com/thomaspoignant/go-feature-flag/retriever"
)

func TestUpdateAudiences(t *testing.T) {
	tests := []struct {
		name string
		// newProvider tells if the update gives another provider (otherwise nil: reuse).
		newProvider bool
		newCfg      audience.Config
		invalid     bool
		wantErr     bool
		user        string
		wantValue   any
		wantOldDown bool
	}{
		{
			name: "same provider, new config keeps membership", newCfg: audience.Config{FailMode: audience.FailModeEmpty},
			user: "bob", wantValue: true,
		},
		{
			name: "same provider, definition changes the stored form", newCfg: audience.Config{
				Definitions: map[string]audience.ListSettings{"beta": {Prefix: "other:"}},
			},
			user: "bob", wantValue: true,
		},
		{name: "new provider with the list", newProvider: true, user: "bob", wantValue: true, wantOldDown: true},
		{
			name: "invalid config is rejected and keeps the previous one", invalid: true, wantErr: true,
			user: "bob", wantValue: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			oldFake := audiencetest.NewFakeProvider()
			require.NoError(t, oldFake.Add(ctx, "beta", audience.ListSettings{}, "bob"))
			g := newAudienceClient(t, &ffclient.AudiencesConfig{Provider: oldFake}, nil)

			upd := &ffclient.AudiencesConfig{Config: tt.newCfg}
			if tt.invalid {
				upd.Config.Cache = audience.CacheConfig{Size: 10} // ttl required
			}
			var newFake *audiencetest.FakeProvider
			if tt.newProvider {
				newFake = audiencetest.NewFakeProvider()
				require.NoError(t, newFake.Add(ctx, "beta", audience.ListSettings{}, "bob"))
				upd.Provider = newFake
			}
			err := g.UpdateAudiences(upd)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Eventually(t, func() bool {
				res, err := g.RawVariation("beta-flag", ffcontext.NewEvaluationContext(tt.user), false)
				return err == nil && res.Value == tt.wantValue &&
					res.Metadata[flag.AudienceStatusMetadataKey] == flag.AudienceStatusHealthy
			}, 2*time.Second, 10*time.Millisecond)
			if tt.wantOldDown {
				assert.Eventually(t, func() bool { return oldFake.Status() != retriever.RetrieverReady },
					2*time.Second, 10*time.Millisecond)
			} else {
				assert.Equal(t, retriever.RetrieverReady, oldFake.Status())
			}
		})
	}
}

func TestUpdateAudiences_NotConfigured(t *testing.T) {
	g := newAudienceClient(t, nil, nil)
	err := g.UpdateAudiences(&ffclient.AudiencesConfig{Provider: audiencetest.NewFakeProvider()})
	assert.ErrorIs(t, err, ffclient.ErrAudiencesNotConfigured)
	var nilClient *ffclient.GoFeatureFlag
	assert.ErrorIs(t, nilClient.UpdateAudiences(&ffclient.AudiencesConfig{}), ffclient.ErrAudiencesNotConfigured)
}

func TestUpdateAudiences_NilConfig(t *testing.T) {
	g := newAudienceClient(t, &ffclient.AudiencesConfig{Provider: audiencetest.NewFakeProvider()}, nil)
	assert.Error(t, g.UpdateAudiences(nil))
}

func TestUpdateAudiences_CacheCarriedOver(t *testing.T) {
	baseCache := audience.CacheConfig{Size: 10, TTL: time.Hour}
	tests := []struct {
		name string
		// update is the new audiences configuration (nil Provider: the provider is reused).
		update func(newProvider *audiencetest.FakeProvider) *ffclient.AudiencesConfig
		// wantLookups is the number of store lookups of the evaluation done after the update,
		// on the provider that is in use then (beta and vip are the two cached lists).
		wantLookups int
		// wantOnNewProvider tells that the lookups go to the new provider.
		wantOnNewProvider bool
	}{
		{
			name: "unchanged lists keep their cache hits",
			update: func(*audiencetest.FakeProvider) *ffclient.AudiencesConfig {
				return &ffclient.AudiencesConfig{Config: audience.Config{Cache: baseCache}}
			},
			wantLookups: 0,
		},
		{
			name: "a ttl change alone keeps the entries",
			update: func(*audiencetest.FakeProvider) *ffclient.AudiencesConfig {
				return &ffclient.AudiencesConfig{Config: audience.Config{
					Cache: audience.CacheConfig{Size: 10, TTL: 2 * time.Hour},
				}}
			},
			wantLookups: 0,
		},
		{
			name: "a fail mode change alone keeps the entries",
			update: func(*audiencetest.FakeProvider) *ffclient.AudiencesConfig {
				return &ffclient.AudiencesConfig{Config: audience.Config{
					Cache: baseCache, FailMode: audience.FailModeEmpty,
				}}
			},
			wantLookups: 0,
		},
		{
			name: "a list with a changed definition is fetched again, the other one is not",
			update: func(*audiencetest.FakeProvider) *ffclient.AudiencesConfig {
				return &ffclient.AudiencesConfig{Config: audience.Config{
					Cache:       baseCache,
					Definitions: map[string]audience.ListSettings{"beta": {Normalize: audience.NormalizeTrimLower}},
				}}
			},
			wantLookups: 1,
		},
		{
			name: "a changed default setting fetches every list again",
			update: func(*audiencetest.FakeProvider) *ffclient.AudiencesConfig {
				return &ffclient.AudiencesConfig{Config: audience.Config{
					Cache:    baseCache,
					Defaults: audience.ListSettings{Prefix: "other:"},
				}}
			},
			wantLookups: 2,
		},
		{
			name: "a changed cache size drops everything",
			update: func(*audiencetest.FakeProvider) *ffclient.AudiencesConfig {
				return &ffclient.AudiencesConfig{Config: audience.Config{
					Cache: audience.CacheConfig{Size: 20, TTL: time.Hour},
				}}
			},
			wantLookups: 2,
		},
		{
			name: "a replaced provider drops everything",
			update: func(p *audiencetest.FakeProvider) *ffclient.AudiencesConfig {
				return &ffclient.AudiencesConfig{Provider: p, Config: audience.Config{Cache: baseCache}}
			},
			wantLookups:       2,
			wantOnNewProvider: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			fake := audiencetest.NewFakeProvider()
			require.NoError(t, fake.Add(ctx, "beta", audience.ListSettings{}, "bob"))
			newFake := audiencetest.NewFakeProvider()
			g := newAudienceClient(t, &ffclient.AudiencesConfig{
				Provider: fake, Config: audience.Config{Cache: baseCache},
			}, nil)
			require.Eventually(t, func() bool { return fake.Status() == retriever.RetrieverReady },
				time.Second, 5*time.Millisecond)
			evalCtx := ffcontext.NewEvaluationContext("bob")
			warm := g.AllFlagsStateWithContext(ctx, evalCtx)
			require.Equal(t, true, warm.GetFlags()["beta-flag"].Value)
			require.Equal(t, 2, fake.LookupCount(), "beta and vip are now cached")

			require.NoError(t, g.UpdateAudiences(tt.update(newFake)))
			require.Eventually(t, func() bool { return newFake.Status() == retriever.RetrieverReady || !tt.wantOnNewProvider },
				time.Second, 5*time.Millisecond)

			before := fake.LookupCount()
			after := g.AllFlagsStateWithContext(ctx, evalCtx)
			target := fake
			if tt.wantOnNewProvider {
				target, before = newFake, 0
			}
			assert.Equal(t, tt.wantLookups, target.LookupCount()-before)
			// the membership answers stay the same when the data did not change (beta is a member
			// in the original store only).
			if !tt.wantOnNewProvider {
				assert.Equal(t, true, after.GetFlags()["beta-flag"].Value)
			}
		})
	}
}

func TestUpdateAudiences_ConcurrentEvaluations(t *testing.T) {
	ctx := context.Background()
	oldFake := audiencetest.NewFakeProvider()
	require.NoError(t, oldFake.Add(ctx, "beta", audience.ListSettings{}, "bob"))
	g := newAudienceClient(t, &ffclient.AudiencesConfig{Provider: oldFake}, nil)

	var stop atomic.Bool
	var evals, wrong atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				res, err := g.RawVariationWithContext(ctx, "beta-flag", ffcontext.NewEvaluationContext("bob"), false)
				evals.Add(1)
				// while a new provider initializes the term is unknown (false): only an error is a bug.
				if err != nil || res.Reason == flag.ReasonError {
					wrong.Add(1)
				}
				_ = g.AllFlagsStateWithContext(ctx, ffcontext.NewEvaluationContext("bob"))
			}
		}()
	}
	for i := 0; i < 20; i++ {
		upd := &ffclient.AudiencesConfig{Config: audience.Config{
			Cache: audience.CacheConfig{Size: 10 + i, TTL: time.Second},
		}}
		if i%3 == 0 {
			f := audiencetest.NewFakeProvider()
			require.NoError(t, f.Add(ctx, "beta", audience.ListSettings{}, "bob"))
			upd.Provider = f
		}
		require.NoError(t, g.UpdateAudiences(upd))
		time.Sleep(5 * time.Millisecond)
	}
	stop.Store(true)
	wg.Wait()
	assert.Zero(t, wrong.Load())
	assert.NotZero(t, evals.Load())
	assert.Eventually(t, func() bool {
		res, err := g.RawVariation("beta-flag", ffcontext.NewEvaluationContext("bob"), false)
		return err == nil && res.Value == true
	}, 2*time.Second, 10*time.Millisecond)
}

func TestUpdateAudiences_AfterClose(t *testing.T) {
	g := newAudienceClient(t, &ffclient.AudiencesConfig{Provider: audiencetest.NewFakeProvider()}, nil)
	g.Close()
	assert.Error(t, g.UpdateAudiences(&ffclient.AudiencesConfig{}))
}
