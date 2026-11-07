package metric

import (
	"time"

	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/thomaspoignant/go-feature-flag/audience"
)

const (
	audienceListLabel    = "list"
	audienceResultLabel  = "result"
	audienceLayoutLabel  = "layout"
	audienceOutcomeLabel = "outcome"
	audienceReasonLabel  = "reason"
)

var _ audience.Metrics = (*AudienceMetrics)(nil)

// AudienceMetrics is the Prometheus implementation of audience.Metrics.
// Labels carry list names, never member ids.
type AudienceMetrics struct {
	lookups  *prom.CounterVec
	duration *prom.HistogramVec
	cache    *prom.CounterVec
	unknown  *prom.CounterVec
}

func newAudienceMetrics() *AudienceMetrics {
	return &AudienceMetrics{
		lookups: prom.NewCounterVec(prom.CounterOpts{
			Name:      "audience_lookups_total",
			Help:      "Audience membership lookups by list and result (member, not_member, unknown).",
			Subsystem: GOFFSubSystem,
		}, []string{audienceListLabel, audienceResultLabel}),
		duration: prom.NewHistogramVec(prom.HistogramOpts{
			Name:      "audience_lookup_duration_seconds",
			Help:      "Duration of the audience store calls by list and layout.",
			Subsystem: GOFFSubSystem,
			Buckets:   []float64{.0005, .001, .0025, .005, .01, .025, .05, .1, .25},
		}, []string{audienceListLabel, audienceLayoutLabel}),
		cache: prom.NewCounterVec(prom.CounterOpts{
			Name:      "audience_cache_total",
			Help:      "Audience lookup cache outcomes by list (hit, miss).",
			Subsystem: GOFFSubSystem,
		}, []string{audienceListLabel, audienceOutcomeLabel}),
		unknown: prom.NewCounterVec(prom.CounterOpts{
			Name:      "audience_unknown_total",
			Help:      "Audience terms that could not be answered by list and reason.",
			Subsystem: GOFFSubSystem,
		}, []string{audienceListLabel, audienceReasonLabel}),
	}
}

func (m *AudienceMetrics) collectors() []prom.Collector {
	return []prom.Collector{m.lookups, m.duration, m.cache, m.unknown}
}

// IncLookup implements audience.Metrics.
func (m *AudienceMetrics) IncLookup(list, result string) {
	m.lookups.WithLabelValues(list, result).Inc()
}

// ObserveLookupDuration implements audience.Metrics.
func (m *AudienceMetrics) ObserveLookupDuration(list, layout string, d time.Duration) {
	m.duration.WithLabelValues(list, layout).Observe(d.Seconds())
}

// IncCache implements audience.Metrics.
func (m *AudienceMetrics) IncCache(list, outcome string) {
	m.cache.WithLabelValues(list, outcome).Inc()
}

// IncUnknown implements audience.Metrics.
func (m *AudienceMetrics) IncUnknown(list, reason string) {
	m.unknown.WithLabelValues(list, reason).Inc()
}

// AudienceMetrics returns the audience metrics, to give to the audience configuration.
// It returns nil when the Metrics were not built with NewMetrics.
func (m *Metrics) AudienceMetrics() audience.Metrics {
	if m.audience == nil {
		return nil
	}
	return m.audience
}
