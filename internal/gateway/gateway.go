// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetv1alpha1 "go.datum.net/fleet/api/v1alpha1"
	"go.datum.net/fleet/internal/journal"
	fleetmetrics "go.datum.net/fleet/internal/metrics"
	"go.datum.net/fleet/internal/protocol"
	"go.datum.net/fleet/internal/redfish"
	"go.datum.net/fleet/internal/relay"
)

const (
	HeaderTimestamp = "X-Datum-Fleet-Timestamp"
	HeaderNonce     = "X-Datum-Fleet-Nonce"
	HeaderSignature = "X-Datum-Fleet-Signature"
	HeaderSession   = "X-Datum-Fleet-Session"
	HeaderAudience  = "X-Datum-Fleet-Audience"
)

type Config struct {
	RelayAddress          string
	RelayPublicURL        string
	RelayGatewayToken     string
	Audience              string
	AdapterRoute          string
	BMCBaseURL            string
	BMCUsername           string
	BMCPassword           string
	JournalDir            string
	StateDir              string
	DropResetResponseOnce bool
}

type Reconciler struct {
	client.Client
	Config Config
	mu     sync.Mutex
	active map[client.ObjectKey]*activeSession
}

type activeSession struct {
	cancel context.CancelFunc
	mu     sync.RWMutex
	spec   fleetv1alpha1.ProjectedMachineManagementSessionSpec
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var p fleetv1alpha1.ProjectedMachineManagementSession
	if err := r.Get(ctx, req.NamespacedName, &p); err != nil {
		if apierrors.IsNotFound(err) {
			r.stop(req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	now := time.Now()
	if !p.DeletionTimestamp.IsZero() || now.After(p.Spec.ExpiresAt.Time) {
		r.stop(req.NamespacedName)
		return ctrl.Result{}, nil
	}
	if p.Status.ConnectedAt == nil && now.After(p.Spec.ConnectBefore.Time) {
		r.stop(req.NamespacedName)
		setCondition(&p, metav1.ConditionFalse, "ConnectWindowExpired", "No client connected before the admitted connection deadline.")
		return ctrl.Result{}, r.Status().Update(ctx, &p)
	}
	if p.Spec.AdapterRoute != r.Config.AdapterRoute {
		setCondition(&p, metav1.ConditionFalse, "AdapterRouteRejected", "The projected adapter route is not served by this gateway.")
		return ctrl.Result{}, r.Status().Update(ctx, &p)
	}
	r.mu.Lock()
	if r.active == nil {
		r.active = map[client.ObjectKey]*activeSession{}
	}
	a := r.active[req.NamespacedName]
	if a == nil {
		runCtx, cancel := context.WithCancel(context.Background())
		a = &activeSession{cancel: cancel, spec: p.Spec}
		r.active[req.NamespacedName] = a
		go r.run(runCtx, req.NamespacedName, a)
	} else {
		a.mu.Lock()
		if !sameBinding(a.spec, p.Spec) {
			a.mu.Unlock()
			r.mu.Unlock()
			r.stop(req.NamespacedName)
			setCondition(&p, metav1.ConditionFalse, "SessionInputsChanged", "Projected binding inputs changed after the gateway accepted them.")
			return ctrl.Result{}, r.Status().Update(ctx, &p)
		}
		a.spec.AuthorityFreshUntil = p.Spec.AuthorityFreshUntil
		a.mu.Unlock()
	}
	r.mu.Unlock()
	wake := time.Until(p.Spec.ExpiresAt.Time)
	if p.Status.ConnectedAt == nil && time.Until(p.Spec.ConnectBefore.Time) < wake {
		wake = time.Until(p.Spec.ConnectBefore.Time)
	}
	return ctrl.Result{RequeueAfter: wake}, nil
}

func (r *Reconciler) run(ctx context.Context, key client.ObjectKey, a *activeSession) {
	fleetmetrics.ActiveSessions.Inc()
	defer fleetmetrics.ActiveSessions.Dec()
	cert, der, fingerprint, err := loadOrCreateCertificate(r.Config.StateDir, key.Name)
	if err != nil {
		r.failStatus(key, "GatewayCertificateError", "The location gateway could not create its session certificate.")
		return
	}
	store, err := journal.Open(filepath.Join(r.Config.JournalDir, "operations"))
	if err != nil {
		r.failStatus(key, "JournalUnavailable", "The durable operation journal is unavailable.")
		return
	}
	routeID := randomHex(24)
	conn, err := r.dialRelay(ctx, routeID)
	if err != nil {
		r.failStatus(key, "RelayUnavailable", "The location gateway could not establish its outbound relay route.")
		return
	}
	defer conn.Close()
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	if err := r.readyStatus(key, routeID, fingerprint); err != nil {
		return
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || line != "OK\n" {
		r.failStatus(key, "RelayDisconnected", "The outbound relay route ended before a client connected.")
		return
	}
	r.connectedStatus(key)
	a.mu.RLock()
	spec := a.spec
	a.mu.RUnlock()
	adapter := &redfish.SushyAdapter{BaseURL: r.Config.BMCBaseURL, SystemID: spec.BMCSystemID, Username: r.Config.BMCUsername, Password: r.Config.BMCPassword, DropResetResponseOnce: r.Config.DropResetResponseOnce}
	h := &redfish.Handler{Adapter: adapter, Journal: store, CurrentAuthority: func() redfish.Authority {
		a.mu.RLock()
		defer a.mu.RUnlock()
		s := a.spec
		return redfish.Authority{SessionUID: string(s.SessionUID), SessionCorrelationID: s.SessionCorrelationID, OperationCorrelationID: s.OperationCorrelationID, ClaimUID: string(s.ClaimUID), MachineUID: string(s.MachineUID), BindingID: s.BindingID, BindingEpoch: s.BindingEpoch, AdapterRoute: s.AdapterRoute, OperationEpoch: 1, ExpiresAt: s.ExpiresAt.Time, FreshUntil: s.AuthorityFreshUntil.Time}
	}}
	protected := r.proofMiddleware(a, h)
	tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, NextProtos: []string{"datum-fleet-redfish/1"}})
	// The relay admits one connector for the session, so an HTTP idle timeout
	// would permanently strand a valid session during a long PXE boot. Expiry,
	// revocation, and projection teardown bound the stream lifetime instead.
	server := &http.Server{Handler: protected, ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 16 << 10}
	_ = server.Serve(newOneConnListener(tlsConn))
	_ = der // DER is retained only to make certificate pin derivation explicit.
	r.endedStatus(key, "Disconnected", "The client connection ended.")
}

func (r *Reconciler) dialRelay(ctx context.Context, routeID string) (net.Conn, error) {
	d := net.Dialer{Timeout: 10 * time.Second}
	c, err := d.DialContext(ctx, "tcp", r.Config.RelayAddress)
	if err != nil {
		return nil, err
	}
	if err := relay.Register(c, routeID, r.Config.RelayGatewayToken); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

func (r *Reconciler) proofMiddleware(a *activeSession, next http.Handler) http.Handler {
	used := map[string]bool{}
	var mu sync.Mutex
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		a.mu.RLock()
		spec := a.spec
		a.mu.RUnlock()
		if req.Header.Get(HeaderSession) != string(spec.SessionUID) || req.Header.Get(HeaderAudience) != r.Config.Audience {
			http.Error(w, "session proof target mismatch", http.StatusUnauthorized)
			return
		}
		proof := protocol.Proof{Timestamp: req.Header.Get(HeaderTimestamp), Nonce: req.Header.Get(HeaderNonce), Signature: req.Header.Get(HeaderSignature)}
		err := protocol.Verify(spec.ClientPublicKey, string(spec.SessionUID), r.Config.Audience, proof, time.Now(), func(n string) bool {
			mu.Lock()
			defer mu.Unlock()
			if used[n] {
				return true
			}
			used[n] = true
			return false
		})
		if err != nil {
			http.Error(w, "invalid session key proof", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, req)
	})
}

func (r *Reconciler) readyStatus(key client.ObjectKey, route, fingerprint string) error {
	return r.updateStatus(key, func(p *fleetv1alpha1.ProjectedMachineManagementSession) {
		p.Status.Connection = &fleetv1alpha1.SessionConnection{RelayURL: r.Config.RelayPublicURL, RouteID: route, GatewayAudience: r.Config.Audience, GatewayCertSHA256: fingerprint, ProtocolVersion: protocol.Version}
		setCondition(p, metav1.ConditionTrue, "SessionReady", "The outbound location route is ready for one connector.")
	})
}
func (r *Reconciler) connectedStatus(key client.ObjectKey) {
	_ = r.updateStatus(key, func(p *fleetv1alpha1.ProjectedMachineManagementSession) {
		now := metav1.Now()
		p.Status.ConnectedAt = &now
		setCondition(p, metav1.ConditionTrue, "Connected", "The client proved possession of the admitted session key.")
	})
}
func (r *Reconciler) endedStatus(key client.ObjectKey, reason, message string) {
	_ = r.updateStatus(key, func(p *fleetv1alpha1.ProjectedMachineManagementSession) {
		now := metav1.Now()
		p.Status.EndedAt = &now
		setCondition(p, metav1.ConditionFalse, reason, message)
	})
}
func (r *Reconciler) failStatus(key client.ObjectKey, reason, message string) {
	_ = r.updateStatus(key, func(p *fleetv1alpha1.ProjectedMachineManagementSession) {
		setCondition(p, metav1.ConditionFalse, reason, message)
	})
}
func (r *Reconciler) updateStatus(key client.ObjectKey, mutate func(*fleetv1alpha1.ProjectedMachineManagementSession)) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var p fleetv1alpha1.ProjectedMachineManagementSession
	if err := r.Get(ctx, key, &p); err != nil {
		return err
	}
	mutate(&p)
	return r.Status().Update(ctx, &p)
}
func (r *Reconciler) stop(key client.ObjectKey) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a := r.active[key]; a != nil {
		a.cancel()
		delete(r.active, key)
	}
}
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Client = mgr.GetClient()
	return ctrl.NewControllerManagedBy(mgr).For(&fleetv1alpha1.ProjectedMachineManagementSession{}).Complete(r)
}

func sameBinding(a, b fleetv1alpha1.ProjectedMachineManagementSessionSpec) bool {
	a.AuthorityFreshUntil = metav1.Time{}
	b.AuthorityFreshUntil = metav1.Time{}
	return a.SessionUID == b.SessionUID && a.ClaimUID == b.ClaimUID && a.MachineUID == b.MachineUID && a.BindingID == b.BindingID && a.BindingEpoch == b.BindingEpoch && a.LocationUID == b.LocationUID && a.LocationRevision == b.LocationRevision && a.BareMetalMachineUID == b.BareMetalMachineUID && a.AccessProfileRevision == b.AccessProfileRevision && a.ClientPublicKey == b.ClientPublicKey && a.ConnectBefore.Equal(&b.ConnectBefore) && a.ExpiresAt.Equal(&b.ExpiresAt) && a.AdapterRoute == b.AdapterRoute && a.BMCSystemID == b.BMCSystemID
}
func setCondition(p *fleetv1alpha1.ProjectedMachineManagementSession, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{Type: fleetv1alpha1.ConditionReady, Status: status, Reason: reason, Message: message, ObservedGeneration: p.Generation, LastTransitionTime: metav1.Now()})
}

func loadOrCreateCertificate(dir, name string) (tls.Certificate, []byte, string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return tls.Certificate{}, nil, "", err
	}
	certPath, keyPath := filepath.Join(dir, name+".crt"), filepath.Join(dir, name+".key")
	if certPEM, err := os.ReadFile(certPath); err == nil {
		keyPEM, e := os.ReadFile(keyPath)
		if e == nil {
			cert, e := tls.X509KeyPair(certPEM, keyPEM)
			if e == nil && len(cert.Certificate) > 0 {
				sum := sha256.Sum256(cert.Certificate[0])
				return cert, cert.Certificate[0], hex.EncodeToString(sum[:]), nil
			}
		}
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, "", err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	now := time.Now()
	tmpl := x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "fleet-gateway"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour), DNSNames: []string{"fleet-gateway"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, pub, priv)
	if err != nil {
		return tls.Certificate{}, nil, "", err
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return tls.Certificate{}, nil, "", err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		return tls.Certificate{}, nil, "", err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, nil, "", err
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	sum := sha256.Sum256(der)
	return cert, der, hex.EncodeToString(sum[:]), err
}
func randomHex(n int) string { b := make([]byte, n); _, _ = rand.Read(b); return hex.EncodeToString(b) }

type oneConnListener struct {
	mu       sync.Mutex
	conn     net.Conn
	accepted bool
	done     chan struct{}
	once     sync.Once
}

func newOneConnListener(conn net.Conn) *oneConnListener {
	l := &oneConnListener{done: make(chan struct{})}
	l.conn = &notifyConn{Conn: conn, closed: func() { l.once.Do(func() { close(l.done) }) }}
	return l
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if !l.accepted {
		l.accepted = true
		conn := l.conn
		l.mu.Unlock()
		return conn, nil
	}
	l.mu.Unlock()
	<-l.done
	return nil, errors.New("single connection consumed")
}
func (l *oneConnListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return l.conn.Close()
}
func (l *oneConnListener) Addr() net.Addr { return dummyAddr("relay") }

type dummyAddr string

func (d dummyAddr) Network() string { return "relay" }
func (d dummyAddr) String() string  { return string(d) }

type notifyConn struct {
	net.Conn
	once   sync.Once
	closed func()
}

func (c *notifyConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.closed)
	return err
}
