package main

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"go.datum.net/fleet/internal/registration"
)

func main() {
	requestPath := flag.String("request", "", "unsigned ControlRequest JSON, exact observed root UID/resourceVersion/epoch and scope")
	keyPath := flag.String("key", "", "offline Ed25519 PKCS8 private key; never mount in the service")
	flag.Parse()
	if err := sign(*requestPath, *keyPath, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func sign(requestPath, keyPath string, output io.Writer) error {
	encoded, err := os.ReadFile(requestPath)
	if err != nil {
		return err
	}
	if len(encoded) > 8192 {
		return errors.New("request exceeds budget")
	}
	var request registration.ControlRequest
	if err := json.Unmarshal(encoded, &request); err != nil {
		return err
	}
	encoded, err = os.ReadFile(keyPath)
	if err != nil {
		return err
	}
	block, rest := pem.Decode(encoded)
	if block == nil || len(rest) != 0 {
		return errors.New("expected one PKCS8 private key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return err
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return errors.New("control key must be Ed25519")
	}
	return json.NewEncoder(output).Encode(registration.SignControl(request, key))
}
