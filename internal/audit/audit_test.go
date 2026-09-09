package audit

import "testing"

func TestChain_Deterministic(t *testing.T) {
	c := NewChainWithSeed("seed")
	id1 := c.Next(map[string]string{"action": "chat", "model": "m1"})
	id2 := c.Next(map[string]string{"action": "chat", "model": "m2"})
	if id1 == "" || id2 == "" {
		t.Fatal("エントリIDが空です")
	}
	if id1 == id2 {
		t.Fatal("異なるエントリのIDが同一です")
	}
	// 同じシードと同じ順序なら同じID列になる(検証側で再現可能)
	c2 := NewChainWithSeed("seed")
	if again := c2.Next(map[string]string{"action": "chat", "model": "m1"}); again != id1 {
		t.Fatalf("チェーンが再現できません: %s != %s", again, id1)
	}
}

func TestChain_FieldOrderIndependent(t *testing.T) {
	c1 := NewChainWithSeed("seed")
	a := c1.Next(map[string]string{"a": "1", "b": "2"})
	c2 := NewChainWithSeed("seed")
	b := c2.Next(map[string]string{"b": "2", "a": "1"})
	if a != b {
		t.Fatal("フィールドの順序が結果に影響しています(決定論的にハッシュ化されていません)")
	}
}

func TestChain_NilFields(t *testing.T) {
	c := NewChainWithSeed("seed")
	if id := c.Next(nil); id == "" {
		t.Fatal("nilフィールドでもIDが返るべき")
	}
}
