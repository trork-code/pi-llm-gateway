package apierr

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWrite_StatusAndContentType(t *testing.T) {
	rec := httptest.NewRecorder()

	Write(rec, http.StatusUnauthorized, "missing key", "invalid_request_error", "invalid_api_key")

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestWrite_EnvelopeShape(t *testing.T) {
	rec := httptest.NewRecorder()

	Write(rec, http.StatusBadRequest, "bad request", "invalid_request_error", "bad_json")

	var env struct {
		Error Error `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("response is not valid JSON: %v\nbody: %s", err, rec.Body.String())
	}
	if env.Error.Message != "bad request" {
		t.Errorf("message = %q, want %q", env.Error.Message, "bad request")
	}
	if env.Error.Type != "invalid_request_error" {
		t.Errorf("type = %q, want %q", env.Error.Type, "invalid_request_error")
	}
	if env.Error.Code != "bad_json" {
		t.Errorf("code = %v, want %q", env.Error.Code, "bad_json")
	}
}

func TestWrite_CodeVariants(t *testing.T) {
	// code は any 型: 文字列/数値/null のいずれも透過すること。
	cases := []struct {
		name string
		code any
		want any
	}{
		{"string", "insufficient_quota", "insufficient_quota"},
		{"number", 401, float64(401)},
		{"null", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			Write(rec, http.StatusForbidden, "denied", "invalid_request_error", tc.code)

			var raw struct {
				Error struct {
					Code any `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
			if raw.Error.Code != tc.want {
				t.Errorf("code = %v (%T), want %v", raw.Error.Code, raw.Error.Code, tc.want)
			}
		})
	}
}

func TestWrite_ErrorMessageEscaped(t *testing.T) {
	// メッセージにJSON特殊文字が含まれても壊れないこと(上流エラーの透過で発生しうる)。
	rec := httptest.NewRecorder()
	msg := `line1 "quoted" \ line2` + "\n" + "line3"

	Write(rec, http.StatusInternalServerError, msg, "server_error", nil)

	var env struct {
		Error Error `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if env.Error.Message != msg {
		t.Errorf("message = %q, want %q", env.Error.Message, msg)
	}
	if strings.Contains(rec.Body.String(), "\n\"message\"") {
		t.Error("raw newline leaked outside of JSON escaping")
	}
}
