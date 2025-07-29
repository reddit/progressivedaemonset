package metrics

import (
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	podsWithGates     *prometheus.GaugeVec
	podsUngated       *prometheus.CounterVec
	rolloutInterval   *prometheus.GaugeVec
	timeRemaining     *prometheus.GaugeVec
	ActiveRollouts    prometheus.Gauge
	completedRollouts prometheus.Counter
	logger            *zap.SugaredLogger
}

func NewMetrics(logger *zap.SugaredLogger, registry *prometheus.Registry) *Metrics {
	m := &Metrics{
		podsWithGates: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "progressive_rollout_pods_with_gates",
				Help: "Number of pods remaining with scheduling gates per DaemonSet",
			},
			[]string{"namespace", "daemonset"},
		),
		podsUngated: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "progressive_rollout_pods_ungated",
				Help: "Number of pods that have had their scheduling gates removed per DaemonSet",
			},
			[]string{"namespace", "daemonset"},
		),
		rolloutInterval: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "progressive_rollout_interval_seconds",
				Help: "Configured rollout interval in seconds per DaemonSet",
			},
			[]string{"namespace", "daemonset"},
		),
		timeRemaining: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "progressive_rollout_time_remaining_seconds",
				Help: "Estimated time remaining in seconds for DaemonSet rollout",
			},
			[]string{"namespace", "daemonset"},
		),
		ActiveRollouts: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "progressive_rollout_active_rollouts",
				Help: "Total number of active DaemonSet rollouts",
			},
		),
		completedRollouts: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "progressive_rollout_completed_total",
				Help: "Total number of completed progressive rollouts",
			},
		),
		logger: logger,
	}

	registry.MustRegister(m.podsWithGates)
	registry.MustRegister(m.podsUngated)
	registry.MustRegister(m.rolloutInterval)
	registry.MustRegister(m.timeRemaining)
	registry.MustRegister(m.completedRollouts)
	registry.MustRegister(m.ActiveRollouts)

	return m
}

func (m *Metrics) StartServer(port string, registry *prometheus.Registry) {
	go func() {
		m.logger.Infof("Starting metrics server on :%s", port)
		http.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
		if err := http.ListenAndServe(":"+port, nil); err != nil {
			m.logger.Fatalf("Failed to start metrics server: %v", err)
		}
	}()
}

// UpdateDaemonSetMetrics updates metrics for a DaemonSet
func (m *Metrics) UpdateDaemonSetMetrics(dsKey string, podsRemaining int, interval time.Duration) {
	namespace, name := splitKey(dsKey)
	if namespace == "" || name == "" {
		m.logger.Warnf("Invalid DaemonSet key: %s", dsKey)
		return
	}
	m.podsWithGates.WithLabelValues(namespace, name).Set(float64(podsRemaining))
	m.podsUngated.WithLabelValues(namespace, name).Inc()
	m.rolloutInterval.WithLabelValues(namespace, name).Set(interval.Seconds())

	// Calculate remaining time based on pods left and interval
	remainingTime := time.Duration(podsRemaining) * interval
	m.timeRemaining.WithLabelValues(namespace, name).Set(remainingTime.Seconds())
}

// RemoveDaemonSetMetrics removes metrics for a completed DaemonSet rollout
func (m *Metrics) RemoveDaemonSetMetrics(dsKey string) {
	namespace, name := splitKey(dsKey)
	if namespace == "" || name == "" {
		m.logger.Warnf("Invalid DaemonSet key: %s", dsKey)
		return
	}
	m.podsWithGates.DeleteLabelValues(namespace, name)
	m.podsUngated.DeleteLabelValues(namespace, name)
	m.rolloutInterval.DeleteLabelValues(namespace, name)
	m.timeRemaining.DeleteLabelValues(namespace, name)
	m.completedRollouts.Inc()
}

func splitKey(key string) (string, string) {
	parts := strings.Split(key, "/")
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", key
}
