package wizard

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// NotifyReload は稼働中gatewayへ POST /admin/reload を通知する。
// addr が空なら GATEWAY_ADDR env > http://127.0.0.1:<PORT|8080>。
func NotifyReload(addr, gatewayKey string) error {
	if addr == "" {
		addr = os.Getenv("GATEWAY_ADDR")
	}
	if addr == "" {
		port := envOrStr("PORT", "8080")
		addr = "http://127.0.0.1:" + port
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(addr, "/")+"/admin/reload", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+gatewayKey)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("reload通知が届きません(%s): %w", addr, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("reload通知が失敗しました(%s → %d %s)", addr, resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	return nil
}

func envOrStr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
