package config

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestIdentityHashesExactLoadedTOMLBytes(t *testing.T) {
	firstBytes := []byte("[log]\nlevel = \"info\"\n")
	secondBytes := []byte("[log]\nlevel=\"info\"\n")
	first := loadIdentityConfig(t, firstBytes)
	second := loadIdentityConfig(t, secondBytes)

	firstIdentity, err := first.Identity()
	if err != nil {
		t.Fatalf("first Identity: %v", err)
	}
	secondIdentity, err := second.Identity()
	if err != nil {
		t.Fatalf("second Identity: %v", err)
	}
	if firstIdentity != hashIdentityBytes(firstBytes) {
		t.Fatalf("first Identity = %q, want exact-byte hash", firstIdentity)
	}
	if secondIdentity != hashIdentityBytes(secondBytes) {
		t.Fatalf("second Identity = %q, want exact-byte hash", secondIdentity)
	}
	if firstIdentity == secondIdentity {
		t.Fatal("different TOML bytes produced the same identity")
	}
}

func TestIdentityUsesStableStructuralFallback(t *testing.T) {
	first := &Config{
		Log:   Log{Level: "info"},
		Paths: Paths{ConversationsDir: "/tmp/conversations"},
	}
	second := &Config{
		Log:   Log{Level: "info"},
		Paths: Paths{ConversationsDir: "/tmp/conversations"},
	}

	firstIdentity, err := first.Identity()
	if err != nil {
		t.Fatalf("first Identity: %v", err)
	}
	secondIdentity, err := second.Identity()
	if err != nil {
		t.Fatalf("second Identity: %v", err)
	}
	if firstIdentity != secondIdentity {
		t.Fatalf("equivalent configs have identities %q and %q", firstIdentity, secondIdentity)
	}
	second.Log.Level = "warn"
	changedIdentity, err := second.Identity()
	if err != nil {
		t.Fatalf("changed Identity: %v", err)
	}
	if changedIdentity == firstIdentity {
		t.Fatal("structurally different configs produced the same identity")
	}
}

func loadIdentityConfig(t *testing.T, body []byte) *Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	value, err := LoadExisting(path)
	if err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}
	return value
}

func hashIdentityBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}
