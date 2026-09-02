// Package sse はストリーミング配信(SSE)の共通処理。
// どのプロバイダーでも同じ書き方でchunkを送れるようにする。
package sse

import (
	"fmt"
	"io"
	"net/http"
)

// WriteChunk はOpenAI互換chunkのpayload(JSON)を data: {...}\n\n として書き、フラッシュする。
func WriteChunk(w io.Writer, fl http.Flusher, payload []byte) error {
	if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
		return err
	}
	if fl != nil {
		fl.Flush()
	}
	return nil
}

// WriteDone は終端の data: [DONE]\n\n を書き、フラッシュする。
func WriteDone(w io.Writer, fl http.Flusher) error {
	if _, err := io.WriteString(w, "data: [DONE]\n\n"); err != nil {
		return err
	}
	if fl != nil {
		fl.Flush()
	}
	return nil
}
