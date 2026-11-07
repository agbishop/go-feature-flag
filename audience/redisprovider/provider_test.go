//go:build docker

package redisprovider_test

import (
	"context"
	"testing"
	"time"

	redis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	testcontainerRedis "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/thomaspoignant/go-feature-flag/audience"
	"github.com/thomaspoignant/go-feature-flag/audience/audiencetest"
	"github.com/thomaspoignant/go-feature-flag/audience/redisprovider"
)

// seeder writes lists straight into Redis, following the documented data contract.
type seeder struct{ client *redis.Client }

func setKey(s audience.ListSettings, list string) string { return s.Prefix + "{" + list + "}" }

func (s *seeder) SupportedLayouts() []string {
	return []string{audience.LayoutSet, audience.LayoutKeys}
}

func (s *seeder) Add(ctx context.Context, list string, st audience.ListSettings, members ...string) error {
	if st.Layout == audience.LayoutKeys {
		for _, m := range members {
			if err := s.client.Set(ctx, st.Prefix+list+":"+m, "1", 0).Err(); err != nil {
				return err
			}
		}
		return nil
	}
	args := make([]any, len(members))
	for i, m := range members {
		args[i] = m
	}
	return s.client.SAdd(ctx, setKey(st, list), args...).Err()
}

func (s *seeder) Remove(ctx context.Context, list string, st audience.ListSettings, members ...string) error {
	if st.Layout == audience.LayoutKeys {
		for _, m := range members {
			if err := s.client.Unlink(ctx, st.Prefix+list+":"+m).Err(); err != nil {
				return err
			}
		}
		return nil
	}
	args := make([]any, len(members))
	for i, m := range members {
		args[i] = m
	}
	return s.client.SRem(ctx, setKey(st, list), args...).Err()
}

func (s *seeder) DeleteList(ctx context.Context, list string, st audience.ListSettings) error {
	if st.Layout == audience.LayoutKeys {
		keys, err := s.client.Keys(ctx, st.Prefix+list+":*").Result()
		if err != nil || len(keys) == 0 {
			return err
		}
		return s.client.Unlink(ctx, keys...).Err()
	}
	return s.client.Unlink(ctx, setKey(st, list)).Err()
}

func startRedis(t *testing.T) (*testcontainerRedis.RedisContainer, string) {
	t.Helper()
	ctx := context.Background()
	c, err := testcontainerRedis.Run(ctx, "docker.io/redis:7")
	testcontainers.CleanupContainer(t, c)
	require.NoError(t, err)
	addr, err := c.Endpoint(ctx, "")
	require.NoError(t, err)
	return c, addr
}

func TestProviderContract(t *testing.T) {
	_, addr := startRedis(t)
	audiencetest.RunProviderContract(t, func(t *testing.T) (audience.Provider, audiencetest.Seeder) {
		client := redis.NewClient(&redis.Options{Addr: addr})
		require.NoError(t, client.FlushAll(context.Background()).Err())
		t.Cleanup(func() { _ = client.Close() })
		return &redisprovider.Provider{Options: &redis.UniversalOptions{Addrs: []string{addr}}},
			&seeder{client: client}
	})
}

func TestPrefixAndErrors(t *testing.T) {
	ctx := context.Background()
	c, addr := startRedis(t)
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer func() { _ = client.Close() }()
	s := &seeder{client: client}

	p := &redisprovider.Provider{Options: &redis.UniversalOptions{Addrs: []string{addr}}}
	require.NoError(t, p.Init(ctx, nil))
	defer func() { _ = p.Shutdown(ctx) }()

	for _, layout := range []string{audience.LayoutSet, audience.LayoutKeys} {
		t.Run("prefix "+layout, func(t *testing.T) {
			st := audience.ListSettings{Layout: layout, Prefix: "goff:"}
			other := audience.ListSettings{Layout: layout, Prefix: "other:"}
			require.NoError(t, s.Add(ctx, "vip", st, "alice"))
			res, err := p.Contains(ctx, []audience.Lookup{
				{List: "vip", Member: "alice", Settings: st},
				{List: "vip", Member: "alice", Settings: other},
			})
			require.NoError(t, err)
			assert.True(t, res[0].Member)
			assert.False(t, res[1].Member)
		})
	}

	t.Run("mixed layouts in one batch", func(t *testing.T) {
		set := audience.ListSettings{Layout: audience.LayoutSet}
		keys := audience.ListSettings{Layout: audience.LayoutKeys}
		require.NoError(t, s.Add(ctx, "m1", set, "a"))
		require.NoError(t, s.Add(ctx, "m2", keys, "b"))
		res, err := p.Contains(ctx, []audience.Lookup{
			{List: "m2", Member: "b", Settings: keys},
			{List: "m1", Member: "b", Settings: set},
			{List: "m1", Member: "a", Settings: set},
		})
		require.NoError(t, err)
		assert.Equal(t, []bool{true, false, true}, []bool{res[0].Member, res[1].Member, res[2].Member})
	})

	t.Run("wrong type key gives per lookup error", func(t *testing.T) {
		require.NoError(t, client.Set(ctx, "{notaset}", "x", 0).Err())
		res, err := p.Contains(ctx, []audience.Lookup{
			{List: "notaset", Member: "a"},
			{List: "vip", Member: "alice", Settings: audience.ListSettings{Prefix: "goff:"}},
		})
		require.NoError(t, err)
		require.Len(t, res, 2)
		assert.Error(t, res[0].Err)
		assert.NoError(t, res[1].Err)
		assert.True(t, res[1].Member)
	})

	t.Run("server stopped gives errors", func(t *testing.T) {
		require.NoError(t, c.Stop(ctx, nil))
		lookups := []audience.Lookup{{List: "a", Member: "x"}, {List: "b", Member: "y"}}
		tctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		res, err := p.Contains(tctx, lookups)
		require.Len(t, res, len(lookups))
		if err == nil {
			for _, r := range res {
				assert.Error(t, r.Err)
			}
		}
		_, err = p.Exists(tctx, map[string]audience.ListSettings{"a": {}})
		assert.Error(t, err)
	})
}
