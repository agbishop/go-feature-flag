package redisprovider

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	redis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thomaspoignant/go-feature-flag/audience"
	"github.com/thomaspoignant/go-feature-flag/retriever"
)

func TestInitFailureKeepsTheClient(t *testing.T) {
	ctx := context.Background()
	p := &Provider{
		Options:     &redis.UniversalOptions{Addrs: []string{"127.0.0.1:1"}, MaxRetries: -1, DialTimeout: 100 * time.Millisecond},
		repingDelay: time.Hour,
	}
	before := runtime.NumGoroutine()
	require.Error(t, p.Init(ctx, nil))
	assert.Equal(t, retriever.RetrieverError, p.Status())
	client, err := p.getClient()
	assert.NoError(t, err, "the client must be kept so that the provider can recover")
	assert.NotNil(t, client)

	// Contains tries the server even in ERROR status: the failure is per lookup, not "not initialized".
	res, err := p.Contains(ctx, []audience.Lookup{{List: "a", Member: "x"}})
	require.NoError(t, err)
	require.Len(t, res, 1)
	require.Error(t, res[0].Err)
	assert.NotContains(t, res[0].Err.Error(), "not initialized")

	require.NoError(t, p.Shutdown(ctx))
	assert.Equal(t, retriever.RetrieverNotReady, p.Status())
	_, err = p.getClient()
	assert.Error(t, err)
	assert.Eventually(t, func() bool { return runtime.NumGoroutine() <= before+2 }, 2*time.Second, 20*time.Millisecond,
		"the re-ping goroutine must stop on Shutdown")
}

func TestBoundedScan(t *testing.T) {
	tests := []struct {
		name         string
		pages        []scanPage
		maxCalls     int
		wantFound    bool
		wantComplete bool
		wantErr      bool
		wantCalls    int
	}{
		{"found on the first page", []scanPage{{keys: []string{"k"}, next: 7}}, 10, true, false, false, 1},
		{"found later", []scanPage{{next: 1}, {next: 2}, {keys: []string{"k"}, next: 3}}, 10, true, false, false, 3},
		{"whole keyspace walked", []scanPage{{next: 1}, {next: 0}}, 10, false, true, false, 2},
		{"bound reached: unknown", []scanPage{{next: 1}, {next: 2}, {next: 3}}, 2, false, false, false, 2},
		{"error", []scanPage{{err: errors.New("boom")}}, 10, false, false, true, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			found, complete, err := boundedScan(context.Background(),
				func(_ context.Context, _ uint64) ([]string, uint64, error) {
					pg := tt.pages[min(calls, len(tt.pages)-1)]
					calls++
					return pg.keys, pg.next, pg.err
				}, tt.maxCalls)
			assert.Equal(t, tt.wantFound, found)
			assert.Equal(t, tt.wantComplete, complete)
			assert.Equal(t, tt.wantErr, err != nil)
			assert.Equal(t, tt.wantCalls, calls)
		})
	}
}

type scanPage struct {
	keys []string
	next uint64
	err  error
}

func TestFillResultsIsPerCommand(t *testing.T) {
	okExists := redis.NewIntCmd(context.Background())
	okExists.SetVal(1)
	failExists := redis.NewIntCmd(context.Background())
	failExists.SetErr(errors.New("node down"))
	okSet := redis.NewBoolSliceCmd(context.Background())
	okSet.SetVal([]bool{true, false})
	failSet := redis.NewBoolSliceCmd(context.Background())
	failSet.SetErr(errors.New("node down"))

	results := make([]audience.Result, 6)
	fillResults(results, []pending{
		{idx: []int{0}, ex: okExists},
		{idx: []int{1}, ex: failExists},
		{idx: []int{2, 3}, smis: okSet},
		{idx: []int{4, 5}, smis: failSet},
	})
	assert.Equal(t, audience.Result{Member: true}, results[0])
	assert.Error(t, results[1].Err)
	assert.Equal(t, audience.Result{Member: true}, results[2])
	assert.Equal(t, audience.Result{Member: false}, results[3])
	assert.Error(t, results[4].Err)
	assert.Error(t, results[5].Err)
}

func TestStoreKey(t *testing.T) {
	hashed := audience.ListSettings{Prefix: "goff:", Layout: audience.LayoutSet, Hash: audience.HashSHA256}
	tests := []struct {
		name       string
		list, id   string
		settings   audience.ListSettings
		wantKey    string
		wantMember string
	}{
		{"set", "beta", "bob", audience.ListSettings{Prefix: "goff:"}, "goff:{beta}", "bob"},
		{"keys", "beta", "bob", audience.ListSettings{Prefix: "goff:", Layout: audience.LayoutKeys}, "goff:beta:bob", "bob"},
		{"trimLower", "beta", " Bob ", audience.ListSettings{Normalize: audience.NormalizeTrimLower}, "{beta}", "bob"},
		{"hashed set", "beta", "bob", hashed, "goff:{beta}", audience.EncodeMember("bob", hashed)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k, m := StoreKey(tt.list, tt.id, tt.settings)
			assert.Equal(t, tt.wantKey, k)
			assert.Equal(t, tt.wantMember, m)
		})
	}
}
