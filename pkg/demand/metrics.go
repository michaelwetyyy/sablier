package demand

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics exposes demand-bridge configuration and source state without
// revealing source credentials or endpoint URLs.
type Metrics struct {
	registry          *prometheus.Registry
	targetInfo        *prometheus.GaugeVec
	targetIdleAfter   *prometheus.GaugeVec
	sourceActive      *prometheus.GaugeVec
	sourceCheckOK     *prometheus.GaugeVec
	sourceLastChecked *prometheus.GaugeVec
}

func NewMetrics(conf Config) *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		targetInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "sablier_demand_target_info",
			Help: "Configured demand target. Always 1 while the target is present in demand configuration.",
		}, []string{"source", "source_type", "target_mode", "target", "failure_policy"}),
		targetIdleAfter: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "sablier_demand_target_idle_after_seconds",
			Help: "Configured idle duration before a demand target may expire.",
		}, []string{"source", "source_type", "target_mode", "target"}),
		sourceActive: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "sablier_demand_source_active",
			Help: "Effective demand state after applying the configured failure policy (1 active, 0 idle).",
		}, []string{"source", "source_type"}),
		sourceCheckOK: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "sablier_demand_source_check_success",
			Help: "Whether the most recent demand-source check succeeded (1 success, 0 failure).",
		}, []string{"source", "source_type"}),
		sourceLastChecked: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "sablier_demand_source_last_check_timestamp_seconds",
			Help: "Unix timestamp of the most recent demand-source check.",
		}, []string{"source", "source_type"}),
	}
	m.registry.MustRegister(m.targetInfo, m.targetIdleAfter, m.sourceActive, m.sourceCheckOK, m.sourceLastChecked)

	for _, source := range conf.Sources {
		mode := "group"
		targets := []string{source.Target.Group}
		if len(source.Target.Names) > 0 {
			mode = "name"
			targets = source.Target.Names
		}
		for _, target := range targets {
			m.targetInfo.WithLabelValues(source.Name, source.Type, mode, target, source.FailurePolicy).Set(1)
			m.targetIdleAfter.WithLabelValues(source.Name, source.Type, mode, target).Set(source.IdleAfter.Seconds())
		}
	}
	return m
}

func (m *Metrics) Handler() http.Handler {
	if m == nil {
		return http.NotFoundHandler()
	}
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

func (m *Metrics) ObserveSource(source SourceConfig, active bool, checkErr error) {
	if m == nil {
		return
	}
	labels := []string{source.Name, source.Type}
	if active {
		m.sourceActive.WithLabelValues(labels...).Set(1)
	} else {
		m.sourceActive.WithLabelValues(labels...).Set(0)
	}
	if checkErr == nil {
		m.sourceCheckOK.WithLabelValues(labels...).Set(1)
	} else {
		m.sourceCheckOK.WithLabelValues(labels...).Set(0)
	}
	m.sourceLastChecked.WithLabelValues(labels...).Set(float64(time.Now().Unix()))
}
