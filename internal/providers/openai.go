package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/awnumar/memguard"
)

// OpenAIAdapter はOpenAI互換APIへの素通しアダプター。
// リクエストはmodel差し替えのみ、レスポンスはそのまま返す。
// OpenAI互換の他社API(Ollama Cloudなど)にも同じ仕組みを流用できる。
type OpenAIAdapter struct {
	name    string                 // registryキー。OpenAI互換の他プロバイダー(NewOpenAICompat)で差し替える
	BaseURL string                 // 例: https://api.openai.com/v1
	key     *memguard.LockedBuffer // 実APIキー(mlock保護・メモリ内暗号化・確実なパージ)
	HTTP    *http.Client
}

// NewOpenAI はOpenAI本体(openai)向けアダプターを作る。
func NewOpenAI(baseURL, apiKey string) *OpenAIAdapter {
	return NewOpenAICompat(ProviderOpenAI, baseURL, apiKey)
}

// NewOpenAICompat はOpenAI互換API(Ollama Cloudなど)向けに、任意のprovider名で
// 素通しアダプターを作る。
func NewOpenAICompat(name, baseURL, apiKey string) *OpenAIAdapter {
	return &OpenAIAdapter{name: name, BaseURL: baseURL, key: sealKey(apiKey), HTTP: guardedHTTPClient()}
}

func (a *OpenAIAdapter) Name() string {
	if a.name != "" {
		return a.name
	}
	return ProviderOpenAI
}

// sealKey は実APIキーをmemguard保護バッファに移す。
// コピー元の[]byteはwipeされる(元のstringはGC管理に戻るが起動時の一時的なコピーのみ)。
func sealKey(k string) *memguard.LockedBuffer {
	b := memguard.NewBufferFromBytes([]byte(k))
	b.Freeze()
	return b
}

// keyString は実APIキーを保護メモリから取り出して文字列として返す。
// net/httpのヘッダは文字列を要求するため、この瞬間だけ文字列化する(短命なコピー)。
func (a *OpenAIAdapter) keyString() string {
	if a.key == nil || !a.key.IsAlive() {
		return ""
	}
	return a.key.String()
}

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
	hreq.Header.Set("Authorization", "Bearer "+a.keyString())
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
