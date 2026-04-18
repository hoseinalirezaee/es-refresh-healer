package metrics

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	ScanTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "healer_scan_total",
			Help: "Number of full ExternalSecret scans.",
		},
		[]string{"result"},
	)

	ExternalSecretsSeenTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "healer_es_seen_total",
			Help: "Number of ExternalSecret resources evaluated.",
		},
		[]string{"source"},
	)

	ExternalSecretsStaleTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "healer_es_stale_total",
			Help: "Number of stale ExternalSecret resources detected.",
		},
		[]string{"reason"},
	)

	ExternalSecretsPatchedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "healer_es_patched_total",
			Help: "Number of stale ExternalSecret resources nudged.",
		},
		[]string{"dry_run"},
	)

	ExternalSecretPatchErrorsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "healer_es_patch_errors_total",
			Help: "Number of ExternalSecret patch failures.",
		},
		[]string{"reason"},
	)

	ExternalSecretRefreshLagSeconds = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "healer_es_refresh_lag_seconds",
			Help:    "Observed ExternalSecret refresh lag in seconds.",
			Buckets: prometheus.ExponentialBuckets(30, 2, 12),
		},
	)

	ExternalSecretRateLimitedTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "healer_es_rate_limited_total",
			Help: "Number of stale ExternalSecret patch attempts skipped by the global rate limit.",
		},
	)

	ExternalSecretCooldownSkippedTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "healer_es_cooldown_skipped_total",
			Help: "Number of stale ExternalSecret patch attempts skipped by cooldown.",
		},
	)
)

func ObserveLag(lag time.Duration) {
	if lag >= 0 {
		ExternalSecretRefreshLagSeconds.Observe(lag.Seconds())
	}
}

func BoolLabel(value bool) string {
	return strconv.FormatBool(value)
}
