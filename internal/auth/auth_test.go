package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFailureTracker(t *testing.T) {
	tr := newFailureTracker(3, time.Minute)
	tr.record("1.2.3.4")
	tr.record("1.2.3.4")
	if got := tr.count("1.2.3.4"); got != 2 {
		t.Fatalf("count = %d, want 2", got)
	}
	if got := tr.count("5.6.7.8"); got != 0 {
		t.Fatalf("count = %d, want 0", got)
	}
}

func TestMiddleware_RateLimit(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := MiddlewareConfig(func() []string { return []string{"k"} }, 2, time.Minute)(next)

	wrong := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	wrong.Header.Set("Authorization", "Bearer wrong")

	// 2回は401
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, wrong)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i+1, rec.Code)
		}
	}
	// 3回目はレート制限で429
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, wrong)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("Retry-Afterヘッダがありません")
	}
	// 正しいキーでもレート制限中は429
	good := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	good.Header.Set("Authorization", "Bearer k")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, good)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("blocked with valid key: status = %d, want 429", rec.Code)
	}
}

func TestMiddleware_RateLimitDisabled(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := MiddlewareConfig(func() []string { return []string{"k"} }, 0, time.Minute)(next)

	wrong := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	wrong.Header.Set("Authorization", "Bearer wrong")
	// maxFailures=0ならレート制限なし(何度失敗しても401)
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, wrong)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i+1, rec.Code)
		}
	}
}
