// テスト用のログ抑制ハンドラ(全体で使い回す)。
package handlers

import (
	"io"
	"log/slog"
)

func newDiscardHandler() slog.Handler {
	return slog.NewTextHandler(io.Discard, nil)
}
