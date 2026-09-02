// Package handlers は/v1/chat/completionsと/v1/modelsのエンドポイント処理。
package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/trork-code/pi-llm-gateway/internal/apierr"
	"github.com/trork-code/pi-llm-gateway/internal/config"
	"github.com/trork-code/pi-llm-gateway/internal/providers"
	"github.com/trork-code/pi-llm-gateway/internal/sse"
)

const maxBodyBytes = 10 << 20 // 10MiB

type Handlers struct {
	Config   *config.Config
	Registry *providers.Registry
	Log      *slog.Logger
}

func New(cfg *config.Config, reg *providers.Registry, log *slog.Logger) *Handlers {
	return &Handlers{Config: cfg, Registry: reg, Log: log}
}

// ChatCompletions は POST /v1/chat/completions の本体。
func (h *Handlers) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		apierr.Write(w, http.StatusBadRequest,
			"リクエストボディの読み込みに失敗しました", "invalid_request_error", nil)
		return
	}

	var in struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || strings.TrimSpace(in.Model) == "" {
		apierr.Write(w, http.StatusBadRequest,
			"model は必須です(ボディはOpenAI形式のJSONである必要があります)", "invalid_request_error", nil)
		return
	}

	// model resolver: エイリアス → (provider名, 実モデル名)
	mc, ok := h.Config.Resolve(in.Model)
	if !ok {
		apierr.Write(w, http.StatusBadRequest,
			fmt.Sprintf("unknown model: %q", in.Model), "invalid_request_error", "model_not_found")
		return
	}

	// providers registry からadapterを取り出す
	ad, ok := h.Registry.Get(mc.Provider)
	if !ok {
		// config検証済みなので通常起きない
		apierr.Write(w, http.StatusInternalServerError,
			fmt.Sprintf("provider %q is not registered", mc.Provider), "api_error", nil)
		return
	}

	req := &providers.ChatRequest{
		Alias:         in.Model,
		ResolvedModel: mc.Model,
		Stream:        in.Stream,
		RawBody:       raw,
	}
	if in.Stream {
		h.streamChat(w, r, ad, req)
		return
	}

	// 非stream: レスポンスを最後まで受け取ってから1回で返す
	resp, err := ad.Chat(r.Context(), req)
	if err != nil {
		h.writeAdapterError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.Status)
	_, _ = w.Write(resp.Body)
}

// streamChat はchunkを受け取るたびに data: {...}\n\n として書き出し、最後に data: [DONE]\n\n を送る。
func (h *Handlers) streamChat(w http.ResponseWriter, r *http.Request, ad providers.Adapter, req *providers.ChatRequest) {
	chunks, err := ad.ChatStream(r.Context(), req)
	if err != nil {
		h.writeAdapterError(w, err)
		return
	}
	fl, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	for chunk := range chunks {
		if err := sse.WriteChunk(w, fl, chunk); err != nil {
			// 書き込み失敗=クライアント切断とみなす。r.Context()経由で上流へのリクエストも中断される
			h.Log.Info("chunkの書き込みに失敗しました(クライアント切断)", "error", err)
			return
		}
		if r.Context().Err() != nil {
			return
		}
	}
	if err := sse.WriteDone(w, fl); err != nil {
		h.Log.Info("完了シグナルの書き込みに失敗しました", "error", err)
	}
}

// writeAdapterError はadapter/上流エラーをOpenAI互換形でクライアントへ返す。
func (h *Handlers) writeAdapterError(w http.ResponseWriter, err error) {
	var herr *providers.HTTPError
	if errors.As(err, &herr) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(herr.Status)
		_, _ = w.Write(herr.Body)
		return
	}
	h.Log.Error("上流プロバイダー呼び出しに失敗しました", "error", err)
	apierr.Write(w, http.StatusBadGateway, "upstream provider request failed", "api_error", nil)
}

// Models は GET /v1/models — エイリアス一覧をOpenAI互換形式で返す。
func (h *Handlers) Models(w http.ResponseWriter, r *http.Request) {
	aliases := make([]string, 0, len(h.Config.Models))
	for alias := range h.Config.Models {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)

	now := time.Now().Unix()
	data := make([]modelEntry, 0, len(aliases))
	for _, alias := range aliases {
		data = append(data, modelEntry{
			ID:      alias,
			Object:  "model",
			Created: now,
			OwnedBy: h.Config.Models[alias].Provider,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(modelsResponse{Object: "list", Data: data})
}

type modelEntry struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type modelsResponse struct {
	Object string       `json:"object"`
	Data   []modelEntry `json:"data"`
}
