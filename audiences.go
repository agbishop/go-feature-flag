package ffclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thomaspoignant/go-feature-flag/audience"
	"github.com/thomaspoignant/go-feature-flag/ffcontext"
	"github.com/thomaspoignant/go-feature-flag/modules/core/flag"
	"github.com/thomaspoignant/go-feature-flag/notifier"
	"github.com/thomaspoignant/go-feature-flag/retriever"
	"github.com/thomaspoignant/go-feature-flag/utils/fflog"
)

const (
	// audienceInitTimeout bounds the provider Init, which must never block New for long.
	audienceInitTimeout = 10 * time.Second
)

// audiencesRuntime holds what ffclient needs at runtime to support audience() terms.
// All its methods are safe to call on a nil receiver (audiences not configured).
// The provider and the resolver can be replaced while running (see update), they are held in an
// immutable audiencesState read with a single atomic load.
type audiencesRuntime struct {
	state   atomic.Pointer[audiencesState]
	logger  *fflog.FFLogger
	metrics audience.Metrics

	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup

	// confirmed holds the lists known to exist in the store, they are not checked again.
	confirmed map[string]struct{}
	// pending holds the lists being checked, a concurrent check does not check them again.
	pending map[string]struct{}
	// checked holds, per flag, the fingerprint of the flag at its last check. A flag that did not
	// change since is not checked again, so the initial check and the first reload warn once.
	checked map[string]string
}

// audiencesState is the part of the runtime replaced by update. It is never modified once
// published.
type audiencesState struct {
	provider audience.Provider
	resolver *audience.Resolver
	// initDone is closed once the provider Init returned (successfully or not).
	initDone chan struct{}
	// ctx is canceled when the provider is retired, it stops its Init.
	ctx    context.Context
	cancel context.CancelFunc
}

// errAudienceNotReady is the failure of the lookups while the provider is still initializing.
var errAudienceNotReady = errors.New("the audience provider is not initialized yet")

// gatedProvider delays the lookups until the background Init of the provider is over. A lookup
// waits at most for its own deadline (the lookup timeout), then fails with errAudienceNotReady:
// the resolver turns it into an unknown term (or a non member with failMode empty).
type gatedProvider struct {
	audience.Provider
	done <-chan struct{}
}

func (g gatedProvider) wait(ctx context.Context) error {
	select {
	case <-g.done:
		return nil
	default:
	}
	select {
	case <-g.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%w: %w", errAudienceNotReady, ctx.Err())
	}
}

// Contains implements audience.Provider.
func (g gatedProvider) Contains(ctx context.Context, l []audience.Lookup) ([]audience.Result, error) {
	if err := g.wait(ctx); err != nil {
		return nil, err
	}
	return g.Provider.Contains(ctx, l)
}

// Exists implements audience.Provider.
func (g gatedProvider) Exists(ctx context.Context, l map[string]audience.ListSettings) (map[string]bool, error) {
	if err := g.wait(ctx); err != nil {
		return nil, err
	}
	return g.Provider.Exists(ctx, l)
}

// newAudiencesRuntime builds the resolver and initializes the provider in the background: it
// returns immediately, whatever the state of the store. A provider that fails to initialize is
// logged and kept: the readiness of GO Feature Flag never depends on the audience store, the
// lookups report errors (unknown terms) until the provider answers.
func newAudiencesRuntime(config Config) *audiencesRuntime {
	if config.Audiences == nil {
		return nil
	}
	a := config.Audiences
	rt := &audiencesRuntime{
		logger: config.internalLogger, metrics: a.Metrics, confirmed: map[string]struct{}{},
		pending: map[string]struct{}{}, checked: map[string]string{},
	}
	rt.ctx, rt.cancel = context.WithCancel(context.Background())
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.state.Store(rt.newState(a.Provider, a.Config, a.Metrics))
	return rt
}

// newState builds a state with a new provider, initialized in the background. The caller
// holds a.mu.
func (a *audiencesRuntime) newState(
	p audience.Provider, cfg audience.Config, metrics audience.Metrics,
) *audiencesState {
	st := &audiencesState{provider: p, initDone: make(chan struct{})}
	st.ctx, st.cancel = context.WithCancel(a.ctx)
	var gated audience.Provider
	if p != nil {
		gated = gatedProvider{Provider: p, done: st.initDone}
		a.wg.Add(1)
		go a.initProvider(st)
	} else {
		close(st.initDone)
		a.logger.Error("audiences are configured without a provider, audience lookups will fail")
	}
	st.resolver = audience.NewResolver(gated, cfg, metrics, a.logger)
	return st
}

func (a *audiencesRuntime) initProvider(st *audiencesState) {
	defer a.wg.Done()
	defer close(st.initDone)
	defer func() {
		if r := recover(); r != nil {
			a.logger.Error("the audience provider panicked while initializing", slog.Any("panic", r))
		}
	}()
	initCtx, cancel := context.WithTimeout(st.ctx, audienceInitTimeout)
	defer cancel()
	if err := st.provider.Init(initCtx, a.logger); err != nil {
		a.logger.Error(
			"impossible to initialize the audience provider, audience lookups will fail until it is available",
			slog.Any("error", err))
	}
}

// update replaces the configuration of the audiences while running. A nil provider (or the
// current one) keeps the running provider and its connection, only the resolver (cache,
// defaults and definitions) is replaced. Another provider is initialized in the background and
// the previous one is shut down once the new state is published. The lookups in flight finish
// on the state they started with.
func (a *audiencesRuntime) update(cfg *AudiencesConfig) error {
	if a == nil {
		return ErrAudiencesNotConfigured
	}
	if cfg == nil {
		return errors.New("the audiences configuration is nil")
	}
	if err := cfg.Config.IsValid(); err != nil {
		return err
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return errors.New("GO Feature Flag is closed")
	}
	old := a.state.Load()
	metrics := cfg.Metrics
	if metrics == nil {
		metrics = a.metrics
	}
	a.metrics = metrics
	reuse := cfg.Provider == nil || cfg.Provider == old.provider
	var st *audiencesState
	if reuse {
		var gated audience.Provider
		if old.provider != nil {
			gated = gatedProvider{Provider: old.provider, done: old.initDone}
		}
		st = &audiencesState{
			provider: old.provider, initDone: old.initDone, ctx: old.ctx, cancel: old.cancel,
			resolver: audience.NewResolverFrom(old.resolver, gated, cfg.Config, metrics, a.logger),
		}
	} else {
		st = a.newState(cfg.Provider, cfg.Config, metrics)
		a.wg.Add(1)
		go a.retire(old)
	}
	a.state.Store(st)
	a.confirmed = map[string]struct{}{}
	a.checked = map[string]string{}
	a.mu.Unlock()
	old.resolver.Close()
	return nil
}

// retire shuts down a provider that is no longer used.
func (a *audiencesRuntime) retire(old *audiencesState) {
	defer a.wg.Done()
	old.cancel()
	<-old.initDone
	if old.provider == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), audienceInitTimeout)
	defer cancel()
	if err := old.provider.Shutdown(ctx); err != nil {
		a.logger.Error("error while shutting down the audience provider", slog.Any("error", err))
	}
}

func (a *audiencesRuntime) close(ctx context.Context) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.closed = true
	a.mu.Unlock()
	a.cancel()
	a.wg.Wait()
	st := a.state.Load()
	st.resolver.Close()
	if st.provider != nil {
		if err := st.provider.Shutdown(ctx); err != nil {
			a.logger.Error("error while shutting down the audience provider", slog.Any("error", err))
		}
	}
}

// ErrAudiencesNotConfigured is returned by UpdateAudiences when the client was created without
// audiences: they cannot be enabled while running.
var ErrAudiencesNotConfigured = errors.New("audiences are not configured on this client")

// UpdateAudiences replaces the audiences configuration (cache, lookup timeout, fail mode,
// defaults and definitions) without restarting. It is safe to call while flags are evaluated.
//
// A nil cfg.Provider keeps the running provider, so the connection to the store is reused. A
// different provider is initialized in the background (the evaluations report the audience
// terms as unknown until it answers) and the previous one is shut down. The cache of the
// lookups is dropped. Audiences must have been configured when the client was created.
func (g *GoFeatureFlag) UpdateAudiences(cfg *AudiencesConfig) error {
	if g == nil {
		return ErrAudiencesNotConfigured
	}
	if err := g.audiences.update(cfg); err != nil {
		return err
	}
	g.audiences.checkInitialFlags(g.retrieverManager)
	return nil
}

// defaultEvalContext is the context used by the evaluation methods without a context parameter.
func (g *GoFeatureFlag) defaultEvalContext() context.Context {
	if g == nil || g.config.Context == nil {
		return context.Background()
	}
	return g.config.Context
}

// resolveAudiences returns the audience membership for one flag, nil when no audience is
// configured (the core then reports NOT_CONFIGURED) or when the flag does not use audiences.
func (g *GoFeatureFlag) resolveAudiences(
	ctx context.Context, f flag.Flag, evalCtx ffcontext.Context, enrichment map[string]any,
) flag.AudienceMembership {
	if g.audiences == nil {
		return nil
	}
	internal, ok := f.(*flag.InternalFlag)
	if !ok {
		return nil
	}
	terms := internal.AudienceTerms()
	if len(terms) == 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return g.audiences.state.Load().resolver.Resolve(ctx, terms, evalCtx, enrichment)
}

// resolveAllAudiences returns the audience membership for several flags with a single lookup step.
func (g *GoFeatureFlag) resolveAllAudiences(
	ctx context.Context, flags map[string]flag.Flag, evalCtx ffcontext.Context, enrichment map[string]any,
) flag.AudienceMembership {
	if g.audiences == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return g.audiences.state.Load().resolver.ResolveAll(ctx, flags, evalCtx, enrichment)
}

// audienceListNames returns the sorted distinct audience list names used by the flags.
func audienceListNames(flags map[string]flag.Flag) []string {
	set := map[string]struct{}{}
	for _, f := range flags {
		if internal, ok := f.(*flag.InternalFlag); ok {
			for _, t := range internal.AudienceTerms() {
				set[t.List] = struct{}{}
			}
		}
	}
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// checkExistence warns about every referenced list that does not exist in the store. It runs
// in the background and never blocks flag loading. Lists confirmed to exist are not checked
// again (a list that is missing is checked again at the next flag change). Member ids are
// never logged.
func (a *audiencesRuntime) checkExistence(flags map[string]flag.Flag) {
	if a == nil || a.state.Load().provider == nil {
		return
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	names := a.claimLists(flags)
	if len(names) == 0 {
		a.mu.Unlock()
		return
	}
	a.wg.Add(1)
	a.mu.Unlock()
	go func() {
		defer a.wg.Done()
		defer a.releaseLists(names)
		defer func() {
			if r := recover(); r != nil {
				a.logger.Error("audience existence check panicked", slog.Any("panic", r))
			}
		}()
		st := a.state.Load()
		select {
		case <-st.initDone:
		case <-a.ctx.Done():
			return
		}
		exists, err := st.resolver.CheckExists(a.ctx, names)
		if err != nil {
			a.logger.Warn("impossible to check that the audiences used by the flags exist",
				slog.Any("error", err))
			// let the next flag change try again.
			a.mu.Lock()
			a.checked = map[string]string{}
			a.mu.Unlock()
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.state.Load() != st {
			// the configuration was replaced while checking: the result is stale.
			return
		}
		for _, n := range names {
			found, known := exists[n]
			switch {
			case !known:
				// the store could not tell within a bounded effort: neither confirmed nor missing.
				a.logger.Debug("impossible to tell if an audience exists in the store", slog.String("audience", n))
			case found:
				a.confirmed[n] = struct{}{}
			default:
				a.logger.Warn("the flags reference an audience that does not exist in the store, "+
					"it matches nobody", slog.String("audience", n))
			}
		}
	}()
}

// claimLists returns the lists to check for the flags and marks them as being checked. A flag
// that did not change since its last check is skipped, as well as the lists
// confirmed to exist or already being checked. The caller must hold a.mu.
func (a *audiencesRuntime) claimLists(flags map[string]flag.Flag) []string {
	set := map[string]struct{}{}
	for key, f := range flags {
		lists := audienceListNames(map[string]flag.Flag{key: f})
		sig := flagFingerprint(f)
		if prev, ok := a.checked[key]; ok && prev == sig {
			continue
		}
		a.checked[key] = sig
		for _, n := range lists {
			set[n] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for n := range set {
		if _, ok := a.confirmed[n]; ok {
			continue
		}
		if _, ok := a.pending[n]; ok {
			continue
		}
		a.pending[n] = struct{}{}
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// flagFingerprint identifies the content of a flag, to tell if a notified flag really changed.
func flagFingerprint(f flag.Flag) string {
	b, err := json.Marshal(f)
	if err != nil {
		return fmt.Sprintf("%p", f) // cannot be compared: always considered as changed
	}
	return string(b)
}

// releaseLists marks the lists as no longer being checked.
func (a *audiencesRuntime) releaseLists(names []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, n := range names {
		delete(a.pending, n)
	}
}

// checkInitialFlags checks the flags loaded during the initialization (notifiers can be
// disabled on init).
func (a *audiencesRuntime) checkInitialFlags(m *retriever.Manager) {
	if a == nil || m == nil {
		return
	}
	flags, err := m.GetFlagsFromCache(a.ctx)
	if err != nil {
		return
	}
	a.checkExistence(flags)
}

// existenceNotifier is notified when the flags change, to check the new audience lists.
func (a *audiencesRuntime) existenceNotifier() notifier.Notifier {
	return existenceNotifier{a: a}
}

type existenceNotifier struct{ a *audiencesRuntime }

// Notify implements notifier.Notifier.
func (n existenceNotifier) Notify(diff notifier.DiffCache) error {
	changed := make(map[string]flag.Flag, len(diff.Added)+len(diff.Updated))
	for k, f := range diff.Added {
		changed[k] = f
	}
	for k, u := range diff.Updated {
		changed[k] = u.After
	}
	n.a.checkExistence(changed)
	return nil
}
