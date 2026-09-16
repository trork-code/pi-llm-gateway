package sse

import (
	"bytes"
	"errors"
	"testing"
)

// fakeFlusher はFlush呼び出しを記録するテスト用Fluser。
type fakeFlusher struct {
	calls int
}

func (f *fakeFlusher) Flush() { f.calls++ }

// errWriter はWriteが必ず失敗するライター(下流切断など)。
type errWriter struct{}

func (errWriter) Write(p []byte) (int, error) { return 0, errors.New("write failed") }

func TestWriteChunk_Format(t *testing.T) {
	var buf bytes.Buffer
	fl := &fakeFlusher{}

	payload := []byte(`{"id":"chatcmpl-1","choices":[{"delta":{"content":"Hi"}}]}`)
	if err := WriteChunk(&buf, fl, payload); err != nil {
		t.Fatalf("WriteChunk() error = %v", err)
	}

	want := "data: " + string(payload) + "\n\n"
	if got := buf.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if fl.calls != 1 {
		t.Errorf("Flush() called %d times, want 1", fl.calls)
	}
}

func TestWriteChunk_MultibytePayload(t *testing.T) {
	// マルチバイト文字やエスケープ済みJSONがそのまま1チャンクとして出ること。
	var buf bytes.Buffer
	payload := []byte(`{"choices":[{"delta":{"content":"こんにちは"}}]}`)
	if err := WriteChunk(&buf, nil, payload); err != nil {
		t.Fatalf("WriteChunk() error = %v", err)
	}
	want := "data: " + string(payload) + "\n\n"
	if got := buf.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestWriteChunk_NilFlusherDoesNotPanic(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteChunk(&buf, nil, []byte(`{}`)); err != nil {
		t.Fatalf("WriteChunk() error = %v", err)
	}
	if got := buf.String(); got != "data: {}\n\n" {
		t.Errorf("body = %q, want %q", got, "data: {}\n\n")
	}
}

func TestWriteChunk_WriteError(t *testing.T) {
	fl := &fakeFlusher{}
	err := WriteChunk(errWriter{}, fl, []byte(`{}`))
	if err == nil {
		t.Fatal("WriteChunk() error = nil, want error")
	}
	if fl.calls != 0 {
		t.Errorf("Flush() called %d times on write failure, want 0", fl.calls)
	}
}

func TestWriteDone_Format(t *testing.T) {
	var buf bytes.Buffer
	fl := &fakeFlusher{}

	if err := WriteDone(&buf, fl); err != nil {
		t.Fatalf("WriteDone() error = %v", err)
	}
	if got, want := buf.String(), "data: [DONE]\n\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if fl.calls != 1 {
		t.Errorf("Flush() called %d times, want 1", fl.calls)
	}
}

func TestWriteDone_WriteError(t *testing.T) {
	if err := WriteDone(errWriter{}, nil); err == nil {
		t.Fatal("WriteDone() error = nil, want error")
	}
}
