// Package metrics - метрики сервиса для Prometheus.
//
// Метрики объявлены на уровне пакета: так их не нужно протаскивать через
// конструкторы всех слоев, а тесты работают без дополнительной настройки.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const namespace = "message_service"

// Registry - собственный реестр вместо глобального prometheus.DefaultRegisterer:
// в выдачу попадают только наши метрики и стандартные метрики Go-рантайма
var Registry = prometheus.NewRegistry()

var (
	HTTPRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "http_requests_total",
		Help:      "HTTP requests by route template, method and status code.",
	}, []string{"method", "route", "status"})

	HTTPDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "http_request_duration_seconds",
		Help:      "HTTP request latency by route template. WebSocket upgrades are excluded.",
		Buckets:   []float64{.002, .005, .01, .025, .05, .1, .25, .5, 1, 2.5},
	}, []string{"method", "route"})

	WSConnections = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "ws_connections",
		Help:      "Open WebSocket connections on this instance.",
	})

	WSUsers = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "ws_users",
		Help:      "Distinct users with at least one WebSocket connection on this instance.",
	})

	WSCommands = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "ws_commands_total",
		Help:      "WebSocket commands from clients by type and result.",
	}, []string{"type", "result"})

	WSSlowClients = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "ws_slow_client_disconnects_total",
		Help:      "Connections dropped because the client could not keep up with its send buffer.",
	})

	FramesDelivered = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "ws_frames_delivered_total",
		Help:      "Event frames queued to WebSocket connections on this instance.",
	})

	MessagesSent = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "messages_sent_total",
		Help:      "Messages stored, by type. Deduplicated retries are counted separately.",
	}, []string{"type"})

	MessagesDeduplicated = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "messages_deduplicated_total",
		Help:      "Send retries answered with an already stored message (same client_msg_id).",
	})

	EventsPublished = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "events_published_total",
		Help:      "Events published to Redis by type, kind (durable/ephemeral) and result.",
	}, []string{"type", "kind", "result"})

	EventsReplayed = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "events_replayed_total",
		Help:      "Events sent to reconnecting clients from the stream.",
	})

	ReplayResyncs = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "replay_resyncs_total",
		Help:      "Reconnects that could not be replayed and require a full resync.",
	})

	RateLimited = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "rate_limited_total",
		Help:      "Requests rejected by the rate limiter, by scope.",
	}, []string{"scope"})
)

func init() {
	Registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		HTTPRequests, HTTPDuration,
		WSConnections, WSUsers, WSCommands, WSSlowClients, FramesDelivered,
		MessagesSent, MessagesDeduplicated,
		EventsPublished, EventsReplayed, ReplayResyncs,
		RateLimited,
	)
}

// Handler отдает метрики в формате Prometheus
func Handler() http.Handler {
	return promhttp.HandlerFor(Registry, promhttp.HandlerOpts{Registry: Registry})
}

// Result - метка результата операции
func Result(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}
