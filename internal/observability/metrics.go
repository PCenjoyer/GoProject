package observability

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type Metrics struct {
	registry         *prometheus.Registry
	requests         *prometheus.CounterVec
	requestDuration  *prometheus.HistogramVec
	claims           prometheus.Counter
	claimErrors      prometheus.Counter
	deferred         prometheus.Counter
	inflight         prometheus.Gauge
	deliveries       *prometheus.CounterVec
	deliveryDuration *prometheus.HistogramVec
}

func NewMetrics() *Metrics {
	metrics := &Metrics{
		registry: prometheus.NewRegistry(),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "hookforge",
			Subsystem: "http",
			Name:      "requests_total",
			Help:      "Обработанные HTTP-запросы по методу, маршруту и статусу ответа.",
		}, []string{"method", "route", "status"}),
		requestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "hookforge",
			Subsystem: "http",
			Name:      "request_duration_seconds",
			Help:      "Длительность HTTP-запросов по методу и маршруту.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"method", "route"}),
		claims: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "hookforge",
			Subsystem: "delivery",
			Name:      "claims_total",
			Help:      "Доставки, полученные обработчиками.",
		}),
		claimErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "hookforge",
			Subsystem: "delivery",
			Name:      "claim_errors_total",
			Help:      "Ошибки базы данных при получении доставок.",
		}),
		deferred: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "hookforge",
			Subsystem: "delivery",
			Name:      "endpoint_saturation_total",
			Help:      "Задачи, отложенные из-за предела параллелизма точки назначения.",
		}),
		inflight: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "hookforge",
			Subsystem: "delivery",
			Name:      "inflight",
			Help:      "Выполняющиеся запросы вебхуков.",
		}),
		deliveries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "hookforge",
			Subsystem: "delivery",
			Name:      "attempts_total",
			Help:      "Завершённые попытки доставки по результату.",
		}, []string{"outcome"}),
		deliveryDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "hookforge",
			Subsystem: "delivery",
			Name:      "attempt_duration_seconds",
			Help:      "Длительность исходящих попыток доставки по результату.",
			Buckets:   []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		}, []string{"outcome"}),
	}
	metrics.registry.MustRegister(
		metrics.requests,
		metrics.requestDuration,
		metrics.claims,
		metrics.claimErrors,
		metrics.deferred,
		metrics.inflight,
		metrics.deliveries,
		metrics.deliveryDuration,
		prometheus.NewGoCollector(),
		prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}),
	)
	return metrics
}

func (m *Metrics) Registry() *prometheus.Registry {
	return m.registry
}

func (m *Metrics) ClaimSucceeded() {
	m.claims.Inc()
}

func (m *Metrics) ClaimFailed() {
	m.claimErrors.Inc()
}

func (m *Metrics) EndpointDeferred() {
	m.deferred.Inc()
}

func (m *Metrics) AttemptStarted() {
	m.inflight.Inc()
}

func (m *Metrics) AttemptFinished(outcome string, duration time.Duration) {
	m.inflight.Dec()
	m.deliveries.WithLabelValues(outcome).Inc()
	m.deliveryDuration.WithLabelValues(outcome).Observe(duration.Seconds())
}

func (m *Metrics) HTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		m.requests.WithLabelValues(r.Method, route, strconv.Itoa(recorder.status)).Inc()
		m.requestDuration.WithLabelValues(r.Method, route).Observe(time.Since(started).Seconds())
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusRecorder) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}
