package metric

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
)

func TestMetrics_IncAllFlag(t *testing.T) {
	metricSrv, err := NewMetrics()
	assert.NoError(t, err)

	metricSrv.IncAllFlag()
	metricSrv.IncAllFlag()
	metricSrv.IncAllFlag()

	assert.Equal(t, 3.0, testutil.ToFloat64(metricSrv.allFlagCounter))
}

func TestMetrics_IncAllFlag_WithBulkMetricEnabled(t *testing.T) {
	metricSrv, err := NewMetrics(MetricsOpts{
		EnableBulkMetricFlagNames: true,
	})
	assert.NoError(t, err)
	metricSrv.IncAllFlag("test-flag")
	metricSrv.IncAllFlag("test-flag")
	metricSrv.IncAllFlag("test-flag", "test-flag2")

	assert.Equal(
		t,
		3.0,
		testutil.ToFloat64(metricSrv.allFlagCounterWithFlag.WithLabelValues("test-flag")),
	)
	assert.Equal(
		t,
		1.0,
		testutil.ToFloat64(metricSrv.allFlagCounterWithFlag.WithLabelValues("test-flag2")),
	)
	assert.Equal(t, 3.0, testutil.ToFloat64(metricSrv.allFlagCounter))
}

func TestMetrics_IncCollectEvalData(t *testing.T) {
	metricSrv, err := NewMetrics()
	assert.NoError(t, err)

	metricSrv.IncCollectEvalData(123)
	metricSrv.IncCollectEvalData(2)

	assert.Equal(t, 125.0, testutil.ToFloat64(metricSrv.collectEvalDataCounter))
}

func TestMetrics_IncFlagEvaluation(t *testing.T) {
	metricSrv, err := NewMetrics()
	assert.NoError(t, err)

	metricSrv.IncFlagEvaluation("test-flag")
	metricSrv.IncFlagEvaluation("test-flag")
	metricSrv.IncFlagEvaluation("test-flag2")

	assert.Equal(
		t,
		2.0,
		testutil.ToFloat64(metricSrv.flagEvaluationCounter.WithLabelValues("test-flag")),
	)
	assert.Equal(
		t,
		1.0,
		testutil.ToFloat64(metricSrv.flagEvaluationCounter.WithLabelValues("test-flag2")),
	)
}

func TestMetrics_IncFlagNotFoundEvaluation(t *testing.T) {
	metricSrv, err := NewMetrics()
	assert.NoError(t, err)

	metricSrv.IncFlagNotFoundEvaluation()
	metricSrv.IncFlagNotFoundEvaluation()

	// all the flags that are not found are counted in the same time series
	assert.Equal(t, 1, testutil.CollectAndCount(metricSrv.flagEvaluationCounter))
	assert.Equal(
		t,
		2.0,
		testutil.ToFloat64(metricSrv.flagEvaluationCounter.WithLabelValues(FlagNotFoundLabelValue)),
	)
}

func TestMetrics_IncFlagCreated(t *testing.T) {
	metricSrv, err := NewMetrics()
	assert.NoError(t, err)

	metricSrv.IncFlagCreated("test-flag")
	metricSrv.IncFlagCreated("test-flag2")

	assert.Equal(t, 2.0, testutil.ToFloat64(metricSrv.flagCreateCounter))
	assert.Equal(
		t,
		1.0,
		testutil.ToFloat64(metricSrv.flagCreateCounterVec.WithLabelValues("test-flag2")),
	)
	assert.Equal(
		t,
		1.0,
		testutil.ToFloat64(metricSrv.flagCreateCounterVec.WithLabelValues("test-flag")),
	)
}

func TestMetrics_IncFlagUpdated(t *testing.T) {
	metricSrv, err := NewMetrics()
	assert.NoError(t, err)

	metricSrv.IncFlagUpdated("test-flag")
	metricSrv.IncFlagUpdated("test-flag2")

	assert.Equal(t, 2.0, testutil.ToFloat64(metricSrv.flagUpdateCounter))
	assert.Equal(
		t,
		1.0,
		testutil.ToFloat64(metricSrv.flagUpdateCounterVec.WithLabelValues("test-flag2")),
	)
	assert.Equal(
		t,
		1.0,
		testutil.ToFloat64(metricSrv.flagUpdateCounterVec.WithLabelValues("test-flag")),
	)
}

func TestMetrics_IncFlagDeleted(t *testing.T) {
	metricSrv, err := NewMetrics()
	assert.NoError(t, err)

	metricSrv.IncFlagDeleted("test-flag")
	metricSrv.IncFlagDeleted("test-flag2")

	assert.Equal(t, 2.0, testutil.ToFloat64(metricSrv.flagDeleteCounter))
	assert.Equal(
		t,
		1.0,
		testutil.ToFloat64(metricSrv.flagDeleteCounterVec.WithLabelValues("test-flag2")),
	)
	assert.Equal(
		t,
		1.0,
		testutil.ToFloat64(metricSrv.flagDeleteCounterVec.WithLabelValues("test-flag")),
	)
}

func TestMetrics_IncFlagChange(t *testing.T) {
	metricSrv, err := NewMetrics()
	assert.NoError(t, err)

	metricSrv.IncFlagChange()
	metricSrv.IncFlagChange()
	metricSrv.IncFlagChange()

	assert.Equal(t, 3.0, testutil.ToFloat64(metricSrv.flagChange))
}

func TestMetrics_IncForceRefresh(t *testing.T) {
	metricSrv, err := NewMetrics()
	assert.NoError(t, err)

	metricSrv.IncForceRefresh()
	metricSrv.IncForceRefresh()
	metricSrv.IncForceRefresh()

	assert.Equal(t, 3.0, testutil.ToFloat64(metricSrv.forceRefreshCounter))
}

func TestMetrics_IncFlagConfigurationCall(t *testing.T) {
	metricSrv, err := NewMetrics()
	assert.NoError(t, err)

	metricSrv.IncFlagConfigurationCall()
	metricSrv.IncFlagConfigurationCall()
	metricSrv.IncFlagConfigurationCall()

	assert.Equal(t, 3.0, testutil.ToFloat64(metricSrv.flagConfigurationCounter))
}

func TestMetrics_IncGetManifestCall(t *testing.T) {
	metricSrv, err := NewMetrics()
	assert.NoError(t, err)

	metricSrv.IncGetManifestCall()
	metricSrv.IncGetManifestCall()
	metricSrv.IncGetManifestCall()
	assert.Equal(t, 3.0, testutil.ToFloat64(metricSrv.getManifestCounter))
}

func TestAudienceMetrics(t *testing.T) {
	m, err := NewMetrics()
	assert.NoError(t, err)
	am := m.AudienceMetrics()
	assert.NotNil(t, am)

	am.IncLookup("beta", "member")
	am.IncLookup("beta", "member")
	am.IncCache("beta", "hit")
	am.IncUnknown("beta", "timeout")
	am.ObserveLookupDuration("beta", "set", 3*time.Millisecond)

	mfs, err := m.Registry.Gather()
	assert.NoError(t, err)
	got := map[string]int{}
	for _, mf := range mfs {
		got[mf.GetName()] = len(mf.GetMetric())
	}
	for _, name := range []string{
		"gofeatureflag_audience_lookups_total",
		"gofeatureflag_audience_lookup_duration_seconds",
		"gofeatureflag_audience_cache_total",
		"gofeatureflag_audience_unknown_total",
	} {
		assert.Equal(t, 1, got[name], name)
	}
	assert.Equal(t, 2.0, testutil.ToFloat64(m.audience.lookups.WithLabelValues("beta", "member")))

	// the zero value has no audience metrics
	var zero Metrics
	assert.Nil(t, zero.AudienceMetrics())
}
