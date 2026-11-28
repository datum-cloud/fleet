package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"k8s.io/client-go/rest"
)

// clientCertDir writes a cert-manager style Secret directory, tls.crt and
// tls.key, holding a client certificate for commonName signed by ca.
func clientCertDir(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, commonName string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: commonName},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, block := range map[string]*pem.Block{"tls.crt": {Type: "CERTIFICATE", Bytes: der}, "tls.key": {Type: "EC PRIVATE KEY", Bytes: keyDER}} {
		if err := os.WriteFile(filepath.Join(dir, name), pem.EncodeToMemory(block), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestEachComponentAuthenticatesToMiloWithItsOwnClientCertificate builds
// the coordinator's connection from the manager's: the same endpoint and
// server CA, but only the coordinator's client certificate, and none of the
// manager's credentials.
func TestEachComponentAuthenticatesToMiloWithItsOwnClientCertificate(t *testing.T) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "synthetic-client-ca"}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	type seen struct {
		commonName    string
		authorization string
	}
	requests := make(chan seen, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := seen{authorization: r.Header.Get("Authorization")}
		if len(r.TLS.PeerCertificates) == 1 {
			got.commonName = r.TLS.PeerCertificates[0].Subject.CommonName
		}
		requests <- got
	}))
	server.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool}
	server.StartTLS()
	defer server.Close()
	serverCA := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})

	manager := clientCertDir(t, ca, caKey, "fleet-manager")
	provider := &rest.Config{Host: server.URL, BearerToken: "manager-token", TLSClientConfig: rest.TLSClientConfig{
		CAData: serverCA, CertFile: filepath.Join(manager, "tls.crt"), KeyFile: filepath.Join(manager, "tls.key")}}
	config, err := withClientCert(provider, clientCertDir(t, ca, caKey, "custody-coordinator"))
	if err != nil {
		t.Fatal(err)
	}
	client, err := rest.HTTPClientFor(config)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(config.Host + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if got := <-requests; got.commonName != "custody-coordinator" || got.authorization != "" {
		t.Fatalf("Milo saw client certificate %q and authorization %q, want only the coordinator's certificate", got.commonName, got.authorization)
	}
	if _, err := withClientCert(provider, t.TempDir()); err == nil {
		t.Fatal("a directory without a client certificate was accepted")
	}
}
