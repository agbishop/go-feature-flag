//go:build docker

package redisprovider

import (
	"context"
	"testing"

	redis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	testcontainerRedis "github.com/testcontainers/testcontainers-go/modules/redis"
)

func TestScanAndVersionAgainstServer(t *testing.T) {
	ctx := context.Background()
	c, err := testcontainerRedis.Run(ctx, "docker.io/redis:7")
	testcontainers.CleanupContainer(t, c)
	require.NoError(t, err)
	addr, err := c.Endpoint(ctx, "")
	require.NoError(t, err)
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer func() { _ = client.Close() }()
	require.NoError(t, client.Set(ctx, "goff:vip:alice", "1", 0).Err())

	t.Run("serverVersion reads redis_version", func(t *testing.T) {
		v, ok := serverVersion(ctx, client)
		assert.True(t, ok)
		assert.True(t, supportsSMIsMember(v))
	})

	tests := []struct {
		name      string
		pattern   string
		wantFound bool
	}{
		{name: "a key matches", pattern: "goff:vip:*", wantFound: true},
		{name: "no key matches", pattern: "goff:unknown:*", wantFound: false},
	}
	for _, tt := range tests {
		t.Run("anyKeyMatches "+tt.name, func(t *testing.T) {
			found, complete, err := anyKeyMatches(ctx, client, tt.pattern)
			require.NoError(t, err)
			assert.Equal(t, tt.wantFound, found)
			assert.Equal(t, !tt.wantFound, complete)
		})
	}
}
