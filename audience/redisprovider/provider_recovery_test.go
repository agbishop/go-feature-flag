//go:build docker

package redisprovider

import (
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	redis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	testcontainerRedis "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/thomaspoignant/go-feature-flag/audience"
	"github.com/thomaspoignant/go-feature-flag/retriever"
)

// toggleProxy forwards to target while enabled and refuses connections otherwise. It keeps a
// stable address across the "server down" and "server up" phases of a test (a restarted
// container gets another mapped port).
type toggleProxy struct {
	ln      net.Listener
	target  string
	enabled atomic.Bool
	mu      sync.Mutex
	conns   []net.Conn
}

func newToggleProxy(t *testing.T, target string) *toggleProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	p := &toggleProxy{ln: ln, target: target}
	t.Cleanup(func() { _ = ln.Close(); p.dropAll() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if !p.enabled.Load() {
				_ = c.Close()
				continue
			}
			up, err := net.Dial("tcp", p.target)
			if err != nil {
				_ = c.Close()
				continue
			}
			p.mu.Lock()
			p.conns = append(p.conns, c, up)
			p.mu.Unlock()
			go func() { _, _ = io.Copy(up, c); _ = up.Close() }()
			go func() { _, _ = io.Copy(c, up); _ = c.Close() }()
		}
	}()
	return p
}

func (p *toggleProxy) addr() string { return p.ln.Addr().String() }

func (p *toggleProxy) dropAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.conns {
		_ = c.Close()
	}
	p.conns = nil
}

func TestRecoversWhenTheServerComesBack(t *testing.T) {
	ctx := context.Background()
	c, err := testcontainerRedis.Run(ctx, "docker.io/redis:7")
	testcontainers.CleanupContainer(t, c)
	require.NoError(t, err)
	target, err := c.Endpoint(ctx, "")
	require.NoError(t, err)
	seed := redis.NewClient(&redis.Options{Addr: target})
	defer func() { _ = seed.Close() }()
	require.NoError(t, seed.SAdd(ctx, "{beta}", "bob").Err())

	lookup := []audience.Lookup{{List: "beta", Member: "bob"}}
	opts := func(addr string) *redis.UniversalOptions {
		return &redis.UniversalOptions{Addrs: []string{addr}, MaxRetries: -1, DialTimeout: 200 * time.Millisecond}
	}

	t.Run("lookups succeed once the server is up, driven by Contains", func(t *testing.T) {
		proxy := newToggleProxy(t, target) // disabled: the server is "stopped"
		p := &Provider{Options: opts(proxy.addr()), repingDelay: time.Hour}
		require.Error(t, p.Init(ctx, nil))
		assert.Equal(t, retriever.RetrieverError, p.Status())
		defer func() { _ = p.Shutdown(ctx) }()

		res, err := p.Contains(ctx, lookup)
		require.NoError(t, err)
		assert.Error(t, res[0].Err)

		proxy.enabled.Store(true) // the server starts
		res, err = p.Contains(ctx, lookup)
		require.NoError(t, err)
		require.NoError(t, res[0].Err)
		assert.True(t, res[0].Member)
		assert.Equal(t, retriever.RetrieverReady, p.Status())
	})

	t.Run("the background ping sets the status back to READY", func(t *testing.T) {
		proxy := newToggleProxy(t, target)
		p := &Provider{Options: opts(proxy.addr()), repingDelay: 50 * time.Millisecond}
		require.Error(t, p.Init(ctx, nil))
		defer func() { _ = p.Shutdown(ctx) }()
		proxy.enabled.Store(true)
		assert.Eventually(t, func() bool { return p.Status() == retriever.RetrieverReady },
			10*time.Second, 50*time.Millisecond)
	})

	t.Run("a dropped connection flips to ERROR then READY again", func(t *testing.T) {
		proxy := newToggleProxy(t, target)
		proxy.enabled.Store(true)
		p := &Provider{Options: opts(proxy.addr())}
		require.NoError(t, p.Init(ctx, nil))
		defer func() { _ = p.Shutdown(ctx) }()
		proxy.enabled.Store(false)
		proxy.dropAll()
		res, _ := p.Contains(ctx, lookup)
		assert.Error(t, res[0].Err)
		assert.Equal(t, retriever.RetrieverError, p.Status())
		proxy.enabled.Store(true)
		res, _ = p.Contains(ctx, lookup)
		assert.NoError(t, res[0].Err)
		assert.Equal(t, retriever.RetrieverReady, p.Status())
	})

	t.Run("a wrong type key only fails its own lookups", func(t *testing.T) {
		require.NoError(t, seed.Set(ctx, "{notaset}", "x", 0).Err())
		require.NoError(t, seed.Set(ctx, "k:beta:bob", "1", 0).Err())
		p := &Provider{Options: opts(target)}
		require.NoError(t, p.Init(ctx, nil))
		defer func() { _ = p.Shutdown(ctx) }()
		keys := audience.ListSettings{Layout: audience.LayoutKeys, Prefix: "k:"}
		res, err := p.Contains(ctx, []audience.Lookup{
			{List: "notaset", Member: "a"},
			{List: "beta", Member: "bob", Settings: keys},
			{List: "beta", Member: "bob"},
		})
		require.NoError(t, err)
		assert.Error(t, res[0].Err)
		assert.Equal(t, audience.Result{Member: true}, res[1])
		assert.Equal(t, audience.Result{Member: true}, res[2])
	})

	t.Run("Exists on the keys layout reports present and absent lists", func(t *testing.T) {
		p := &Provider{Options: opts(target)}
		require.NoError(t, p.Init(ctx, nil))
		defer func() { _ = p.Shutdown(ctx) }()
		keys := audience.ListSettings{Layout: audience.LayoutKeys, Prefix: "k:"}
		got, err := p.Exists(ctx, map[string]audience.ListSettings{"beta": keys, "nope": keys})
		require.NoError(t, err)
		assert.Equal(t, map[string]bool{"beta": true, "nope": false}, got)
	})
}
