// Package audiencetest provides an in-memory audience provider with fault injection and the
// contract suite every audience.Provider implementation must pass.
package audiencetest

import (
	"context"
	"sync"
	"time"

	"github.com/thomaspoignant/go-feature-flag/audience"
	"github.com/thomaspoignant/go-feature-flag/retriever"
	"github.com/thomaspoignant/go-feature-flag/utils/fflog"
)

// Seeder populates the store behind a provider, for the contract suite.
// Members are the stored ids, already encoded (see audience.EncodeMember).
type Seeder interface {
	// SupportedLayouts lists the layouts (audience.LayoutSet, audience.LayoutKeys) the provider handles.
	SupportedLayouts() []string
	// Add creates the list if needed and adds the members.
	Add(ctx context.Context, list string, settings audience.ListSettings, members ...string) error
	// Remove removes members from the list.
	Remove(ctx context.Context, list string, settings audience.ListSettings, members ...string) error
	// DeleteList removes the whole list.
	DeleteList(ctx context.Context, list string, settings audience.ListSettings) error
}

// FakeProvider is an in-memory audience.Provider and Seeder with fault injection.
// It is safe for concurrent use.
type FakeProvider struct {
	mu        sync.Mutex
	lists     map[string]map[string]struct{}
	status    retriever.Status
	errAll    error
	errLists  map[string]error
	delay     time.Duration
	calls     int
	lookupsN  int
	lastBatch []audience.Lookup
}

// NewFakeProvider returns an empty provider, not initialized yet.
func NewFakeProvider() *FakeProvider {
	return &FakeProvider{
		lists:    map[string]map[string]struct{}{},
		status:   retriever.RetrieverNotReady,
		errLists: map[string]error{},
	}
}

// FailAll makes every Contains/Exists call return err (nil restores the normal behavior).
func (f *FakeProvider) FailAll(err error) { f.mu.Lock(); f.errAll = err; f.mu.Unlock() }

// FailList makes the results of the list carry err (nil restores the normal behavior).
func (f *FakeProvider) FailList(list string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		delete(f.errLists, list)
		return
	}
	f.errLists[list] = err
}

// SetDelay makes every call wait d (or the end of its context, then it returns ctx.Err()).
func (f *FakeProvider) SetDelay(d time.Duration) { f.mu.Lock(); f.delay = d; f.mu.Unlock() }

// Calls is the number of Contains calls received.
func (f *FakeProvider) Calls() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

// LookupCount is the total number of lookups received by Contains.
func (f *FakeProvider) LookupCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.lookupsN }

// LastLookups is a copy of the lookups of the last Contains call.
func (f *FakeProvider) LastLookups() []audience.Lookup {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]audience.Lookup(nil), f.lastBatch...)
}

// Init implements audience.Provider.
func (f *FakeProvider) Init(_ context.Context, _ *fflog.FFLogger) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = retriever.RetrieverReady
	return nil
}

// Shutdown implements audience.Provider.
func (f *FakeProvider) Shutdown(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = retriever.RetrieverNotReady
	return nil
}

// Status implements audience.Provider.
func (f *FakeProvider) Status() retriever.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

func (f *FakeProvider) wait(ctx context.Context) error {
	f.mu.Lock()
	d := f.delay
	f.mu.Unlock()
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Contains implements audience.Provider.
func (f *FakeProvider) Contains(ctx context.Context, lookups []audience.Lookup) ([]audience.Result, error) {
	f.mu.Lock()
	f.calls++
	f.lookupsN += len(lookups)
	f.lastBatch = append([]audience.Lookup(nil), lookups...)
	errAll := f.errAll
	f.mu.Unlock()
	if err := f.wait(ctx); err != nil {
		return nil, err
	}
	if errAll != nil {
		return nil, errAll
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]audience.Result, len(lookups))
	for i, l := range lookups {
		if err := f.errLists[l.List]; err != nil {
			res[i].Err = err
			continue
		}
		_, res[i].Member = f.lists[l.List][l.Member]
	}
	return res, nil
}

// Exists implements audience.Provider.
func (f *FakeProvider) Exists(ctx context.Context, lists map[string]audience.ListSettings) (map[string]bool, error) {
	if err := f.wait(ctx); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errAll != nil {
		return nil, f.errAll
	}
	out := make(map[string]bool, len(lists))
	for n := range lists {
		_, out[n] = f.lists[n]
	}
	return out, nil
}

// SupportedLayouts implements Seeder: the fake ignores the layout.
func (f *FakeProvider) SupportedLayouts() []string {
	return []string{audience.LayoutSet, audience.LayoutKeys}
}

// Add implements Seeder.
func (f *FakeProvider) Add(_ context.Context, list string, _ audience.ListSettings, members ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lists[list] == nil {
		f.lists[list] = map[string]struct{}{}
	}
	for _, m := range members {
		f.lists[list][m] = struct{}{}
	}
	return nil
}

// Remove implements Seeder.
func (f *FakeProvider) Remove(_ context.Context, list string, _ audience.ListSettings, members ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range members {
		delete(f.lists[list], m)
	}
	return nil
}

// DeleteList implements Seeder.
func (f *FakeProvider) DeleteList(_ context.Context, list string, _ audience.ListSettings) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.lists, list)
	return nil
}
