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
