package main

import "testing"

func TestWireGuardOnly(t *testing.T) {
	for _, address := range []string{"10.77.0.2:8798", "10.77.0.1:9000"} {
		if err := wireGuardOnly(address); err != nil {
			t.Fatalf("expected %s to pass: %v", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:8798", "192.168.0.124:8798", "127.0.0.1:8798"} {
		if err := wireGuardOnly(address); err == nil {
			t.Fatalf("expected %s to be rejected", address)
		}
	}
}

func TestOBSAuthentication(t *testing.T) {
	got := obsAuthentication("password", "salt", "challenge")
	const want = "zTM5ki6L2vVvBQiTG9ckH1Lh64AbnCf6XZ226UmnkIA="
	if got != want {
		t.Fatalf("authentication mismatch: got %q want %q", got, want)
	}
}
