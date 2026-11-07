// Package audience resolves the membership of evaluation contexts in external audiences
// (the audience() query term). It holds the Provider contract implemented by the storage
// backends, the configuration, and the Resolver that ffclient and the relay proxy call
// before evaluating flags.
//
// Normalization, hashing and encoding of ids are done here, once, so that providers only
// receive the exact id to look for in their store (see Lookup.Member).
package audience

import (
	"context"

	"github.com/thomaspoignant/go-feature-flag/retriever"
	"github.com/thomaspoignant/go-feature-flag/utils/fflog"
)

// Lookup is one membership question sent to a Provider.
type Lookup struct {
	// List is the audience name, as written in the flag.
	List string
	// Member is the id to look for, already normalized, hashed and encoded according to
	// Settings (see EncodeMember). Providers must use it as is. With Encoding "raw" it is the
	// 32 raw bytes of the digest held in a Go string.
	Member string
	// Settings are the effective settings of the list (defaults, glob and definition merged).
	// Providers use Settings.Prefix (empty means the provider's own default prefix),
	// Settings.Layout to pick the storage layout and Settings.Encoding for key encoding.
	Settings ListSettings
}

// Result is the answer to one Lookup. When Err is not nil, Member must be ignored.
type Result struct {
	Member bool
	Err    error
}

// Provider is a read-only audience store.
type Provider interface {
	// Init prepares the provider (connects to the store).
	Init(ctx context.Context, logger *fflog.FFLogger) error
	// Shutdown releases the resources of the provider.
	Shutdown(ctx context.Context) error
	// Status is the readiness of the provider (ready, not ready or error).
	Status() retriever.Status
	// Contains answers all the lookups in as few round trips as the backend allows.
	// It returns exactly one Result per lookup, in the same order. A failure that affects
	// one lookup goes in its Result.Err; a failure of the whole call is the returned error.
	Contains(ctx context.Context, lookups []Lookup) ([]Result, error)
	// Exists reports which lists exist, for load-time warnings and lint. The keys are the list
	// names and the values their effective settings (prefix and layout tell where to look).
	// A list that does not exist is not an error for Contains: it matches nobody.
	// A list the provider cannot tell about within a bounded effort (e.g. a huge keyspace scan)
	// is left out of the map: it is unknown, not missing, and must not be reported as missing.
	Exists(ctx context.Context, lists map[string]ListSettings) (map[string]bool, error)
}
