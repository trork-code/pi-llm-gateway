// Middleware: gatewayキーの検証(認証境界)。
package auth

import (
	"crypto/subtle"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/trork-code/pi-llm-gateway/internal/apierr"
)

// Middleware は一致するgatewayキーを持たないリクエストを401で止める。
// 認証失敗のレート制限は既定値(IPあたり1分に20回失敗)で有効。
// ここを通らないとhandlerには一切進めない。
func Middleware(gatewayKeys func() []string) func(http.Handler) http.Handler {
	return MiddlewareConfig(gatewayKeys, 20, time.Minute)
}

// MiddlewareConfig は認証失敗レート制限のしきい値を指定できる版。
// maxFailuresが0以下の場合はレート制限を無効化する。
// カウンタはウィンドウ(TTL)でのみ減衰する。しきい値に達すると該当IPからの
// リクエストは有効なキーでも429になる(Kong等のbrute force protectionと同じ仕様)。
func MiddlewareConfig(gatewayKeys func() []string, maxFailures int, window time.Duration) func(http.Handler) http.Handler {
	tracker := newFailureTracker(maxFailures, window)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientIP(r)
			if maxFailures > 0 && tracker.count(ip) >= maxFailures {
				w.Header().Set("Retry-After", strconv.Itoa(int(window.Seconds())))
				apierr.Write(w, http.StatusTooManyRequests,
					"認証失敗が多すぎます。しばらく待ってから再試行してください",
					"rate_limit_error", "rate_limit_exceeded")
				return
			}
			if !matchAny(r.Header.Get("Authorization"), gatewayKeys()) {
				if maxFailures > 0 {
					tracker.record(ip)
				}
				apierr.Write(w, http.StatusUnauthorized,
					"認証に失敗しました(gatewayキーが不正です)", "authentication_error", "invalid_api_key")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func matchAny(header string, want []string) bool {
	const prefix = "Bearer "
	if len(want) == 0 || !strings.HasPrefix(header, prefix) {
		return false
	}
	got := strings.TrimSpace(header[len(prefix):])
	if got == "" {
		return false
	}
	// タイミング攻撃対策でconstant-time比較を使う(キーをログには決して出さない)
	for _, key := range want {
		if subtle.ConstantTimeCompare([]byte(got), []byte(key)) == 1 {
			return true
		}
	}
	return false
}
