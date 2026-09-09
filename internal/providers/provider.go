// Package providers はプロバイダーごとのアダプター(変換ロジック)を集約する。
// 新しいプロバイダーを足すときは、このパッケージに1ファイル追加するだけ。
package providers

import (
	"context"
	"fmt"
	"io"
	"sync"
)

const (
	ProviderOpenAI      = "openai"
	ProviderAnthropic   = "anthropic"
	ProviderOllamaCloud = "ollamacloud"
	ProviderOpenRouter  = "openrouter"
	ProviderGroq        = "groq"
	ProviderNVIDIA      = "nvidia"
)

// maxUpstreamBody は上流通信で読み取る上限(32MiB)。
const maxUpstreamBody = 32 << 20

// ChatRequest はhandlerからadapterへ渡すリクエスト。
type ChatRequest struct {
	Alias         string // Piが送ってきたエイリアス(例: pi-default)
	ResolvedModel string // 実モデル名(例: claude-sonnet-4-6)
	Stream        bool
	RawBody       []byte // 元のリクエストボディ(JSON)
}

// ChatResponse は非stream時の応答(OpenAI互換JSON)。
type ChatResponse struct {
	Status int
	Body   []byte
}

// HTTPError は上流のステータス/エラーボディ(OpenAI互換JSON)をそのまま伝える。
type HTTPError struct {
	Status int
	Body   []byte
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("provider error: status=%d body=%s", e.Status, string(e.Body))
}

// Adapter は各プロバイダーが実装すべきインターフェース。
// やることは共通: OpenAI形式を受け取り、そのプロバイダー向けに送り、結果をOpenAI形式で返す。
type Adapter interface {
	// Name はregistryのキー(models.yamlのprovider名)。
	Name() string
	// Chat は非streamのリクエストを処理する。
	Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
	// ChatStream はstreamのリクエストを処理し、OpenAI互換chunkのpayload(JSON)を流すチャネルを返す。
	// チャネルは完了時に閉じる。"data: [DONE]"はhandler側が送るためここには含めない。
	ChatStream(ctx context.Context, req *ChatRequest) (<-chan []byte, error)
}

var (
	_ Adapter = (*OpenAIAdapter)(nil)
	_ Adapter = (*AnthropicAdapter)(nil)
)

// Registry はprovider名 → adapter。
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]Adapter
}

func NewRegistry() *Registry {
	return &Registry{adapters: map[string]Adapter{}}
}

func (r *Registry) Register(a Adapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[a.Name()] = a
}

func (r *Registry) Get(name string) (Adapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[name]
	return a, ok
}

// readAllLimit はrから最大limitバイト読み取る。
func readAllLimit(r io.Reader, limit int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, limit))
}
