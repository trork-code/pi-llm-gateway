// 認証失敗のレート制限: gatewayキーの総当たりを阻止する。
// IPごとに失敗回数を記録し、しきい値を超えたら429で拒否する。
package auth

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// failureTracker はクライアントIPごとの認証失敗履歴。
type failureTracker struct {
	mu       sync.Mutex
	failures map[string][]time.Time
	max      int
	window   time.Duration
}

func newFailureTracker(max int, window time.Duration) *failureTracker {
	return &failureTracker{failures: map[string][]time.Time{}, max: max, window: window}
}

// record は失敗を1件記録する(ウィンドウ外の古い記録を整理)。
func (f *failureTracker) record(ip string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-f.window)
	var keep []time.Time
	for _, t := range f.failures[ip] {
		if t.After(cutoff) {
			keep = append(keep, t)
		}
	}
	keep = append(keep, now)
	f.failures[ip] = keep
	// マップの肥大化対策(攻撃元IPが大量にある場合)
	if len(f.failures) > 10000 {
		for k, ts := range f.failures {
			if len(ts) == 0 || ts[len(ts)-1].Before(cutoff) {
				delete(f.failures, k)
			}
		}
	}
}

// count は直近のウィンドウ内の失敗回数を返す。
func (f *failureTracker) count(ip string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	cutoff := time.Now().Add(-f.window)
	n := 0
	for _, t := range f.failures[ip] {
		if t.After(cutoff) {
			n++
		}
	}
	return n
}

// clientIP はリクエスト元IPを取り出す(chi RealIPがRemoteAddrを書き換えるため、ここを参照)。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
