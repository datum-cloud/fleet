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

func TestReadEnrollmentRequiresCompleteTargets(t *testing.T) {
	dir := t.TempDir()
	complete := filepath.Join(dir, "complete.json")
	if err := os.WriteFile(complete, []byte(`{"fleet-provider/server-example-001":{"bmcEndpoint":"https://[2001:db8::1]","systemUUID":"00000000-0000-4000-8000-000000000001","bootMACAddress":"02:00:00:00:00:01","bmcPublicKeySHA256":"`+strings.Repeat("a", 64)+`","disks":["disk-0"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if enrollment, err := readEnrollment(complete); err != nil || len(enrollment) != 1 {
		t.Fatalf("complete enrollment = %v, %v", enrollment, err)
	}
	for name, body := range map[string]string{
		"no-namespace": `{"server-example-001":{"bmcEndpoint":"e","systemUUID":"u","bmcPublicKeySHA256":"k","disks":["disk-0"]}}`,
		"no-key":       `{"fleet-provider/server-example-001":{"bmcEndpoint":"e","systemUUID":"u","disks":["disk-0"]}}`,
		"no-disks":     `{"fleet-provider/server-example-001":{"bmcEndpoint":"e","systemUUID":"u","bmcPublicKeySHA256":"k"}}`,
		"unknown":      `{"fleet-provider/server-example-001":{"bmcEndpoint":"e","systemUUID":"u","bmcPublicKeySHA256":"k","disks":["disk-0"],"extra":true}}`,
	} {
		path := filepath.Join(dir, name+".json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readEnrollment(path); err == nil {
			t.Errorf("%s: enrollment accepted", name)
		}
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
