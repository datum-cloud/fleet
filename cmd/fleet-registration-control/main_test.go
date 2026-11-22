package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"go.datum.net/fleet/internal/registration"
)

func TestOfflineControlSigner(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	request := registration.ControlRequest{Action: "expand", ID: "change", Reason: "synthetic", RootUID: "root", ResourceVersion: "9", Epoch: 2, Authority: "milo", Projects: []string{"provider"}}
	encoded, _ := json.Marshal(request)
	directory := t.TempDir()
	requestPath, keyPath := filepath.Join(directory, "request"), filepath.Join(directory, "private")
	if err := os.WriteFile(requestPath, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := sign(requestPath, keyPath, &output); err != nil {
		t.Fatal(err)
	}
	actual, err := registration.ParseControl(output.Bytes())
	if err != nil || !reflect.DeepEqual(actual, registration.SignControl(request, key)) {
		t.Fatal("signer changed request", err)
	}
	if err := os.WriteFile(keyPath, []byte("not a private key"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := sign(requestPath, keyPath, &output); err == nil {
		t.Fatal("invalid key accepted")
	}
}
