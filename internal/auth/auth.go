// Package auth はリクエストヘッダのgatewayキー(Authorization: Bearer)を検証する。
package auth

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/trork-code/pi-llm-gateway/internal/apierr"
)

// Middleware は一致しないキーのリクエストを401で止める。
// ここを通らないとhandlerには一切進めない。
func Middleware(gatewayKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !match(r.Header.Get("Authorization"), gatewayKey) {
				apierr.Write(w, http.StatusUnauthorized,
					"認証に失敗しました(gatewayキーが不正です)", "authentication_error", "invalid_api_key")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func match(header, want string) bool {
	const prefix = "Bearer "
	if want == "" || !strings.HasPrefix(header, prefix) {
		return false
	}
	got := strings.TrimSpace(header[len(prefix):])
	if got == "" {
		return false
	}
	// タイミング攻撃対策でconstant-time比較を使う
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
