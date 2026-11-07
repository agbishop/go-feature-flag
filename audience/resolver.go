package audience

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/thomaspoignant/go-feature-flag/modules/core/ffcontext"
	"github.com/thomaspoignant/go-feature-flag/modules/core/flag"
	"github.com/thomaspoignant/go-feature-flag/utils/fflog"
	"golang.org/x/sync/singleflight"
)

// errProviderNotConfigured is the failure of every lookup when no provider is set.
var errProviderNotConfigured = errors.New("audience provider is not configured")

// Resolver answers the audience terms of flags for an evaluation context. ffclient and the
// relay proxy call it before the evaluation and put the result in flag.Context.AudienceMembership.
// It is safe for concurrent use.
type Resolver struct {
	provider Provider
	cfg      Config
	metrics  Metrics
	logger   *fflog.FFLogger
	cache    *lru.Cache[string, cacheEntry] // nil when disabled
	flights  singleflight.Group

	// now is the clock, replaced in the tests.
	now func() time.Time
	// existsTimeout bounds CheckExists.
	existsTimeout time.Duration

	logMu      sync.Mutex
	failing    bool
	lastFailAt time.Time
}

// DefaultExistsTimeout bounds one load-time existence check (CheckExists).
const DefaultExistsTimeout = 2 * time.Second

// failureLogInterval is the minimum delay between two store failure warnings.
const failureLogInterval = time.Minute

// cacheEntry is a cached answer with its expiry (lazy expiry: no background goroutine).
type cacheEntry struct {
	member  bool
	expires time.Time
}

// NewResolver builds a Resolver. metrics and logger can be nil. The config is expected to be
// valid (see Config.IsValid). A nil provider makes every lookup fail (unknown, or non member
// with failMode empty).
func NewResolver(provider Provider, cfg Config, metrics Metrics, logger *fflog.FFLogger) *Resolver {
	if metrics == nil {
		metrics = NopMetrics{}
	}
	r := &Resolver{
		provider: provider, cfg: cfg, metrics: metrics, logger: logger,
		now: time.Now, existsTimeout: DefaultExistsTimeout,
	}
	if cfg.Cache.Size > 0 && cfg.Cache.TTL > 0 {
		if c, err := lru.New[string, cacheEntry](cfg.Cache.Size); err == nil {
			r.cache = c
		}
	}
	return r
}

// NewResolverFrom builds a Resolver like NewResolver and carries over the cache of prev, so a
// configuration change does not send a burst of lookups to the store. The caller must use the
// same provider (store) as prev, otherwise it must use NewResolver.
//
// The cache is carried over only when its size is unchanged. An entry is kept only when the
// effective settings of its list (prefix, layout, normalize, hash, encoding) are unchanged: the
// others are dropped and fetched again. A kept entry keeps its expiry, so a TTL change applies
// to the entries cached from then on. The entries are copied (not shared), so lookups still
// running on prev cannot add a stale entry to the new cache. It is safe to call while prev is
// used by concurrent evaluations.
func NewResolverFrom(prev *Resolver, provider Provider, cfg Config, metrics Metrics, logger *fflog.FFLogger) *Resolver {
	r := NewResolver(provider, cfg, metrics, logger)
	if prev == nil || prev.cache == nil || r.cache == nil || prev.cfg.Cache.Size != cfg.Cache.Size {
		return r
	}
	oldCfg, newCfg := prev.cfg, cfg
	unchanged := map[string]bool{}
	now := prev.now()
	for _, key := range prev.cache.Keys() { // from the oldest to the newest: keeps the LRU order
		list, _, _ := strings.Cut(key, "\x00")
		same, known := unchanged[list]
		if !known {
			same = sameCachedForm(oldCfg.Resolve(list), newCfg.Resolve(list))
			unchanged[list] = same
		}
		if !same {
			continue
		}
		if e, ok := prev.cache.Peek(key); ok && now.Before(e.expires) {
			r.cache.Add(key, e)
		}
	}
	return r
}

// sameCachedForm tells if two settings of a list give the same answers for the same id: the
// fail mode only matters for failures, which are never cached.
func sameCachedForm(a, b ListSettings) bool {
	a.FailMode, b.FailMode = "", ""
	return a == b
}

// Close releases the resources of the Resolver (it drops the cache). It starts no goroutine
// and can be called several times. It does not shut the provider down.
func (r *Resolver) Close() {
	if r == nil || r.cache == nil {
		return
	}
	r.cache.Purge()
}

func (r *Resolver) cacheGet(key string) (bool, bool) {
	e, ok := r.cache.Get(key)
	if !ok {
		return false, false
	}
	if !r.now().Before(e.expires) {
		r.cache.Remove(key)
		return false, false
	}
	return e.member, true
}

func (r *Resolver) cacheAdd(key string, member bool) {
	r.cache.Add(key, cacheEntry{member: member, expires: r.now().Add(r.cfg.Cache.TTL)})
}

type pair = flag.AudienceLookup

func cacheKey(p pair) string { return p.List + "\x00" + p.Value }

// Resolve looks up every (list, attribute value) pair of the terms in evalCtx overlaid with
// the enrichment (the evaluation context enrichment of the flag.Context, including the
// automatic "env"), exactly as the evaluation reads its attributes (see flag.AudienceQueryMap).
// A nil evalCtx is an empty context.
//
// Contract: for every term whose attribute is a string in evalCtx, the returned map has an
// entry. Member is true or false when the store answered. When it did not (error, timeout,
// provider missing) the entry has Err true, except when the effective failMode of the list is
// "empty": then it is a non member without Err. Terms with a missing or non string attribute
// get no entry (the core treats them as unknown). The map is never nil.
func (r *Resolver) Resolve(
	ctx context.Context, terms []flag.AudienceTerm, evalCtx ffcontext.Context, enrichment map[string]any,
) flag.AudienceMembership {
	return r.resolve(ctx, terms, evalCtx, enrichment)
}

// ResolveAll is the bulk variant: it collects the terms of all flags (non internal flags are
// ignored) and resolves them with a single provider call.
func (r *Resolver) ResolveAll(
	ctx context.Context, flags map[string]flag.Flag, evalCtx ffcontext.Context, enrichment map[string]any,
) flag.AudienceMembership {
	var terms []flag.AudienceTerm
	for _, f := range flags {
		if v, ok := f.(*flag.InternalFlag); ok {
			terms = append(terms, v.AudienceTerms()...)
		}
	}
	return r.resolve(ctx, terms, evalCtx, enrichment)
}

func (r *Resolver) resolve(
	ctx context.Context, terms []flag.AudienceTerm, evalCtx ffcontext.Context, enrichment map[string]any,
) flag.AudienceMembership {
	out := flag.AudienceMembership{}
	if len(terms) == 0 {
		return out
	}
	ctxMap := flag.AudienceQueryMap(evalCtx, enrichment)
	var misses []pair
	seen := map[pair]struct{}{}
	for _, t := range terms {
		value, ok := flag.AudienceAttributeValue(ctxMap, t.Attribute)
		if !ok {
			r.metrics.IncUnknown(t.List, ReasonMissingAttribute)
			continue
		}
		p := pair{List: t.List, Value: value}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		if r.cache != nil {
			if member, hit := r.cacheGet(cacheKey(p)); hit {
				r.metrics.IncCache(p.List, CacheHit)
				r.metrics.IncLookup(p.List, memberLabel(member))
				out[p] = flag.AudienceResult{Member: member}
				continue
			}
			r.metrics.IncCache(p.List, CacheMiss)
		}
		misses = append(misses, p)
	}
	if len(misses) == 0 {
		return out
	}
	r.fetch(ctx, misses, out)
	return out
}

func memberLabel(member bool) string {
	if member {
		return ResultMember
	}
	return ResultNotMember
}

// fetch asks the provider about the misses (one Contains call) and fills out.
func (r *Resolver) fetch(ctx context.Context, misses []pair, out flag.AudienceMembership) {
	sort.Slice(misses, func(i, j int) bool {
		if misses[i].List != misses[j].List {
			return misses[i].List < misses[j].List
		}
		return misses[i].Value < misses[j].Value
	})
	ctx, cancel := context.WithTimeout(ctx, r.cfg.lookupTimeout())
	defer cancel()

	results, err := r.callProvider(ctx, misses)
	r.reportHealth(ctx, err, results)
	for i, p := range misses {
		settings := r.cfg.Resolve(p.List)
		var res Result
		switch {
		case err != nil:
			res.Err = err
		case results[i].Err != nil:
			res.Err = results[i].Err
		default:
			res.Member = results[i].Member
		}
		if res.Err == nil {
			if r.cache != nil {
				r.cacheAdd(cacheKey(p), res.Member) // errors are never cached
			}
			r.metrics.IncLookup(p.List, memberLabel(res.Member))
			out[p] = flag.AudienceResult{Member: res.Member}
			continue
		}
		reason := ReasonStoreError
		if errors.Is(res.Err, context.DeadlineExceeded) || errors.Is(res.Err, context.Canceled) {
			reason = ReasonTimeout
		}
		r.metrics.IncLookup(p.List, ResultUnknown)
		r.metrics.IncUnknown(p.List, reason)
		if settings.FailMode == FailModeEmpty {
			out[p] = flag.AudienceResult{Member: false}
		} else {
			out[p] = flag.AudienceResult{Err: true}
		}
	}
}

// reportHealth logs the store failures (rate limited: the first one, then at most one per
// failureLogInterval) and the recovery. It logs the error class and message only, never the
// lists, ids or members.
func (r *Resolver) reportHealth(reqCtx context.Context, err error, results []Result) {
	if err == nil {
		for _, res := range results {
			if res.Err != nil {
				err = res.Err
				break
			}
		}
	}
	if r.logger == nil {
		return
	}
	r.logMu.Lock()
	defer r.logMu.Unlock()
	if err == nil {
		if r.failing {
			r.failing = false
			r.logger.Info("the audience store is available again")
		}
		return
	}
	if errors.Is(err, context.Canceled) && reqCtx.Err() != nil {
		return // the caller gave up, this is not a store failure
	}
	now := r.now()
	if r.failing && now.Sub(r.lastFailAt) < failureLogInterval {
		return
	}
	r.failing = true
	r.lastFailAt = now
	class := "store_error"
	if errors.Is(err, context.DeadlineExceeded) {
		class = "timeout"
	}
	r.logger.Warn("audience lookups are failing, the audience terms are unknown or empty",
		slog.String("class", class), slog.String("error", err.Error()))
}

// callProvider performs the singleflight-ed Contains call. On success it returns exactly
// len(misses) results.
//
// Singleflight is keyed by the whole sorted miss set rather than per pair: it keeps one
// Contains (one round trip) per request, and identical concurrent requests (same user, same
// flags, e.g. a burst of retries or a bulk evaluation) share one store call. Requests with
// overlapping but different sets are not merged, which is the price of not splitting a
// request into several round trips. The shared call is detached from the cancellation of the
// first caller (so a cancelled leader does not fail the followers) and bounded by the lookup
// timeout; each caller still stops waiting at its own deadline.
func (r *Resolver) callProvider(ctx context.Context, misses []pair) ([]Result, error) {
	if r.provider == nil {
		return nil, errProviderNotConfigured
	}
	var sb strings.Builder
	for _, p := range misses {
		fmt.Fprintf(&sb, "%d:%s%d:%s", len(p.List), p.List, len(p.Value), p.Value)
	}
	ch := r.flights.DoChan(sb.String(), func() (any, error) {
		callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.cfg.lookupTimeout())
		defer cancel()
		return r.contains(callCtx, misses)
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
		return res.Val.([]Result), nil
	}
}

// contains builds the lookups (normalization, hashing, encoding) and calls the provider.
func (r *Resolver) contains(ctx context.Context, misses []pair) (results []Result, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			results, err = nil, fmt.Errorf("audience provider panicked: %v", rec)
		}
	}()
	lookups := make([]Lookup, len(misses))
	durations := map[string]string{} // list -> layout
	for i, p := range misses {
		s := r.cfg.Resolve(p.List)
		lookups[i] = Lookup{List: p.List, Member: EncodeMember(p.Value, s), Settings: s}
		durations[p.List] = s.Layout
	}
	start := time.Now()
	results, err = r.provider.Contains(ctx, lookups)
	elapsed := time.Since(start)
	for list, layout := range durations {
		r.metrics.ObserveLookupDuration(list, layout, elapsed)
	}
	if err != nil {
		return nil, err
	}
	if len(results) != len(lookups) {
		return nil, fmt.Errorf("audience provider returned %d results for %d lookups", len(results), len(lookups))
	}
	return results, nil
}

// CheckExists reports which of the lists exist in the store, using the effective settings of
// each name, bounded by DefaultExistsTimeout. Lists the store cannot tell about (see
// Provider.Exists) are absent from the result. It is meant for load-time warnings; a nil Resolver
// or a missing provider is an error. It is safe to call on a nil Resolver.
func (r *Resolver) CheckExists(ctx context.Context, names []string) (res map[string]bool, err error) {
	if r == nil || r.provider == nil {
		return nil, errProviderNotConfigured
	}
	defer func() {
		if rec := recover(); rec != nil {
			res, err = nil, fmt.Errorf("audience provider panicked: %v", rec)
		}
	}()
	lists := make(map[string]ListSettings, len(names))
	for _, n := range names {
		lists[n] = r.cfg.Resolve(n)
	}
	callCtx, cancel := context.WithTimeout(ctx, r.existsTimeout)
	defer cancel()
	return r.provider.Exists(callCtx, lists)
}
