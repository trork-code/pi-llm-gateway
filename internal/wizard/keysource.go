package wizard

import (
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"strings"
)

// ResolveAPIKeySource は --api-key / 対話入力の値を実際のキー文字列に解決する。
// 形式: "env:NAME" / "file:PATH" / "-"(stdin) / それ以外は値そのもの。
func ResolveAPIKeySource(spec string, stdin io.Reader) (string, error) {
	spec = strings.TrimSpace(spec)
	switch {
	case spec == "":
		return "", fmt.Errorf("API key が空です")
	case spec == "-":
		b, err := io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("stdinからキーを読めません: %w", err)
		}
		k := strings.TrimSpace(string(b))
		if k == "" {
			return "", fmt.Errorf("stdinから読んだ API key が空です")
		}
		return k, nil
	case strings.HasPrefix(spec, "env:"):
		name := strings.TrimPrefix(spec, "env:")
		if name == "" {
			return "", fmt.Errorf("env: の後に環境変数名が必要です")
		}
		v := os.Getenv(name)
		if v == "" {
			return "", fmt.Errorf("環境変数 %s が未設定です", name)
		}
		return strings.TrimSpace(v), nil
	case strings.HasPrefix(spec, "file:"):
		path := strings.TrimPrefix(spec, "file:")
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("ファイルからキーを読めません(%s): %w", path, err)
		}
		k := strings.TrimSpace(string(b))
		if k == "" {
			return "", fmt.Errorf("%s は空です", path)
		}
		return k, nil
	default:
		return spec, nil
	}
}

// GenerateGatewayKey は gk- + 128bit ランダムの gatewayキーを生成する。
// `gateway keys add` と同じ形式。
func GenerateGatewayKey() (string, error) {
	var key [16]byte
	if _, err := rand.Read(key[:]); err != nil {
		return "", fmt.Errorf("乱数生成に失敗: %w", err)
	}
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, len(key)*2)
	for _, b := range key {
		out = append(out, hexdigits[b>>4], hexdigits[b&0xf])
	}
	return "gk-" + string(out), nil
}

// Source はAPIキーの入力経路(サマリ表示用ラベル)。
func Source(spec string) string {
	switch {
	case strings.HasPrefix(spec, "env:"), strings.HasPrefix(spec, "file:"), spec == "-":
		return spec
	default:
		return "pasted value"
	}
}
