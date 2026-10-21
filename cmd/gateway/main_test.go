package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestEnvVarsDocumented はコードで使う環境変数がREADMEの一覧に載っていることを担保する。
// ドキュメントの腐敗(実装とREADMEの乖離)をCIで検知するためのテスト。
func TestEnvVarsDocumented(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	// os.Getenv("X") / envOr("X", ...) / envInt("X", ...) の3パターンを収集する
	re := regexp.MustCompile(`(?:os\.Getenv|envOr|envInt)\("([A-Z][A-Z0-9_]+)"`)
	matches := re.FindAllStringSubmatch(string(src), -1)
	seen := map[string]bool{}
	for _, m := range matches {
		seen[m[1]] = true
	}
	if len(seen) == 0 {
		t.Fatal("環境変数の検出に失敗しました(テスト自体の不備)")
	}

	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	for env := range seen {
		if !strings.Contains(string(readme), "`"+env+"`") {
			t.Errorf("環境変数 %s がREADMEの環境変数一覧にありません(追加するかREADMEを更新してください)", env)
		}
	}
}

// TestIsInitFlag は裸フラグ省略形の振り分け規則。
func TestIsInitFlag(t *testing.T) {
	if !isInitFlag([]string{"--port", "18099"}) {
		t.Fatal("--port は init 省略形")
	}
	if !isInitFlag([]string{"-provider", "x", "--api-key", "env:Y", "-y"}) {
		t.Fatal("provider/api-key/y は init 省略形")
	}
	if !isInitFlag([]string{"--print-json", "--no-start"}) {
		t.Fatal("print-json/no-start は init 省略形")
	}
	if isInitFlag([]string{"-check"}) {
		t.Fatal("-check は既存経路(checkモード)に残す")
	}
	if isInitFlag([]string{"-v"}) {
		t.Fatal("-v はversionケースで処理済み")
	}
	if isInitFlag([]string{"-unknown-flag"}) {
		t.Fatal("未知フラグはinit省略形とみなさない")
	}
}
