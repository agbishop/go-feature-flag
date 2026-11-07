// Package redisprovider is an audience.Provider backed by Redis, built on go-redis/v9.
// It also works with Valkey servers, which speak the same protocol and commands.
//
// # Data contract
//
// The provider is read-only. Two layouts are supported, selected per list through
// audience.ListSettings.Layout:
//
//	set   key "<prefix>{<name>}", a Redis set of ids        -> SMISMEMBER (one per list per batch)
//	keys  key "<prefix><name>:<id>", one key per id (any value) -> EXISTS (one per lookup)
//
// All the lookups of a Contains call go in a single pipeline (one round trip on a single
// node; on Cluster the client splits the pipeline per node and runs them in parallel).
// Ids are used as is: with encoding "raw" an id is a 32 byte binary string.
// A list that does not exist matches nobody and is not an error.
//
// Redis 6.2 or Valkey 7.2 is the minimum server version, because SMISMEMBER is used.
//
// # Updating lists
//
// Use SADD/SREM for sets and SET/UNLINK for keys. To rebuild a whole set atomically, build it
// at "<prefix>{<name>}:tmp" and RENAME it onto "<prefix>{<name>}" (both keys share the hash
// tag, so this works on Cluster). Remove large sets with UNLINK, never DEL, which blocks the
// server while it frees memory.
//
// # Access
//
// GO Feature Flag only reads, so a read-only user is enough:
//
//	ACL SETUSER goff on >secret ~goff:* +smismember +exists +scan +ping +info
//
// On Redis Cluster add the commands go-redis uses to discover the topology:
//
//	ACL SETUSER goff on >secret ~goff:* +smismember +exists +scan +ping +info +cluster|slots +cluster|shards
//
// (and +readonly when the client is configured with ReadOnly or RouteByLatency/RouteRandomly,
// which send READONLY on the replica connections).
//
// Drop +scan if you have no keys layout lists to check at load; +info is only used for the
// optional server version warning at Init (a refusal is ignored).
//
// # Cost of the existence check
//
// The load-time warning for lists that do not exist uses EXISTS for the set layout (O(1)) but
// a SCAN with a MATCH pattern for the keys layout, because there is no key to test directly.
// SCAN walks the whole keyspace in COUNT-sized steps, so on a big database a missing list is
// expensive to prove. The provider bounds the effort (maxScanCalls calls of COUNT scanCount per
// node): when the list is not found within the bound, the answer is "unknown" and no warning
// is emitted. Lookups (Contains) never scan.
//
// # Availability
//
// A failed connection at Init is not fatal: the status is ERROR, the client is kept (go-redis
// reconnects by itself), a background ping retries with a backoff, and Contains keeps trying
// the server. The status goes back to READY as soon as a command succeeds.
package redisprovider

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	redis "github.com/redis/go-redis/v9"
	"github.com/thomaspoignant/go-feature-flag/audience"
	"github.com/thomaspoignant/go-feature-flag/retriever"
	"github.com/thomaspoignant/go-feature-flag/utils/fflog"
)

const (
	// scanCount is the COUNT hint of each SCAN call of the existence check.
	scanCount = 1000
	// maxScanCalls bounds the SCAN calls (per node) of one existence check.
	maxScanCalls = 100

	repingMin = time.Second
	repingMax = 30 * time.Second
)

var _ audience.Provider = (*Provider)(nil)

// Provider reads audience lists from Redis (or Valkey).
type Provider struct {
	// Options to connect to Redis: a single node (one Addr), a Cluster (several Addrs)
	// or Sentinel (MasterName set).
	Options *redis.UniversalOptions

	mu     sync.RWMutex
	client redis.UniversalClient
	status retriever.Status

	// background re-ping after a failed Init.
	repingCancel context.CancelFunc
	repingWg     sync.WaitGroup
	// repingDelay is the first delay of the re-ping (repingMin when zero, tests shorten it).
	repingDelay time.Duration
}

// Init connects to the server and pings it. It never panics. On a failed ping the error is
// returned and the status is ERROR, but the client is kept: go-redis reconnects lazily, so the
// provider recovers when the server comes back (a background ping with a backoff, and every
// successful Contains, set the status back to READY).
func (p *Provider) Init(ctx context.Context, logger *fflog.FFLogger) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Options == nil {
		p.status = retriever.RetrieverError
		return errors.New("redisprovider: options are required")
	}
	_ = p.stopLocked()
	p.status = retriever.RetrieverNotReady
	client := redis.NewUniversalClient(p.Options)
	p.client = client
	if err := client.Ping(ctx).Err(); err != nil {
		p.status = retriever.RetrieverError
		p.startRepingLocked(client, logger)
		return fmt.Errorf("redisprovider: ping failed: %w", err)
	}
	p.status = retriever.RetrieverReady
	if logger != nil {
		if v, ok := serverVersion(ctx, client); ok && !supportsSMIsMember(v) {
			logger.Warn("redis server is older than 6.2, SMISMEMBER is not available", "version", v)
		}
	}
	return nil
}

// stopLocked stops the re-ping and closes the client. p.mu must be held; the re-ping goroutine
// never takes it before it observes the cancellation, so waiting for it here is safe.
func (p *Provider) stopLocked() error {
	if p.repingCancel != nil {
		p.repingCancel()
		p.repingCancel = nil
	}
	if p.client == nil {
		return nil
	}
	err := p.client.Close()
	p.client = nil
	if errors.Is(err, redis.ErrClosed) {
		return nil
	}
	return err
}

// startRepingLocked pings the server with an exponential backoff until it answers.
func (p *Provider) startRepingLocked(client redis.UniversalClient, logger *fflog.FFLogger) {
	ctx, cancel := context.WithCancel(context.Background())
	p.repingCancel = cancel
	delay := p.repingDelay
	if delay <= 0 {
		delay = repingMin
	}
	p.repingWg.Add(1)
	go func() {
		defer p.repingWg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			pingCtx, pcancel := context.WithTimeout(ctx, 2*time.Second)
			err := client.Ping(pingCtx).Err()
			pcancel()
			if err == nil {
				if ctx.Err() == nil {
					p.setStatus(client, retriever.RetrieverReady)
					if logger != nil {
						logger.Info("the redis audience provider is connected")
					}
				}
				return
			}
			delay = min(delay*2, repingMax)
		}
	}()
}

// setStatus sets the status unless the client has been replaced or shut down meanwhile.
func (p *Provider) setStatus(client redis.UniversalClient, st retriever.Status) {
	p.mu.RLock()
	same := p.client == client && p.status == st
	p.mu.RUnlock()
	if same {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client == client && client != nil {
		p.status = st
	}
}

// Status returns the readiness of the provider.
func (p *Provider) Status() retriever.Status {
	if p == nil {
		return retriever.RetrieverNotReady
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.status == "" {
		return retriever.RetrieverNotReady
	}
	return p.status
}

// Shutdown stops the background ping, closes the client and sets the status as not ready.
func (p *Provider) Shutdown(_ context.Context) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	err := p.stopLocked()
	p.status = retriever.RetrieverNotReady
	if err != nil {
		p.status = retriever.RetrieverError
	}
	p.mu.Unlock()
	p.repingWg.Wait()
	return err
}

func (p *Provider) getClient() (redis.UniversalClient, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.client == nil {
		return nil, errors.New("redisprovider: provider is not initialized")
	}
	return p.client, nil
}

// StoreKey returns where the id of an audience is stored for the list settings: the Redis key
// and the member to look for in it. With the set layout the key holds the whole list and the
// member is the encoded id; with the keys layout the key is specific to the id and the member
// is the encoded id too (it is the suffix of the key). The id is normalized, hashed and encoded
// according to the settings (audience.EncodeMember). It is the single source of truth for the
// provider and for the tools that show where an id is stored.
func StoreKey(list, id string, s audience.ListSettings) (key, member string) {
	member = audience.EncodeMember(id, s)
	if s.Layout == audience.LayoutKeys {
		return memberKey(s.Prefix, list, member), member
	}
	return setKey(s.Prefix, list), member
}

// setKey is the key of a list in the set layout.
func setKey(prefix, name string) string { return prefix + "{" + name + "}" }

// memberKey is the key of one id in the keys layout.
func memberKey(prefix, name, member string) string { return prefix + name + ":" + member }

// pending links a pipelined command to the lookups it answers.
type pending struct {
	idx  []int // indexes in the input lookups
	smis *redis.BoolSliceCmd
	ex   *redis.IntCmd
}

// Contains answers all the lookups in one pipeline. It always returns len(lookups) results.
func (p *Provider) Contains(ctx context.Context, lookups []audience.Lookup) ([]audience.Result, error) {
	results := make([]audience.Result, len(lookups))
	if len(lookups) == 0 {
		return results, nil
	}
	client, err := p.getClient()
	if err != nil {
		return failAll(results, err), err
	}
	if err := ctx.Err(); err != nil {
		return failAll(results, err), err
	}

	pipe := client.Pipeline()
	var cmds []pending
	setGroups := map[string]int{} // set key -> index in cmds
	var setMembers [][]string
	for i, l := range lookups {
		prefix := l.Settings.Prefix
		if l.Settings.Layout == audience.LayoutKeys {
			cmds = append(cmds, pending{idx: []int{i}, ex: pipe.Exists(ctx, memberKey(prefix, l.List, l.Member))})
			setMembers = append(setMembers, nil)
			continue
		}
		key := setKey(prefix, l.List)
		g, ok := setGroups[key]
		if !ok {
			g = len(cmds)
			setGroups[key] = g
			cmds = append(cmds, pending{})
			setMembers = append(setMembers, nil)
		}
		cmds[g].idx = append(cmds[g].idx, i)
		setMembers[g] = append(setMembers[g], l.Member)
	}
	// Issue the SMISMEMBER commands once the members of each set are grouped.
	for key, g := range setGroups {
		args := make([]any, len(setMembers[g]))
		for j, m := range setMembers[g] {
			args[j] = m
		}
		cmds[g].smis = pipe.SMIsMember(ctx, key, args...)
	}

	_, execErr := pipe.Exec(ctx)
	// Every command carries its own outcome: on a Cluster the client splits the pipeline per
	// node, so a node failure only fails the commands sent to that node. A non-nil execErr
	// (network error, timeout, command error) is therefore not turned into a failure of all.
	fillResults(results, cmds)
	if execErr != nil && !isCommandError(execErr) {
		if ctx.Err() == nil { // a cancelled request does not tell anything about the server
			p.setStatus(client, retriever.RetrieverError)
		}
	} else {
		p.setStatus(client, retriever.RetrieverReady)
	}
	return results, nil
}

// fillResults maps the replies of the pipelined commands back to the input order.
func fillResults(results []audience.Result, cmds []pending) {
	for _, c := range cmds {
		switch {
		case c.ex != nil:
			n, err := c.ex.Result()
			setResult(results, c.idx[0], n > 0, err)
		case c.smis != nil:
			vals, err := c.smis.Result()
			for j, i := range c.idx {
				switch {
				case err != nil:
					results[i].Err = err
				case j >= len(vals):
					results[i].Err = errors.New("redisprovider: short SMISMEMBER reply")
				default:
					results[i].Member = vals[j]
				}
			}
		}
	}
}

// isCommandError reports whether err is a server reply error (the pipeline itself worked and
// each command carries its own error).
func isCommandError(err error) bool {
	var rerr redis.Error
	return errors.As(err, &rerr)
}

func setResult(results []audience.Result, i int, member bool, err error) {
	if err != nil {
		results[i].Err = err
		return
	}
	results[i].Member = member
}

func failAll(results []audience.Result, err error) []audience.Result {
	for i := range results {
		results[i] = audience.Result{Err: err}
	}
	return results
}

// Exists reports which lists exist, using each list's prefix and layout: for the set layout
// EXISTS on "<prefix>{<name>}", for the keys layout a SCAN for "<prefix><name>:*" (on every
// master for a Cluster) that stops at the first match. The SCAN is bounded (maxScanCalls calls
// of COUNT scanCount per node): a keys list not found within the bound is left out of the
// result, it is unknown rather than missing (see the package documentation for the cost).
func (p *Provider) Exists(ctx context.Context, lists map[string]audience.ListSettings) (map[string]bool, error) {
	client, err := p.getClient()
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(lists))
	if len(lists) == 0 {
		return out, nil
	}
	pipe := client.Pipeline()
	setCmds := map[string]*redis.IntCmd{}
	var keyNames []string
	for n, st := range lists {
		if st.Layout == audience.LayoutKeys {
			keyNames = append(keyNames, n)
			continue
		}
		setCmds[n] = pipe.Exists(ctx, setKey(st.Prefix, n))
	}
	if len(setCmds) > 0 {
		if _, err := pipe.Exec(ctx); err != nil && !isCommandError(err) {
			return nil, err
		}
	}
	for n, c := range setCmds {
		v, err := c.Result()
		if err != nil {
			return nil, err
		}
		out[n] = v > 0
	}
	sort.Strings(keyNames)
	for _, n := range keyNames {
		found, complete, err := anyKeyMatches(ctx, client, escapeGlob(lists[n].Prefix+n)+":*")
		if err != nil {
			return nil, err
		}
		if found || complete {
			out[n] = found
		}
	}
	return out, nil
}

// scanFunc is one SCAN call.
type scanFunc func(ctx context.Context, cursor uint64) (keys []string, next uint64, err error)

// boundedScan calls scan until a key is returned, the cursor is back to 0 (complete: the
// whole keyspace has been walked) or maxCalls calls were made (not complete).
func boundedScan(ctx context.Context, scan scanFunc, maxCalls int) (found, complete bool, err error) {
	var cursor uint64
	for i := 0; i < maxCalls; i++ {
		keys, next, err := scan(ctx, cursor)
		if err != nil {
			return false, false, err
		}
		if len(keys) > 0 {
			return true, false, nil
		}
		if next == 0 {
			return false, true, nil
		}
		cursor = next
	}
	return false, false, nil
}

// anyKeyMatches scans (on every master for a Cluster) until a key matches the pattern. complete
// is false when the bound was reached on a node without finding a key.
func anyKeyMatches(
	ctx context.Context, client redis.UniversalClient, pattern string,
) (found, complete bool, err error) {
	scanOn := func(ctx context.Context, c redis.Cmdable) (bool, bool, error) {
		return boundedScan(ctx, func(ctx context.Context, cursor uint64) ([]string, uint64, error) {
			return c.Scan(ctx, cursor, pattern, scanCount).Result()
		}, maxScanCalls)
	}
	if cc, ok := client.(*redis.ClusterClient); ok {
		var mu sync.Mutex
		complete = true
		err = cc.ForEachMaster(ctx, func(ctx context.Context, node *redis.Client) error {
			f, c, err := scanOn(ctx, node)
			mu.Lock()
			found = found || f
			complete = complete && c
			mu.Unlock()
			return err
		})
		if found {
			return true, false, nil
		}
		return false, complete && err == nil, err
	}
	return scanOn(ctx, client)
}

// escapeGlob escapes the special characters of a Redis MATCH pattern.
func escapeGlob(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`*?[]\^-`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// serverVersion reads redis_version from INFO server.
func serverVersion(ctx context.Context, client redis.UniversalClient) (string, bool) {
	info, err := client.Info(ctx, "server").Result()
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(info, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "redis_version:"); ok {
			return v, true
		}
	}
	return "", false
}

// supportsSMIsMember tells if a redis_version (Valkey reports 7.2.4) is at least 6.2.
func supportsSMIsMember(version string) bool {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return true // unknown format, do not warn
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return true
	}
	return major > 6 || (major == 6 && minor >= 2)
}
