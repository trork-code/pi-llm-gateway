// 監査ログ(アーキテクチャ仕様書§9)のミドルウェア。
package handlers

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/trork-code/pi-llm-gateway/internal/audit"
)

// RequestLogger は監査方針に沿ったアクセスログを出すミドルウェア。
// ログに出すのは method / path / status / 所要時間 / request_id のみ。
// **リクエスト内容(メッセージ・プロンプト)は絶対にログに出さない**(多層防御④)。
// 各エントリにはハッシュチェーンのID(audit_id)を付けて改ざん検知を可能にする。
func RequestLogger(log *slog.Logger, chain *audit.Chain) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			var auditID string
			var chainFields map[string]string
			if chain != nil {
				chainFields = map[string]string{
					"method":      r.Method,
					"path":        r.URL.Path,
					"status":      strconv.Itoa(sw.status),
					"duration_ms": strconv.FormatInt(time.Since(start).Milliseconds(), 10),
					"request_id":  chimw.GetReqID(r.Context()),
				}
				auditID = chain.Next(chainFields)
			}
			log.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", sw.status,
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", chimw.GetReqID(r.Context()),
				"audit_id", auditID,
			)
		})
	}
}

// statusWriter は書き込まれたステータスコードを記録し、Flusherも透過する。
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Flush はストリーミング(SSE)のために下位のFlusherへ透過する。
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
