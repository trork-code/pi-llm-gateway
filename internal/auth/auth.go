// Package auth はリクエストヘッダのgatewayキー(Authorization: Bearer)を検証する。
// 複数のgatewayキーを許容する(将来のキーごとアクセス制御に向けた余地。アーキテクチャ仕様書§7)。
package auth

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/trork-code/pi-llm-gateway/internal/apierr"
)

// Middleware は一致するgatewayキーを持たないリクエストを401で止める。
// ここを通らないとhandlerには一切進めない。
func Middleware(gatewayKeys []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !matchAny(r.Header.Get("Authorization"), gatewayKeys) {
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
