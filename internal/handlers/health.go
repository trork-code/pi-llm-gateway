// /healthz — 認証不要の稼働確認エンドポイント。
// gatewayキーを知らないツール(pi起動前の確認、gateway status等)でも
// 稼働・構成の状態だけを確認できるようにする。鍵そのものは含めない。
package handlers

import (
	"encoding/json"
	"github.com/trork-code/pi-llm-gateway/internal/config"
	"net/http"
	"os"
	"sort"
	"time"
)

// healthPayload は/healthzの応答(非秘匿のみ)。
type healthPayload struct {
	Status        string                      `json:"status"`
	UptimeSeconds int                         `json:"uptime_seconds"`
	PID           int                         `json:"pid"`
	DefaultModel  string                      `json:"default_model"`
	Models        map[string]modelHealthEntry `json:"models"`
	Providers     []string                    `json:"providers"`
	GatewayKeys   int                         `json:"gateway_keys"`
	Recipients    int                         `json:"identity_recipients"`
	// Dormant はドーマント常駐(design-distribution.md §8)での鍵未化状態(§docs/design-batch3.md)。
	Dormant bool `json:"dormant"`
}

// modelHealthEntry はエイリアス1つ分の非秘匿概要。
type modelHealthEntry struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// Health は GET /healthz の本体(認証不要)。
// Runtimeがnil(dormant・鍵未復号)のときは構成無しで状態のみを返す(§8: healthzはwarmトリガ外)。
func (h *Handlers) Health(w http.ResponseWriter, _ *http.Request) {
	resp := healthPayload{
		Status:        "ok",
		UptimeSeconds: int(time.Since(h.started).Seconds()),
		PID:           os.Getpid(),
		Models:        make(map[string]modelHealthEntry, 0),
	}
	if rt := h.rt.Load(); rt != nil {
		resp.DefaultModel = rt.Config.DefaultModel
		resp.Providers = sortedProviderNames(rt.Config)
		resp.Models = make(map[string]modelHealthEntry, len(rt.Config.Models))
		for alias, mc := range rt.Config.Models {
			resp.Models[alias] = modelHealthEntry{Provider: mc.Provider, Model: mc.Model}
		}
		resp.GatewayKeys = len(rt.Secrets.AllGatewayKeys())
		resp.Recipients = len(rt.Secrets.IdentityRecipients)
	} else {
		resp.Dormant = true
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(resp)
}

func sortedProviderNames(cfg *config.Config) []string {
	names := make([]string, 0, len(cfg.Providers))
	for n := range cfg.Providers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
