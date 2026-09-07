package auth

import (
	"path/filepath"
	"testing"
)

func TestPairAndRotate(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.RotateForTests("123456", "test-token-grok-bridge-mvp")
	ok, tok, msg := s.Pair("000000")
	if ok || tok != "" {
		t.Fatalf("expected fail, got ok=%v tok=%q msg=%q", ok, tok, msg)
	}
	ok, tok, msg = s.Pair("123456")
	if !ok || tok != "test-token-grok-bridge-mvp" {
		t.Fatalf("pair failed: ok=%v tok=%q msg=%q", ok, tok, msg)
	}
	if s.PairingCode() == "123456" {
		t.Fatal("pairing code should rotate after success")
	}
	if !s.CheckToken(tok) {
		t.Fatal("token check failed")
	}
	newTok := s.RotateToken()
	if newTok == tok || s.CheckToken(tok) {
		t.Fatal("old token should be invalid")
	}
	code := s.RotatePairingCode()
	if code == "" {
		t.Fatal("empty pairing code")
	}
	st := s.Status()
	if st["paired"] != true {
		t.Fatalf("status=%v", st)
	}
	// auth.json mode 0600
	info, err := filepath.Glob(filepath.Join(dir, "auth.json"))
	if err != nil || len(info) != 1 {
		t.Fatal(err)
	}
}

func TestRateLimiter(t *testing.T) {
	r := NewPairRateLimiter(3, 60)
	for i := 0; i < 3; i++ {
		if !r.Allow("1.2.3.4") {
			t.Fatalf("attempt %d should allow", i)
		}
	}
	if r.Allow("1.2.3.4") {
		t.Fatal("should rate limit")
	}
	if !r.Allow("9.9.9.9") {
		t.Fatal("other IP should allow")
	}
}
