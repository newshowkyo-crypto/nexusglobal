package httpx

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	HTTPRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests",
		},
		[]string{"method", "path", "status"},
	)

	HTTPRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "path"},
	)

	PayoutFreezeTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "payout_freeze_total",
			Help: "Total number of payout freezes",
		},
	)

	PayoutConfirmTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "payout_confirm_total",
			Help: "Total number of payout confirms",
		},
	)

	PayoutReleaseTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "payout_release_total",
			Help: "Total number of payout releases",
		},
	)

	MAERouteSelectedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mae_route_selected_total",
			Help: "Total number of MAE route selections",
		},
		[]string{"mae_id", "is_testing"},
	)

	OutboxPendingCount = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "outbox_pending_count",
			Help: "Number of pending outbox events",
		},
	)
)

func init() {
	prometheus.MustRegister(
		HTTPRequestsTotal,
		HTTPRequestDuration,
		PayoutFreezeTotal,
		PayoutConfirmTotal,
		PayoutReleaseTotal,
		MAERouteSelectedTotal,
		OutboxPendingCount,
	)
}

func MetricsHandler() http.Handler {
	return promhttp.Handler()
}
