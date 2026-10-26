package secret

import (
	"strings"
	"testing"
)

func TestSealOpen(t *testing.T) {
	b, _ := New("master")
	enc, err := b.Seal("hello")
	if err != nil || !strings.HasPrefix(enc, "v1:") || strings.Contains(enc, "hello") {
		t.Fatal(enc, err)
	}
	if v, err := b.Open(enc); err != nil || v != "hello" {
		t.Fatal(v, err)
	}
	other, _ := New("other")
	if _, err := other.Open(enc); err == nil {
		t.Fatal("wrong key must fail")
	}
	k, p := NewAPIKey("agent")
	if !strings.HasPrefix(k, p) || len(k) < 40 || Hash(k) == k {
		t.Fatal(k, p)
	}
	if id := ID("box"); !strings.HasPrefix(id, "box_") || len(id) != 16 {
		t.Fatal(id)
	}
}
