// Package apierr はOpenAI互換のエラーレスポンス({ "error": {...} })を共通化する。
package apierr

import (
	"encoding/json"
	"net/http"
)

// Error はOpenAI互換エラーオブジェクトの本体。
type Error struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    any    `json:"code"`
}

type envelope struct {
	Error Error `json:"error"`
}

// Write はstatusとエラー内容をOpenAI互換JSONで書き出す。
func Write(w http.ResponseWriter, status int, message, typ string, code any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope{Error: Error{Message: message, Type: typ, Code: code}})
}
