package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.datum.net/fleet/internal/registration"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestWebhookRequiresTrustedAPIServerClient(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issuer := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "synthetic-api-server-ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	ca, err := x509.CreateCertificate(rand.Reader, issuer, issuer, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "synthetic-api-server"}, NotBefore: issuer.NotBefore, NotAfter: issuer.NotAfter, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	certificate, err := x509.CreateCertificate(rand.Reader, leaf, issuer, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "client-ca.crt")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca}), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := webhookTLS(path)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", registration.MetricsHandler())
	mux.HandleFunc("/", func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) })
	server := httptest.NewUnstartedServer(mux)
	server.TLS = config
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	if response, err := server.Client().Get(server.URL); err == nil {
		response.Body.Close()
		t.Fatal("unauthenticated AdmissionReview transport was accepted")
	}
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.Certificates = []tls.Certificate{{Certificate: [][]byte{certificate}, PrivateKey: privateKey}}
	trusted := &http.Client{Transport: transport}
	response, err := trusted.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatal("trusted API-server client failed")
	}
	if response, err := server.Client().Get(server.URL + "/metrics"); err == nil {
		response.Body.Close()
		t.Fatal("unauthenticated metrics transport was accepted")
	}
	response, err = trusted.Get(server.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("trusted metrics request failed")
	}
	untrustedTransport := server.Client().Transport.(*http.Transport).Clone()
	untrustedTransport.TLSClientConfig.Certificates = server.TLS.Certificates
	untrusted := &http.Client{Transport: untrustedTransport}
	if response, err := untrusted.Get(server.URL); err == nil {
		response.Body.Close()
		t.Fatal("untrusted client certificate was accepted")
	}
	if _, err := webhookTLS(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("missing CA accepted")
	}
}

// TestTransportMetricsRequireTrustedAPIServerClient serves the production
// Transport() through the production webhookTLS, so /metrics is proven to sit
// behind the same client-certificate boundary as admission and /status.
func TestTransportMetricsRequireTrustedAPIServerClient(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issuer := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "synthetic-api-server-ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	ca, err := x509.CreateCertificate(rand.Reader, issuer, issuer, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "synthetic-api-server"}, NotBefore: issuer.NotBefore, NotAfter: issuer.NotAfter, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	certificate, err := x509.CreateCertificate(rand.Reader, leaf, issuer, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "client-ca.crt")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca}), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := webhookTLS(path)
	if err != nil {
		t.Fatal(err)
	}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	ledger := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: registration.LedgerName, Namespace: "private", UID: "ledger-1"}, Data: map[string][]byte{"ledger.json": []byte(`{"authority":"milo","projects":["provider"]}`)}}
	handler := &registration.Handler{Store: registration.Store{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(ledger).Build(), Namespace: "private", Authority: "milo", Projects: []string{"provider"}}}
	server := httptest.NewUnstartedServer(http.MaxBytesHandler(handler.Transport(), 2<<20))
	server.TLS = config
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	if response, err := server.Client().Get(server.URL + "/metrics"); err == nil {
		response.Body.Close()
		t.Fatal("metrics served without a trusted API-server client certificate")
	}
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.Certificates = []tls.Certificate{{Certificate: [][]byte{certificate}, PrivateKey: privateKey}}
	response, err := (&http.Client{Transport: transport}).Get(server.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || !strings.Contains(string(body), "fleet_registration_ledger_tail_headroom_bytes ") || !strings.Contains(string(body), "fleet_registration_ledger_remaining_segments 16") {
		t.Fatalf("trusted metrics scrape: %d %v\n%s", response.StatusCode, err, body)
	}
}
