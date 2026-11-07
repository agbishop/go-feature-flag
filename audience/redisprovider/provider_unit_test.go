package redisprovider

import (
	"context"
	"testing"

	redis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thomaspoignant/go-feature-flag/audience"
	"github.com/thomaspoignant/go-feature-flag/retriever"
)

func TestKeys(t *testing.T) {
	assert.Equal(t, "goff:{beta}", setKey("goff:", "beta"))
	assert.Equal(t, "{beta}", setKey("", "beta"))
	assert.Equal(t, "goff:beta:alice", memberKey("goff:", "beta", "alice"))
	assert.Equal(t, "beta:alice", memberKey("", "beta", "alice"))
}

func TestEscapeGlob(t *testing.T) {
	tests := []struct{ in, want string }{
		{"beta", "beta"},
		{"a*b", `a\*b`},
		{"a[1]?", `a\[1\]\?`},
		{`a\b`, `a\\b`},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) { assert.Equal(t, tt.want, escapeGlob(tt.in)) })
	}
}

func TestSupportsSMIsMember(t *testing.T) {
	tests := []struct {
		v    string
		want bool
	}{
		{"7.2.4", true}, {"6.2.0", true}, {"6.0.9", false}, {"5.0.14", false}, {"weird", true}, {"x.y.z", true},
	}
	for _, tt := range tests {
		t.Run(tt.v, func(t *testing.T) { assert.Equal(t, tt.want, supportsSMIsMember(tt.v)) })
	}
}

func TestFailAll(t *testing.T) {
	res := failAll(make([]audience.Result, 3), assert.AnError)
	require.Len(t, res, 3)
	for _, r := range res {
		assert.ErrorIs(t, r.Err, assert.AnError)
		assert.False(t, r.Member)
	}
}

func TestLifecycleWithoutServer(t *testing.T) {
	ctx := context.Background()

	t.Run("nil options", func(t *testing.T) {
		p := &Provider{}
		assert.Equal(t, retriever.RetrieverNotReady, p.Status())
		assert.Error(t, p.Init(ctx, nil))
		assert.Equal(t, retriever.RetrieverError, p.Status())
	})
	t.Run("unreachable server", func(t *testing.T) {
		p := &Provider{Options: &redis.UniversalOptions{Addrs: []string{"127.0.0.1:1"}, MaxRetries: -1}}
		assert.Error(t, p.Init(ctx, nil))
		assert.Equal(t, retriever.RetrieverError, p.Status())
		assert.NoError(t, p.Shutdown(ctx))
		assert.Equal(t, retriever.RetrieverNotReady, p.Status())
	})
	t.Run("not initialized", func(t *testing.T) {
		p := &Provider{}
		lookups := []audience.Lookup{{List: "a", Member: "x"}, {List: "a", Member: "y"}}
		res, err := p.Contains(ctx, lookups)
		assert.Error(t, err)
		require.Len(t, res, 2)
		assert.Error(t, res[0].Err)
		_, err = p.Exists(ctx, map[string]audience.ListSettings{"a": {}})
		assert.Error(t, err)
		var nilP *Provider
		assert.Equal(t, retriever.RetrieverNotReady, nilP.Status())
		assert.NoError(t, nilP.Shutdown(ctx))
	})
	t.Run("empty batch", func(t *testing.T) {
		res, err := (&Provider{}).Contains(ctx, nil)
		assert.NoError(t, err)
		assert.Empty(t, res)
	})
}

func TestServerVersionAndScanErrors(t *testing.T) {
	ctx := context.Background()
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
	defer func() { _ = client.Close() }()

	t.Run("serverVersion on unreachable server", func(t *testing.T) {
		v, ok := serverVersion(ctx, client)
		assert.False(t, ok)
		assert.Empty(t, v)
	})
	t.Run("anyKeyMatches on unreachable server", func(t *testing.T) {
		found, complete, err := anyKeyMatches(ctx, client, "*")
		assert.Error(t, err)
		assert.False(t, found)
		assert.False(t, complete)
	})
}
