package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync/atomic"
	"time"
)

// Счётчик запросов и время запуска сервера
var (
	requestCount atomic.Int64 // сколько запросов обработал сервер (атомарный, без гонок)
	startTime    = time.Now() // когда запустили программу, нужно для uptime
)

// statusRecorder оборачивает http.ResponseWriter и запоминает код ответа,
// потому что сам ResponseWriter не умеет сказать, какой статус уже отправлен
type statusRecorder struct {
	http.ResponseWriter     // встраивание: все методы оригинала доступны как свои
	status              int // код ответа, который отправил обработчик
}

// WriteHeader перехватывает вызов обработчика: запоминаем код и передаём дальше
func (rec *statusRecorder) WriteHeader(code int) {
	rec.status = code
	rec.ResponseWriter.WriteHeader(code)
}

// countRequests увеличивает счётчик ДО того, как запрос попадёт в обработчик
func countRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1) // атомарно +1, безопасно из многих горутин
		next.ServeHTTP(w, r)
	})
}

// logRequests пишет в лог метод, путь, статус и длительность каждого запроса
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// По умолчанию 200: если обработчик не вызвал WriteHeader, Go сам отправит 200
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r) // обработчик пишет ответ в нашу обёртку

		log.Printf("%s %s -> %d (%v)", r.Method, r.URL.Path, rec.status, time.Since(start))
	})
}

// statsResponse описывает JSON-ответ эндпоинта /stats
type statsResponse struct {
	Requests int64  `json:"requests"`
	Uptime   string `json:"uptime"`
}

// handleStats отдаёт число обработанных запросов и время работы сервера
func handleStats(w http.ResponseWriter, r *http.Request) {
	stats := statsResponse{
		Requests: requestCount.Load(), // атомарное чтение
		Uptime:   time.Since(startTime).Round(time.Second).String(),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(stats)
}
