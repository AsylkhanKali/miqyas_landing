// Package metrics — минимальный HTTP-middleware для счётчиков запросов.
// Использует expvar (стандартная библиотека) для экспорта в формате JSON на /metrics.
// Prometheus-совместимый формат добавляется через OTel collector (см. docker-compose.yml).
package metrics

import (
	"expvar"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

var (
	requestsTotal    = expvar.NewMap("http_requests_total")
	requestDuration  = expvar.NewMap("http_request_duration_ms_sum")
	requestsInFlight = expvar.NewInt("http_requests_in_flight")

	mu         sync.Mutex
	histograms = map[string][]float64{}
)

// Middleware собирает базовые HTTP-метрики: кол-во запросов, статус, latency.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &responseWriter{ResponseWriter: w, status: 200}
		start := time.Now()
		requestsInFlight.Add(1)

		next.ServeHTTP(rw, r)

		requestsInFlight.Add(-1)
		elapsed := time.Since(start).Milliseconds()
		key := fmt.Sprintf("%s %d", r.Method, rw.status)
		requestsTotal.Add(key, 1)
		requestDuration.Add(key, elapsed)

		mu.Lock()
		histograms[key] = append(histograms[key], float64(elapsed))
		mu.Unlock()
	})
}

// Handler возвращает /metrics endpoint (JSON из expvar + summary latencies).
// Возвращает http.HandlerFunc для совместимости с chi r.Get().
func Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		expvar.Do(func(kv expvar.KeyValue) {
			fmt.Fprintf(w, "%s=%s\n", kv.Key, kv.Value)
		})
		mu.Lock()
		defer mu.Unlock()
		for key, vals := range histograms {
			if len(vals) == 0 {
				continue
			}
			var sum float64
			for _, v := range vals {
				sum += v
			}
			avg := sum / float64(len(vals))
			fmt.Fprintf(w, "http_latency_avg_ms{%s}=%s\n", key, strconv.FormatFloat(avg, 'f', 2, 64))
		}
	}
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}
