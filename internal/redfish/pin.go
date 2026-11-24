package redfish

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ErrPinMismatch means the BMC presented a TLS public key other than the
// approved pin. The handshake is aborted before any request, so no credential
// is sent.
var ErrPinMismatch = errors.New("redfish: BMC TLS public key does not match the approved pin")

// PublicKeySHA256 returns the lowercase hex SHA-256 of the certificate's DER
// SubjectPublicKeyInfo. It is the same value as
//
//	openssl x509 -pubkey -noout | openssl pkey -pubin -outform der | sha256sum
//
// and stays the same when a BMC reissues its certificate for the same key.
func PublicKeySHA256(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:])
}

// ParsePin accepts 64 hex digits, optionally colon-separated and in either
// case, and returns the lowercase form PublicKeySHA256 produces.
func ParsePin(pin string) (string, error) {
	pin = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(pin), ":", ""))
	if b, err := hex.DecodeString(pin); err != nil || len(b) != sha256.Size {
		return "", errors.New("redfish: TLS pin must be a SHA-256 of the public key in hex")
	}
	return pin, nil
}

// PinnedTLSConfig verifies the peer by its leaf public key alone. BMC
// certificates are commonly self-signed and share a subject across a fleet, so
// chain and hostname verification are replaced, not added to: the handshake
// succeeds only when the leaf SubjectPublicKeyInfo hashes to pin.
func PinnedTLSConfig(pin string) (*tls.Config, error) {
	pin, err := ParsePin(pin)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // replaced by the pin check in VerifyConnection
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 || PublicKeySHA256(cs.PeerCertificates[0]) != pin {
				return ErrPinMismatch
			}
			return nil
		},
	}, nil
}

// NewPinnedClient is NewClient over https with the peer verified by
// PinnedTLSConfig instead of the system trust store. Every request opens a new
// connection, so every request re-checks the pin before it is written.
func NewPinnedClient(endpoint, user, password, pin string, timeout time.Duration) (*Client, error) {
	canonical, err := ParseEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(canonical, "https://") {
		return nil, errors.New("redfish: a pinned client requires an https endpoint")
	}
	config, err := PinnedTLSConfig(pin)
	if err != nil {
		return nil, err
	}
	return newClient(canonical, user, password, timeout, config)
}

// Certificate is what a BMC's TLS handshake revealed about its leaf
// certificate.
type Certificate struct {
	PublicKeySHA256 string
	NotAfter        time.Time
	SelfSigned      bool
}

// ServiceRoot holds the anonymous service root facts a probe reads.
type ServiceRoot struct {
	RedfishVersion string
	UUID           string
	Vendor         string
	Product        string
}

// ErrNoHandshake means a probe did not complete a TLS handshake.
var ErrNoHandshake = errors.New("redfish: TLS handshake not completed")

// Probe completes a TLS handshake with any certificate, records it, and issues
// one anonymous GET of the service root through the same guard as every other
// request. It holds no credential, so it cannot send one, which is what makes
// accepting an unverified certificate here safe. The certificate is returned
// whenever the handshake completed, even if the GET failed; otherwise the
// error is ErrNoHandshake.
func Probe(ctx context.Context, endpoint string, timeout time.Duration) (Certificate, ServiceRoot, error) {
	canonical, err := ParseEndpoint(endpoint)
	if err != nil {
		return Certificate{}, ServiceRoot{}, err
	}
	if !strings.HasPrefix(canonical, "https://") || timeout <= 0 {
		return Certificate{}, ServiceRoot{}, errors.New("redfish: a probe requires an https endpoint and a positive timeout")
	}
	u, _ := url.Parse(canonical)
	var (
		mu   sync.Mutex
		leaf *x509.Certificate
	)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DisableKeepAlives = true
	transport.TLSClientConfig = &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // observed, not trusted: no credential is held
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return ErrNoHandshake
			}
			mu.Lock()
			leaf = cs.PeerCertificates[0]
			mu.Unlock()
			return nil
		},
	}
	httpClient := &http.Client{Timeout: timeout, Transport: guard{scheme: u.Scheme, host: u.Host, next: transport},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, canonical+"/redfish/v1/", nil)
	if err != nil {
		return Certificate{}, ServiceRoot{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, getErr := httpClient.Do(req)
	var root struct {
		RedfishVersion, UUID, Vendor, Product string
	}
	if getErr == nil {
		if resp.StatusCode/100 != 2 {
			getErr = Status{resp.StatusCode}
		} else if err := json.NewDecoder(resp.Body).Decode(&root); err != nil {
			getErr = errors.New("redfish: transport failure or unreadable response")
		}
		resp.Body.Close()
	}
	mu.Lock()
	defer mu.Unlock()
	if leaf == nil {
		return Certificate{}, ServiceRoot{}, ErrNoHandshake
	}
	cert := Certificate{PublicKeySHA256: PublicKeySHA256(leaf), NotAfter: leaf.NotAfter.UTC(),
		SelfSigned: bytes.Equal(leaf.RawIssuer, leaf.RawSubject) && leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature) == nil}
	if getErr != nil {
		var status Status
		if !errors.As(getErr, &status) {
			getErr = errors.New("redfish: transport failure or unreadable response")
		}
		return cert, ServiceRoot{}, getErr
	}
	return cert, ServiceRoot{RedfishVersion: root.RedfishVersion, UUID: strings.ToLower(root.UUID), Vendor: root.Vendor, Product: root.Product}, nil
}
