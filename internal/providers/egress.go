// 上流egress制御: provider adapterが接続できる先を、設定済みbase_urlのドメインに限定する。
// config改ざん・設定ドリフト・DNS乗っ取り等で実APIキーが意図しないサーバーに
// 送信されるのを防ぐ(鍵の持ち出し経路の遮断)。
package providers

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
)

var (
	egressMu      sync.RWMutex
	egressAllow   map[string]struct{} // 許可ホスト(小文字、ポートなし)
	egressEnabled bool
)

// SetEgressAllowlist は上流接続を許可するホスト一覧を設定する(ドメイン許可リスト)。
// 以後、provider adapterが作るhttp.Clientの上流接続はこの一覧のホストに限定される。
// hostsが空の場合はegress制御を無効化する。
func SetEgressAllowlist(hosts []string) error {
	m := make(map[string]struct{}, len(hosts))
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			return errors.New("egress allowlistに空のホストがあります")
		}
		m[h] = struct{}{}
	}
	egressMu.Lock()
	defer egressMu.Unlock()
	egressAllow = m
	egressEnabled = len(m) > 0
	return nil
}

// guardedHTTPClient はegress制御が有効ならガード付きTransportを持つクライアントを、
// 無効なら既定のクライアントを返す。adapterの構築時に呼ばれる。
func guardedHTTPClient() *http.Client {
	egressMu.RLock()
	defer egressMu.RUnlock()
	if !egressEnabled {
		return &http.Client{}
	}
	allow := make(map[string]struct{}, len(egressAllow))
	for k, v := range egressAllow {
		allow[k] = v
	}
	d := &guardedDialer{allow: allow}
	return &http.Client{Transport: &http.Transport{DialContext: d.DialContext}}
}

// guardedDialer はダイヤル先のホストが許可リストに含まれるかを検証する。
// http.Clientがリダイレクトを辿る場合も毎回この検証を通る。
type guardedDialer struct {
	allow map[string]struct{}
}

func (g *guardedDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("egress: 不正なアドレス %q: %w", addr, err)
	}
	host = strings.ToLower(host)
	if _, ok := g.allow[host]; !ok {
		return nil, fmt.Errorf("egress: 許可リストにない上流ホスト %q への接続を拒否しました", host)
	}
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}
