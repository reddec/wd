package wd

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type Metrics struct {
	requests      *prometheus.CounterVec
	payload       *prometheus.HistogramVec
	response      *prometheus.HistogramVec
	input         *prometheus.CounterVec
	output        *prometheus.CounterVec
	executionTime *prometheus.CounterVec
	timing        *prometheus.HistogramVec
	busyWorkers   prometheus.Gauge
}

func NewDefaultMetrics() *Metrics {
	return NewMetrics(prometheus.DefaultRegisterer)
}

func NewMetrics(registry prometheus.Registerer) *Metrics {
	factory := promauto.With(registry)
	return &Metrics{
		requests: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: "webhooks",
			Name:      "requests",
			Help:      "total requests number",
		}, []string{"path", "status"}),
		payload: factory.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "webhooks",
			Name:      "payload",
			Help:      "requests payload distribution",
			Buckets:   []float64{128, 256, 512, 1024, 2048, 4096, 8192, 16384, 32768, 65536},
		}, []string{"path"}),
		response: factory.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "webhooks",
			Name:      "response",
			Help:      "response payload distribution",
			Buckets:   []float64{128, 256, 512, 1024, 2048, 4096, 8192, 16384, 32768, 65536},
		}, []string{"path"}),
		input: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: "webhooks",
			Name:      "input",
			Help:      "total payload bytes in excluding headers",
		}, []string{"path"}),
		output: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: "webhooks",
			Name:      "output",
			Help:      "total response bytes out excluding headers",
		}, []string{"path"}),
		executionTime: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: "webhooks",
			Name:      "execution",
			Help:      "total seconds spent for processing",
		}, []string{"path"}),
		timing: factory.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "webhooks",
			Name:      "time",
			Help:      "execution time distribution",
			Buckets:   prometheus.DefBuckets,
		}, []string{"path"}),
		busyWorkers: factory.NewGauge(prometheus.GaugeOpts{
			Namespace: "webhooks",
			Name:      "busy_workers",
			Help:      "number of busy workers",
		}),
	}
}

func (m *Metrics) AddBusyWorker(inc int64) {
	if m == nil {
		return
	}
	m.busyWorkers.Add(float64(inc))
}

func (m *Metrics) countResult(req *http.Request, br *bufferedResponse, input *meteredStream) {
	if m == nil {
		return
	}
	duration := time.Since(br.created).Seconds()
	m.executionTime.WithLabelValues(req.URL.Path).Add(duration)
	m.timing.WithLabelValues(req.URL.Path).Observe(duration)
	m.output.WithLabelValues(req.URL.Path).Add(float64(br.sent))
	m.requests.WithLabelValues(req.URL.Path, strconv.Itoa(br.statusCode)).Inc()
	m.response.WithLabelValues(req.URL.Path).Observe(float64(br.sent))
	m.input.WithLabelValues(req.URL.Path).Add(float64(input.read))
	m.payload.WithLabelValues(req.URL.Path).Observe(float64(input.read))
}

func (m *Metrics) RecordForbidden(path string) {
	if m == nil {
		return
	}
	m.requests.WithLabelValues(path, "403").Inc()
}
