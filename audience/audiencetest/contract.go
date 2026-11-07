package audiencetest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thomaspoignant/go-feature-flag/audience"
	"github.com/thomaspoignant/go-feature-flag/retriever"
	"github.com/thomaspoignant/go-feature-flag/utils/fflog"
)

const (
	memberAlice = "alice"
	listVIP     = "vip"
)

// RunProviderContract runs the contract every audience.Provider must satisfy.
// newProvider returns a fresh, not initialized provider with an empty store, plus the Seeder
// that populates the same store. The suite calls Init/Shutdown itself, and registers a cleanup
// that shuts the provider down.
//
//nolint:funlen // a contract suite is a flat list of subtests
func RunProviderContract(t *testing.T, newProvider func(t *testing.T) (audience.Provider, Seeder)) {
	t.Helper()
	ctx := context.Background()

	setup := func(t *testing.T) (audience.Provider, Seeder) {
		t.Helper()
		p, s := newProvider(t)
		require.NoError(t, p.Init(ctx, &fflog.FFLogger{}))
		t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
		return p, s
	}

	t.Run("lifecycle", func(t *testing.T) {
		p, _ := newProvider(t)
		require.NoError(t, p.Init(ctx, &fflog.FFLogger{}))
		assert.Equal(t, retriever.RetrieverReady, p.Status())
		require.NoError(t, p.Shutdown(ctx))
		assert.NotEqual(t, retriever.RetrieverReady, p.Status())
	})

	for _, layout := range layoutsOf(t, newProvider) {
		settings := audience.ListSettings{Layout: layout}
		t.Run("layout_"+layout, func(t *testing.T) {
			t.Run("member and non member", func(t *testing.T) {
				p, s := setup(t)
				require.NoError(t, s.Add(ctx, "beta", settings, memberAlice, "bob"))
				res, err := p.Contains(ctx, []audience.Lookup{
					{List: "beta", Member: memberAlice, Settings: settings},
					{List: "beta", Member: "carol", Settings: settings},
				})
				require.NoError(t, err)
				require.Len(t, res, 2)
				assert.NoError(t, res[0].Err)
				assert.True(t, res[0].Member)
				assert.NoError(t, res[1].Err)
				assert.False(t, res[1].Member)
			})

			t.Run("unknown list matches nobody without error", func(t *testing.T) {
				p, _ := setup(t)
				res, err := p.Contains(ctx, []audience.Lookup{{List: "ghost", Member: memberAlice, Settings: settings}})
				require.NoError(t, err)
				require.Len(t, res, 1)
				assert.NoError(t, res[0].Err)
				assert.False(t, res[0].Member)
			})

			t.Run("order and length follow the lookups, across lists", func(t *testing.T) {
				p, s := setup(t)
				require.NoError(t, s.Add(ctx, "a", settings, "1", "3"))
				require.NoError(t, s.Add(ctx, "b", settings, "2"))
				lookups := []audience.Lookup{
					{List: "a", Member: "1", Settings: settings},
					{List: "b", Member: "1", Settings: settings},
					{List: "b", Member: "2", Settings: settings},
					{List: "a", Member: "2", Settings: settings},
					{List: "a", Member: "3", Settings: settings},
					{List: "a", Member: "1", Settings: settings}, // duplicate lookup
				}
				want := []bool{true, false, true, false, true, true}
				res, err := p.Contains(ctx, lookups)
				require.NoError(t, err)
				require.Len(t, res, len(lookups))
				for i := range want {
					assert.NoError(t, res[i].Err, "lookup %d", i)
					assert.Equal(t, want[i], res[i].Member, "lookup %d", i)
				}
			})

			t.Run("empty batch", func(t *testing.T) {
				p, _ := setup(t)
				res, err := p.Contains(ctx, nil)
				require.NoError(t, err)
				assert.Empty(t, res)
			})

			t.Run("large batch", func(t *testing.T) {
				p, s := setup(t)
				const n = 500
				var members []string
				var lookups []audience.Lookup
				for i := 0; i < n; i++ {
					id := fmt.Sprintf("user-%d", i)
					if i%2 == 0 {
						members = append(members, id)
					}
					lookups = append(lookups, audience.Lookup{List: "big", Member: id, Settings: settings})
				}
				require.NoError(t, s.Add(ctx, "big", settings, members...))
				res, err := p.Contains(ctx, lookups)
				require.NoError(t, err)
				require.Len(t, res, n)
				for i := range res {
					require.NoError(t, res[i].Err)
					assert.Equal(t, i%2 == 0, res[i].Member, "lookup %d", i)
				}
			})

			t.Run("hashed values", func(t *testing.T) {
				p, s := setup(t)
				hs := audience.ListSettings{
					Layout: layout, Normalize: audience.NormalizeTrimLower,
					Hash: audience.HashSHA256, Encoding: audience.EncodingHex,
				}
				in := audience.EncodeMember(" Bob@X.com ", hs)
				out := audience.EncodeMember("eve@x.com", hs)
				require.NoError(t, s.Add(ctx, "emails", hs, in))
				res, err := p.Contains(ctx, []audience.Lookup{
					{List: "emails", Member: audience.EncodeMember("bob@x.com", hs), Settings: hs},
					{List: "emails", Member: out, Settings: hs},
				})
				require.NoError(t, err)
				require.Len(t, res, 2)
				assert.True(t, res[0].Member)
				assert.False(t, res[1].Member)
			})

			t.Run("raw digests", func(t *testing.T) {
				p, s := setup(t)
				rs := audience.ListSettings{Layout: layout, Hash: audience.HashSHA256, Encoding: audience.EncodingRaw}
				require.NoError(t, s.Add(ctx, "raw", rs, audience.EncodeMember("bob", rs)))
				res, err := p.Contains(ctx, []audience.Lookup{
					{List: "raw", Member: audience.EncodeMember("bob", rs), Settings: rs},
					{List: "raw", Member: audience.EncodeMember("eve", rs), Settings: rs},
				})
				require.NoError(t, err)
				require.Len(t, res, 2)
				assert.True(t, res[0].Member)
				assert.False(t, res[1].Member)
			})

			t.Run("changes are visible: remove and delete", func(t *testing.T) {
				p, s := setup(t)
				require.NoError(t, s.Add(ctx, "tmp", settings, "x", "y"))
				require.NoError(t, s.Remove(ctx, "tmp", settings, "x"))
				l := func(m string) []audience.Lookup {
					return []audience.Lookup{{List: "tmp", Member: m, Settings: settings}}
				}
				res, err := p.Contains(ctx, l("x"))
				require.NoError(t, err)
				assert.False(t, res[0].Member)
				res, err = p.Contains(ctx, l("y"))
				require.NoError(t, err)
				assert.True(t, res[0].Member)
				require.NoError(t, s.DeleteList(ctx, "tmp", settings))
				res, err = p.Contains(ctx, l("y"))
				require.NoError(t, err)
				assert.False(t, res[0].Member)
			})

			t.Run("exists", func(t *testing.T) {
				p, s := setup(t)
				require.NoError(t, s.Add(ctx, "present", settings, "x"))
				got, err := p.Exists(ctx, map[string]audience.ListSettings{"present": settings, "absent": settings})
				require.NoError(t, err)
				assert.Equal(t, map[string]bool{"present": true, "absent": false}, got)
			})

			t.Run("custom prefix", func(t *testing.T) {
				p, s := setup(t)
				ps := audience.ListSettings{Layout: layout, Prefix: "seg:"}
				require.NoError(t, s.Add(ctx, listVIP, ps, memberAlice))
				res, err := p.Contains(ctx, []audience.Lookup{
					{List: listVIP, Member: memberAlice, Settings: ps},
					{List: listVIP, Member: "bob", Settings: ps},
				})
				require.NoError(t, err)
				require.Len(t, res, 2)
				assert.True(t, res[0].Member)
				assert.False(t, res[1].Member)
				got, err := p.Exists(ctx, map[string]audience.ListSettings{listVIP: ps, "nope": ps})
				require.NoError(t, err)
				assert.Equal(t, map[string]bool{listVIP: true, "nope": false}, got)
			})
		})
	}

	t.Run("cancelled context returns an error", func(t *testing.T) {
		p, _ := setup(t)
		cctx, cancel := context.WithTimeout(ctx, time.Nanosecond)
		defer cancel()
		<-cctx.Done()
		res, err := p.Contains(cctx, []audience.Lookup{{List: "a", Member: "x"}})
		if err == nil {
			// per-result errors are an acceptable way to report it.
			require.Len(t, res, 1)
			assert.Error(t, res[0].Err)
		}
	})
}

// layoutsOf returns the layouts supported by the provider under test.
func layoutsOf(t *testing.T, newProvider func(t *testing.T) (audience.Provider, Seeder)) []string {
	t.Helper()
	_, s := newProvider(t)
	layouts := s.SupportedLayouts()
	require.NotEmpty(t, layouts, "a provider must support at least one layout")
	return layouts
}
