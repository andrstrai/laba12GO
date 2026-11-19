package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// resetState обнуляет глобальное хранилище и счётчик ID.
func resetState() {
	store.mu.Lock()
	store.items = make(map[int]Item)
	store.mu.Unlock()
	nextID.Store(0)
}

// TestGetItemsWithDelay — проверяем, что delay меньше таймаута не мешает.
func TestGetItemsWithDelay(t *testing.T) {
	resetState()
	req := httptest.NewRequest(http.MethodGet, "/items?delay=50ms", nil)
	rec := httptest.NewRecorder()
	handleGetItems(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

// TestHandlerRespectsCanceledContext — обработчик должен сразу выйти,
// если контекст уже отменён.
func TestHandlerRespectsCanceledContext(t *testing.T) {
	resetState()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // отменяем немедленно

	req := httptest.NewRequest(http.MethodGet, "/items?delay=5s", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	start := time.Now()
	handleGetItems(rec, req)
	elapsed := time.Since(start)

	if elapsed > 500*time.Millisecond {
		t.Fatalf("handler should return immediately on canceled ctx, took %v", elapsed)
	}
}

// TestTimeoutHandler — http.TimeoutHandler возвращает 503 при превышении лимита.
func TestTimeoutHandler(t *testing.T) {
	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
			w.Write([]byte("done"))
		case <-r.Context().Done():
			return
		}
	})
	h := http.TimeoutHandler(slow, 200*time.Millisecond, `{"error":"timeout"}`)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

// TestRaceConcurrentRequests — главный тест для go test -race:
// параллельно пишем (POST), читаем список (GET /items) и читаем по id.
func TestRaceConcurrentRequests(t *testing.T) {
	resetState()
	srv := httptest.NewServer(newHandler())
	defer srv.Close()

	const writers = 30
	const readers = 30
	const ops = 20

	var wg sync.WaitGroup
	wg.Add(writers + readers)

	// Писатели
	for i := 0; i < writers; i++ {
		go func(id int) {
			defer wg.Done()
			client := srv.Client()
			for j := 0; j < ops; j++ {
				body, _ := json.Marshal(Item{
					Name:  fmt.Sprintf("w%d-%d", id, j),
					Price: float64(j),
				})
				resp, err := client.Post(
					srv.URL+"/items",
					"application/json",
					bytes.NewReader(body),
				)
				if err != nil {
					t.Errorf("POST err: %v", err)
					return
				}
				resp.Body.Close()
			}
		}(i)
	}

	// Читатели
	for i := 0; i < readers; i++ {
		go func(id int) {
			defer wg.Done()
			client := srv.Client()
			for j := 0; j < ops; j++ {
				resp, err := client.Get(srv.URL + "/items")
				if err != nil {
					t.Errorf("GET /items err: %v", err)
					return
				}
				resp.Body.Close()

				resp, err = client.Get(fmt.Sprintf("%s/items/%d", srv.URL, (id*ops+j)%50+1))
				if err != nil {
					t.Errorf("GET /items/{id} err: %v", err)
					return
				}
				resp.Body.Close()
			}
		}(i)
	}

	wg.Wait()

	store.mu.RLock()
	got := len(store.items)
	store.mu.RUnlock()

	want := writers * ops
	if got != want {
		t.Fatalf("expected %d items, got %d", want, got)
	}
}

// TestRaceConcurrentDelete — параллельные DELETE + GET.
func TestRaceConcurrentDelete(t *testing.T) {
	resetState()

	// Наполняем хранилище 100 элементами
	store.mu.Lock()
	for i := 1; i <= 100; i++ {
		store.items[i] = Item{ID: i, Name: fmt.Sprintf("item-%d", i), Price: float64(i)}
	}
	store.mu.Unlock()
	nextID.Store(100)

	srv := httptest.NewServer(newHandler())
	defer srv.Close()

	var wg sync.WaitGroup
	wg.Add(200)

	for i := 1; i <= 100; i++ {
		// Удаляем конкретный id
		go func(id int) {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/items/%d", srv.URL, id), nil)
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Errorf("DELETE err: %v", err)
				return
			}
			resp.Body.Close()
		}(i)

		// Параллельно читаем список
		go func() {
			defer wg.Done()
			resp, err := srv.Client().Get(srv.URL + "/items")
			if err != nil {
				t.Errorf("GET err: %v", err)
				return
			}
			resp.Body.Close()
		}()
	}

	wg.Wait()

	store.mu.RLock()
	got := len(store.items)
	store.mu.RUnlock()
	if got != 0 {
		t.Fatalf("expected 0 items after delete, got %d", got)
	}
}
