package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// OpenAIAdapter はOpenAI互換APIへの素通しアダプター。
// リクエストはmodel差し替えのみ、レスポンスはそのまま返す。
type OpenAIAdapter struct {
	BaseURL string // 例: https://api.openai.com/v1
	APIKey  string
	HTTP    *http.Client
}

func NewOpenAI(baseURL, apiKey string) *OpenAIAdapter {
	return &OpenAIAdapter{BaseURL: baseURL, APIKey: apiKey, HTTP: &http.Client{}}
}

func (a *OpenAIAdapter) Name() string { return ProviderOpenAI }

// rewriteBody はRawBodyのmodelを実モデル名へ差し替える。
func (a *OpenAIAdapter) rewriteBody(req *ChatRequest, forceStream bool) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(req.RawBody, &m); err != nil {
		return nil, err
	}
	m["model"] = req.ResolvedModel
	if forceStream {
		m["stream"] = true
	}
	return json.Marshal(m)
}

func (a *OpenAIAdapter) do(ctx context.Context, req *ChatRequest, forceStream bool) (*http.Response, error) {
	body, err := a.rewriteBody(req, forceStream)
	if err != nil {
		return nil, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Authorization", "Bearer "+a.APIKey)
	return a.HTTP.Do(hreq)
}

// Chat は非stream。応答はOpenAI互換なのでそのまま返す。
func (a *OpenAIAdapter) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	resp, err := a.do(ctx, req, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := readAllLimit(resp.Body, maxUpstreamBody)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, &HTTPError{Status: resp.StatusCode, Body: body}
	}
	return &ChatResponse{Status: resp.StatusCode, Body: body}, nil
}

// ChatStream は上流のSSE行("data: {...}")をそのままOpenAI互換chunkとして流す。
// contextがキャンセルされると(=クライアントが切断されると)上流の読み取りも中断する。
func (a *OpenAIAdapter) ChatStream(ctx context.Context, req *ChatRequest) (<-chan []byte, error) {
	resp, err := a.do(ctx, req, true)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		body, _ := readAllLimit(resp.Body, maxUpstreamBody)
		resp.Body.Close()
		return nil, &HTTPError{Status: resp.StatusCode, Body: body}
	}

	ch := make(chan []byte)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), maxUpstreamBody)
		for sc.Scan() {
			payload, ok := strings.CutPrefix(sc.Text(), "data:")
			if !ok {
				continue // 空行やコメント行は無視
			}
			payload = strings.TrimSpace(payload)
			if payload == "" {
				continue
			}
			if payload == "[DONE]" {
				return
			}
			select {
			case ch <- []byte(payload):
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}
