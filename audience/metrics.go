package audience

import "time"

// Label values of the metrics.
const (
	ResultMember    = "member"
	ResultNotMember = "not_member"
	ResultUnknown   = "unknown"

	CacheHit  = "hit"
	CacheMiss = "miss"

	ReasonStoreError       = "store_error"
	ReasonTimeout          = "timeout"
	ReasonMissingAttribute = "missing_attribute"
)

// Metrics is the hook the Resolver reports to. Implementations (Prometheus, OpenTelemetry...)
// live outside of this package and must be safe for concurrent use.
// Labels carry list names, never member ids or attribute values.
type Metrics interface {
	// IncLookup counts one resolved (list, id) pair: goff.audience.lookups{list,result}.
	// result is ResultMember, ResultNotMember or ResultUnknown.
	IncLookup(list, result string)
	// ObserveLookupDuration records the duration of a provider call for a list:
	// goff.audience.lookup.duration{list,layout}.
	ObserveLookupDuration(list, layout string, d time.Duration)
	// IncCache counts a cache outcome: goff.audience.cache{list,outcome}.
	// outcome is CacheHit or CacheMiss. Not called when the cache is disabled.
	IncCache(list, outcome string)
	// IncUnknown counts an unknown term: goff.audience.unknown{list,reason}.
	// reason is ReasonStoreError, ReasonTimeout or ReasonMissingAttribute.
	IncUnknown(list, reason string)
}

// NopMetrics is a Metrics that does nothing.
type NopMetrics struct{}

// IncLookup implements Metrics.
func (NopMetrics) IncLookup(string, string) {}

// ObserveLookupDuration implements Metrics.
func (NopMetrics) ObserveLookupDuration(string, string, time.Duration) {}

// IncCache implements Metrics.
func (NopMetrics) IncCache(string, string) {}

// IncUnknown implements Metrics.
func (NopMetrics) IncUnknown(string, string) {}
