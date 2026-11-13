package protocol

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

func TestProofBindingAndReplay(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	p, err := Sign(key, "session-a", "gateway-a", now)
	if err != nil {
		t.Fatal(err)
	}
	used := false
	consume := func(string) bool { old := used; used = true; return old }
	if err := Verify(PublicKey(key), "session-a", "gateway-a", p, now, consume); err != nil {
		t.Fatal(err)
	}
	if err := Verify(PublicKey(key), "session-a", "gateway-a", p, now, consume); err == nil {
		t.Fatal("replay accepted")
	}
	if err := Verify(PublicKey(key), "session-a", "gateway-b", p, now, nil); err == nil {
		t.Fatal("changed audience accepted")
	}
	if err := Verify(PublicKey(key), "session-b", "gateway-a", p, now, nil); err == nil {
		t.Fatal("changed session accepted")
	}
	if err := Verify(PublicKey(key), "session-a", "gateway-a", p, now.Add(ClockSkew+time.Second), nil); err == nil {
		t.Fatal("expired signature accepted")
	}
}
