package main

import (
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadToken(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "valid")
	if err := os.WriteFile(valid, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if token, err := readToken(valid); err != nil || token != "secret" {
		t.Fatalf("read valid token: token=%q err=%v", token, err)
	}

	for name, content := range map[string]string{
		"empty":     "",
		"multiline": "first\nsecond",
		"oversized": strings.Repeat("x", maxTokenFileBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readToken(path); err == nil {
				t.Fatal("want invalid token file to fail")
			}
		})
	}
	if _, err := readToken(""); err == nil {
		t.Fatal("want missing token path to fail")
	}
}

func TestRuntimeAndAdminTokensMustDiffer(t *testing.T) {
	if err := validateDistinctTokens("runtime", "admin"); err != nil {
		t.Fatalf("distinct tokens rejected: %v", err)
	}
	if err := validateDistinctTokens("same", "same"); err == nil {
		t.Fatal("equal runtime and admin tokens were accepted")
	}
}

func TestNewServerHasFiniteResourceBounds(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := newServer("127.0.0.1:0", http.NewServeMux(), logger)
	for name, value := range map[string]time.Duration{
		"read header": server.ReadHeaderTimeout,
		"read":        server.ReadTimeout,
		"write":       server.WriteTimeout,
		"idle":        server.IdleTimeout,
	} {
		if value <= 0 {
			t.Errorf("%s timeout is not bounded", name)
		}
	}
	if server.MaxHeaderBytes <= 0 {
		t.Fatal("headers are not bounded")
	}
}
