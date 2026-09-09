// Package audit は監査ログの改ざん検知(ハッシュチェーン)を提供する。
// 各エントリのIDは「直前のID + エントリ内容」のSHA-256で、ログの連続性が崩れると
// チェーンが途切れるため、改ざん・欠落を検知できる(OWASP「Auditing」)。
package audit

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
)

// Chain は監査エントリの逐次ハッシュチェーン。
type Chain struct {
	mu   sync.Mutex
	prev string
}

// NewChain は新しいチェーンを開始する(セッションを一意にする乱数シード)。
func NewChain() (*Chain, error) {
	seed := make([]byte, 16)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	return &Chain{prev: hex.EncodeToString(seed)}, nil
}

// NewChainWithSeed は固定シードでチェーンを開始する(テスト用・決定論的に再現可能)。
func NewChainWithSeed(seed string) *Chain {
	return &Chain{prev: seed}
}

// Next はエントリをチェーンに追加し、そのエントリのID(16進)を返す。
// fieldsのキーをソートして決定論的にハッシュ化する。
func (c *Chain) Next(fields map[string]string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(c.prev)
	for _, k := range keys {
		b.WriteByte('|')
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(fields[k])
	}
	sum := sha256.Sum256([]byte(b.String()))
	c.prev = hex.EncodeToString(sum[:])
	return c.prev
}
