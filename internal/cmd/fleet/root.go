// SPDX-License-Identifier: Apache-2.0

package fleet

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"go.datum.net/datumctl/plugin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fleetv1alpha1 "go.datum.net/fleet/internal/api/session/v1alpha1"
	"go.datum.net/fleet/internal/gateway"
	"go.datum.net/fleet/internal/protocol"
	"go.datum.net/fleet/internal/relay"
)

const (
	ExitUsage      = 2
	ExitAuth       = 10
	ExitPermission = 11
	ExitAPI        = 12
	ExitConnect    = 13
)

type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

type proxyOptions struct {
	namespace, listen, credentialFile, readyFile, caFile, apiServer, apiCAFile string
	ttl                                                                        time.Duration
}

func Command() *cobra.Command {
	root := plugin.NewRootCmd("fleet", "Manage Datum Fleet resources")
	mm := &cobra.Command{Use: "machine-management", Short: "Access allocation-scoped machine management"}
	o := &proxyOptions{}
	proxy := &cobra.Command{Use: "proxy MACHINE", Short: "Open a short-lived local Redfish endpoint", Args: cobra.ExactArgs(1), SilenceUsage: true, RunE: func(cmd *cobra.Command, args []string) error { return o.run(cmd.Context(), cmd, args[0]) }}
	proxy.Flags().StringVar(&o.namespace, "namespace", "default", "Machine namespace")
	proxy.Flags().StringVar(&o.listen, "listen", "127.0.0.1:0", "loopback address for the local Redfish endpoint")
	proxy.Flags().DurationVar(&o.ttl, "ttl", 15*time.Minute, "session lifetime (1m-15m)")
	proxy.Flags().StringVar(&o.credentialFile, "credential-file", "", "0600 JSON credential file (temporary when omitted)")
	proxy.Flags().StringVar(&o.readyFile, "ready-file", "", "machine-readable non-secret readiness JSON")
	proxy.Flags().StringVar(&o.caFile, "ca-file", "", "session CA bundle path (temporary when omitted)")
	proxy.Flags().StringVar(&o.apiServer, "api-server", "", "Fleet/Kubernetes API URL (defaults to datumctl API context)")
	proxy.Flags().StringVar(&o.apiCAFile, "api-ca-file", "", "CA bundle for the API server")
	mm.AddCommand(proxy)
	root.AddCommand(mm)
	return root
}

func (o *proxyOptions) run(parent context.Context, cmd *cobra.Command, machine string) error {
	if o.ttl < time.Minute || o.ttl > 15*time.Minute {
		return &ExitError{ExitUsage, errors.New("--ttl must be between 1m and 15m")}
	}
	pctx := plugin.Context()
	if pctx.CredentialsHelper == "" {
		return &ExitError{ExitAuth, errors.New("run datumctl-fleet through datumctl so the credential helper is available")}
	}
	if pctx.Project == "" {
		return &ExitError{ExitUsage, errors.New("select a Datum project before creating a machine-management session")}
	}
	token, err := plugin.Token()
	if err != nil {
		return &ExitError{ExitAuth, fmt.Errorf("obtain credentials through datumctl: %w", err)}
	}
	api, err := newAPIClient(o, pctx.APIHost, token, pctx.Project)
	if err != nil {
		return &ExitError{ExitAPI, err}
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return &ExitError{ExitConnect, err}
	}
	defer clear(privateKey)
	ctx, cancel := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer cancel()
	session, err := api.create(ctx, o.namespace, machine, protocol.PublicKey(privateKey), o.ttl)
	if err != nil {
		return classifyAPI(err)
	}
	sessionName := session.Name
	defer func() {
		revokeCtx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		_ = api.delete(revokeCtx, o.namespace, sessionName)
	}()
	session, err = api.waitReady(ctx, o.namespace, sessionName)
	if err != nil {
		return classifyAPI(err)
	}
	conn, err := connectLocation(ctx, privateKey, session)
	if err != nil {
		return &ExitError{ExitConnect, err}
	}
	defer conn.Close()
	cleanup, endpoint, err := o.serveLocal(ctx, privateKey, session.Name, session.Status.Connection, conn)
	if err != nil {
		return &ExitError{ExitConnect, err}
	}
	defer cleanup()
	fmt.Fprintf(cmd.OutOrStdout(), "Session %s is ready until %s\nRedfish endpoint: %s\nReadiness file: %s\n", session.Name, session.Status.ExpiresAt.UTC().Format(time.RFC3339), endpoint, o.readyFile)
	<-ctx.Done()
	return nil
}

type apiClient struct {
	base, project, token string
	client               *http.Client
}

func newAPIClient(o *proxyOptions, host, token, project string) (*apiClient, error) {
	base := o.apiServer
	if base == "" {
		if strings.Contains(host, "://") {
			base = host
		} else {
			base = "https://" + host
		}
	}
	if base == "https://" || base == "" {
		return nil, errors.New("datumctl did not provide an API host")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if o.apiCAFile != "" {
		b, err := os.ReadFile(o.apiCAFile)
		if err != nil {
			return nil, err
		}
		roots, _ := x509.SystemCertPool()
		if roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(b) {
			return nil, errors.New("API CA bundle contains no certificates")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
	return &apiClient{base: strings.TrimRight(base, "/"), project: project, token: token, client: &http.Client{Transport: transport, Timeout: 30 * time.Second}}, nil
}
func (a *apiClient) url(ns, name string) string {
	u := a.base + "/apis/metal.datumapis.com/v1alpha1/namespaces/" + url.PathEscape(ns) + "/machinemanagementsessions"
	if name != "" {
		u += "/" + url.PathEscape(name)
	}
	return u
}
func (a *apiClient) do(ctx context.Context, method, u string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("X-Datum-Project", a.project)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &apiError{status: resp.StatusCode, message: strings.TrimSpace(string(b))}
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
	}
	return nil
}
func (a *apiClient) create(ctx context.Context, ns, machine, key string, ttl time.Duration) (*fleetv1alpha1.MachineManagementSession, error) {
	s := &fleetv1alpha1.MachineManagementSession{TypeMeta: metav1.TypeMeta{APIVersion: fleetv1alpha1.GroupVersion.String(), Kind: "MachineManagementSession"}, ObjectMeta: metav1.ObjectMeta{GenerateName: machine + "-", Namespace: ns}, Spec: fleetv1alpha1.MachineManagementSessionSpec{MachineRef: fleetv1alpha1.LocalReference{Name: machine}, Protocol: "Redfish", AccessProfile: "Provisioning", ClientPublicKey: key, TTL: metav1.Duration{Duration: ttl}}}
	b, _ := json.Marshal(s)
	var out fleetv1alpha1.MachineManagementSession
	if err := a.do(ctx, http.MethodPost, a.url(ns, ""), strings.NewReader(string(b)), &out); err != nil {
		return nil, err
	}
	return &out, nil
}
func (a *apiClient) waitReady(ctx context.Context, ns, name string) (*fleetv1alpha1.MachineManagementSession, error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		var s fleetv1alpha1.MachineManagementSession
		if err := a.do(ctx, http.MethodGet, a.url(ns, name), nil, &s); err != nil {
			return nil, err
		}
		for _, c := range s.Status.Conditions {
			if c.Type == fleetv1alpha1.ConditionReady && c.Status == metav1.ConditionTrue && s.Status.Connection != nil {
				return &s, nil
			}
			if c.Type == fleetv1alpha1.ConditionReady && c.Status == metav1.ConditionFalse {
				return nil, fmt.Errorf("session rejected: %s: %s", c.Reason, c.Message)
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
func (a *apiClient) delete(ctx context.Context, ns, name string) error {
	return a.do(ctx, http.MethodDelete, a.url(ns, name), nil, nil)
}

type apiError struct {
	status  int
	message string
}

func (e *apiError) Error() string { return fmt.Sprintf("API returned %d: %s", e.status, e.message) }
func classifyAPI(err error) error {
	var e *apiError
	if errors.As(err, &e) {
		if e.status == http.StatusUnauthorized {
			return &ExitError{ExitAuth, err}
		}
		if e.status == http.StatusForbidden {
			return &ExitError{ExitPermission, err}
		}
	}
	return &ExitError{ExitAPI, err}
}

func connectLocation(ctx context.Context, key ed25519.PrivateKey, s *fleetv1alpha1.MachineManagementSession) (net.Conn, error) {
	c := s.Status.Connection
	if c == nil {
		return nil, errors.New("session has no connection")
	}
	u, err := url.Parse(c.RelayURL)
	if err != nil || u.Scheme != "tcp" {
		return nil, fmt.Errorf("unsupported relay URL %q", c.RelayURL)
	}
	d := net.Dialer{}
	raw, err := d.DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return nil, fmt.Errorf("reach relay: %w", err)
	}
	if err := relay.Connect(raw, c.RouteID); err != nil {
		_ = raw.Close()
		return nil, err
	}
	expected, err := hex.DecodeString(c.GatewayCertSHA256)
	if err != nil || len(expected) != sha256.Size {
		_ = raw.Close()
		return nil, errors.New("session published an invalid gateway certificate pin")
	}
	tlsConn := tls.Client(raw, &tls.Config{MinVersion: tls.VersionTLS13, ServerName: "fleet-gateway", NextProtos: []string{"datum-fleet-redfish/1"}, InsecureSkipVerify: true, VerifyConnection: func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) != 1 {
			return errors.New("gateway certificate chain must contain one pinned certificate")
		}
		sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
		if !equal(sum[:], expected) {
			return errors.New("gateway certificate does not match the admitted session")
		}
		return nil
	}})
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("authenticate location gateway: %w", err)
	}
	return tlsConn, nil
}

func (o *proxyOptions) serveLocal(ctx context.Context, key ed25519.PrivateKey, sessionName string, c *fleetv1alpha1.SessionConnection, remote net.Conn) (func(), string, error) {
	listener, err := net.Listen("tcp", o.listen)
	if err != nil {
		return nil, "", err
	}
	if !isLoopback(listener.Addr()) {
		_ = listener.Close()
		return nil, "", errors.New("local Redfish endpoint must listen on loopback")
	}
	cert, caPEM, err := localCertificate()
	if err != nil {
		_ = listener.Close()
		return nil, "", err
	}
	dir, err := os.MkdirTemp("", "datumctl-fleet-")
	if err != nil {
		_ = listener.Close()
		return nil, "", err
	}
	removeDir := true
	if o.caFile == "" {
		o.caFile = filepath.Join(dir, "ca.pem")
	}
	if o.credentialFile == "" {
		o.credentialFile = filepath.Join(dir, "credentials.json")
	}
	if o.readyFile == "" {
		o.readyFile = filepath.Join(dir, "ready.json")
	}
	cleanup := func() {
		_ = listener.Close()
		_ = remote.Close()
		_ = os.Remove(o.credentialFile)
		_ = os.Remove(o.caFile)
		_ = os.Remove(o.readyFile)
		if removeDir {
			_ = os.Remove(dir)
		}
	}
	if err := os.WriteFile(o.caFile, caPEM, 0o600); err != nil {
		cleanup()
		return nil, "", err
	}
	password := randomSecret()
	username := "fleet"
	endpoint := "https://" + listener.Addr().String() + "/redfish/v1/"
	credential := map[string]string{"endpoint": endpoint, "username": username, "password": password, "caBundle": o.caFile}
	if err := writeJSONFile(o.credentialFile, credential, 0o600); err != nil {
		cleanup()
		return nil, "", err
	}
	ready := map[string]any{"ready": true, "endpoint": endpoint, "credentialFile": o.credentialFile, "caBundle": o.caFile, "session": sessionName}
	if err := writeJSONFile(o.readyFile, ready, 0o644); err != nil {
		cleanup()
		return nil, "", err
	}
	transport := &http.Transport{ForceAttemptHTTP2: false, DialTLSContext: singleDialer(remote)}
	proxy := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "https", Host: "fleet-gateway"})
	proxy.Transport = transport
	original := proxy.Director
	proxy.Director = func(r *http.Request) {
		original(r)
		r.Host = "fleet-gateway"
		r.Header.Del("Authorization")
		r.Header.Del("X-Auth-Token")
		p, _ := protocol.Sign(key, c.RouteID, c.GatewayAudience, time.Now())
		r.Header.Set(gateway.HeaderSession, c.RouteID)
		r.Header.Set(gateway.HeaderAudience, c.GatewayAudience)
		r.Header.Set(gateway.HeaderTimestamp, p.Timestamp)
		r.Header.Set(gateway.HeaderNonce, p.Nonce)
		r.Header.Set(gateway.HeaderSignature, p.Signature)
	}
	local := newLocalAuth(username, password, proxy)
	server := &http.Server{Handler: local, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	tlsListener := tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	go func() { _ = server.Serve(tlsListener) }()
	return cleanup, endpoint, nil
}

func singleDialer(conn net.Conn) func(context.Context, string, string) (net.Conn, error) {
	var mu sync.Mutex
	used := false
	return func(context.Context, string, string) (net.Conn, error) {
		mu.Lock()
		defer mu.Unlock()
		if used {
			return nil, errors.New("location connection is single-use")
		}
		used = true
		return conn, nil
	}
}
func newLocalAuth(user, password string, next http.Handler) http.Handler {
	tokens := map[string]bool{}
	var mu sync.Mutex
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validLocalTarget(r) {
			http.Error(w, "arbitrary local targets and Host routing are not accepted", http.StatusBadRequest)
			return
		}
		// Generic Redfish clients discover the service root before creating a
		// session. It contains only virtual links and still crosses the admitted,
		// proof-protected gateway stream.
		if r.Method == http.MethodGet && (r.URL.Path == "/redfish/v1" || r.URL.Path == "/redfish/v1/") {
			// DMTF clients commonly probe the slashless URI first. Canonicalize it
			// for the virtual gateway without redirecting or exposing another host.
			if r.URL.Path == "/redfish/v1" {
				r.URL.Path = "/redfish/v1/"
			}
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/redfish/v1/SessionService/Sessions" {
			var in struct {
				UserName string `json:"UserName"`
				Password string `json:"Password"`
			}
			if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in) != nil || in.UserName != user || in.Password != password {
				http.Error(w, "invalid local Redfish credentials", http.StatusUnauthorized)
				return
			}
			token := randomSecret()
			mu.Lock()
			tokens[token] = true
			mu.Unlock()
			w.Header().Set("X-Auth-Token", token)
			w.Header().Set("Location", "/redfish/v1/SessionService/Sessions/local")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"@odata.id":"/redfish/v1/SessionService/Sessions/local","Id":"local","Name":"Fleet local session"}`)
			return
		}
		if r.Method == http.MethodDelete && r.URL.Path == "/redfish/v1/SessionService/Sessions/local" {
			token := r.Header.Get("X-Auth-Token")
			mu.Lock()
			valid := tokens[token]
			if valid {
				delete(tokens, token)
			}
			mu.Unlock()
			if !valid {
				http.Error(w, "invalid local Redfish session", http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		authorized := false
		if u, p, ok := r.BasicAuth(); ok && u == user && p == password {
			authorized = true
		}
		mu.Lock()
		if tokens[r.Header.Get("X-Auth-Token")] {
			authorized = true
		}
		mu.Unlock()
		if !authorized {
			w.Header().Set("WWW-Authenticate", `Basic realm="Fleet Redfish"`)
			http.Error(w, "local Redfish authentication required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func validLocalTarget(r *http.Request) bool {
	if r.URL.IsAbs() || r.RequestURI == "*" || strings.Contains(r.URL.Path, "..") || !strings.HasPrefix(r.URL.Path, "/redfish/v1") {
		return false
	}
	if r.URL.RawPath != "" {
		unescaped, err := url.PathUnescape(r.URL.RawPath)
		if err != nil || unescaped != r.URL.Path {
			return false
		}
	}
	host := r.Host
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	host = strings.Trim(host, "[]")
	return host == "localhost" || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
}
func localCertificate() (tls.Certificate, []byte, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	pub := &priv.PublicKey
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	now := time.Now()
	tmpl := x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "datumctl-fleet local Redfish"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}, DNSNames: []string{"localhost"}, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, pub, priv)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(priv)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	return cert, certPEM, err
}
func randomSecret() string { b := make([]byte, 24); _, _ = rand.Read(b); return hex.EncodeToString(b) }
func writeJSONFile(path string, v any, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
func isLoopback(addr net.Addr) bool {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
func equal(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	return v == 0
}
