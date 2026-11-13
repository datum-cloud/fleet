package gateway

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.datum.net/fleet/internal/journal"
	"go.datum.net/fleet/internal/protocol"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	coordinationv1 "go.datum.net/fleet/internal/api/coordination/v1alpha1"
	fleetv1alpha1 "go.datum.net/fleet/internal/api/session/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
)

func TestTeardownRequiresDurableSettledEffects(t *testing.T) {
	for _, outcome := range []journal.Outcome{journal.Pending, journal.Unknown, journal.Failed, journal.Succeeded} {
		t.Run(string(outcome), func(t *testing.T) {
			reconciler := &Reconciler{Config: Config{JournalDir: t.TempDir()}}
			key := client.ObjectKey{Namespace: "cell", Name: "session"}
			if err := reconciler.verifyTeardown(key, "capacity"); err == nil {
				t.Fatal("missing journal treated as cleanup proof")
			}
			store, err := journal.Open(filepath.Join(reconciler.Config.JournalDir, "operations"))
			if err != nil {
				t.Fatal(err)
			}
			control, err := journal.Open(filepath.Join(reconciler.Config.JournalDir, "sessions"))
			if err != nil {
				t.Fatal(err)
			}
			marker := journal.Identity(key.Name, "SESSION", "", nil)
			if _, _, err := control.Admit(journal.Operation{ID: marker, SessionName: key.Name, MachineCapacityName: "capacity", DeviceName: "device"}); err != nil {
				t.Fatal(err)
			}
			if _, err := control.Finish(marker, journal.Succeeded, 0, "synthetic proof"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.Admit(journal.Operation{ID: "effect", SessionName: key.Name, MachineCapacityName: "capacity", DeviceName: "device"}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Finish("effect", outcome, 0, "synthetic result"); err != nil {
				t.Fatal(err)
			}
			restarted := &Reconciler{Config: reconciler.Config}
			if err := restarted.verifyTeardown(key, "capacity"); (err == nil) != (outcome == journal.Succeeded) {
				t.Fatalf("outcome %s: %v", outcome, err)
			}
			if outcome == journal.Succeeded {
				if _, err := control.Get(journal.Identity(key.Name, "REVOKED", "", nil)); err != nil {
					t.Fatalf("missing durable revocation: %v", err)
				}
				if err := os.RemoveAll(filepath.Join(reconciler.Config.JournalDir, "operations")); err != nil {
					t.Fatal(err)
				}
				if err := restarted.verifyTeardown(key, "capacity"); err == nil {
					t.Fatal("lost operation directory treated as empty evidence")
				}
			}
		})
	}
}

func TestStopDrainsRequestsAndDeniesLaterEffects(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	active := &activeSession{cancel: cancel, done: make(chan struct{}), spec: coordinationv1.ProjectedMachineManagementSessionSpec{ClientPublicKey: protocol.PublicKey(privateKey)}}
	key := client.ObjectKey{Name: "session", Namespace: "cell"}
	reconciler := &Reconciler{Config: Config{Audience: "audience"}, active: map[client.ObjectKey]*activeSession{key: active}}
	entered, release, served, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	handler := reconciler.proofMiddleware(active, "route", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { close(entered); <-release }))
	proof, err := protocol.Sign(privateKey, "route", "audience", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Header.Set(HeaderSession, "route")
	request.Header.Set(HeaderAudience, "audience")
	request.Header.Set(HeaderTimestamp, proof.Timestamp)
	request.Header.Set(HeaderNonce, proof.Nonce)
	request.Header.Set(HeaderSignature, proof.Signature)
	go func() { handler.ServeHTTP(httptest.NewRecorder(), request); close(served) }()
	<-entered
	go func() { reconciler.stop(key); close(stopped) }()
	<-ctx.Done()
	select {
	case <-stopped:
		t.Fatal("teardown returned with a running effect")
	default:
	}
	close(release)
	<-served
	<-stopped
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("post-revocation request: %d", response.Code)
	}
	if err := reconciler.verifyTeardown(key, "capacity"); err == nil {
		t.Fatal("unstopped stream accepted as teardown")
	}
	close(active.done)
}

func TestRetainedSessionRejectsChangedInputsAfterRestart(t *testing.T) {
	store, err := journal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	spec := coordinationv1.ProjectedMachineManagementSessionSpec{DesiredState: fleetv1alpha1.ProjectionActive, ClientPublicKey: "key"}
	target := resolvedTarget{MachineCapacityName: "capacity", DeviceName: "device", Adapter: "synthetic", SystemID: "physical"}
	if err := retainSession(store, "session", spec, target); err != nil {
		t.Fatal(err)
	}
	spec.AuthorityFreshUntil = metav1.Now()
	if err := retainSession(store, "session", spec, target); err != nil {
		t.Fatalf("freshness could not advance: %v", err)
	}
	spec.ClientPublicKey = "changed"
	if err := retainSession(store, "session", spec, target); err == nil {
		t.Fatal("changed client key recovered old authority")
	}
	spec.ClientPublicKey = "key"
	target.DeviceName = "other-device"
	if err := retainSession(store, "session", spec, target); err == nil {
		t.Fatal("changed device recovered old authority")
	}
}

func TestBindingInputsCannotChangeAfterGatewayAcceptance(t *testing.T) {
	a := coordinationv1.ProjectedMachineManagementSessionSpec{
		MachineCapacityRef: fleetv1alpha1.LocalReference{Name: "capacity"}, Protocol: "Redfish", AccessProfile: "Provisioning", ClientPublicKey: "key",
		ConnectBefore: metav1.NewTime(time.Unix(100, 0)), ExpiresAt: metav1.NewTime(time.Unix(200, 0)),
		AuthorityFreshUntil: metav1.NewTime(time.Unix(110, 0)),
	}
	b := a
	b.AuthorityFreshUntil = metav1.NewTime(time.Unix(120, 0))
	if !sameBinding(a, b) {
		t.Fatal("authority freshness was not refreshable")
	}
	b.MachineCapacityRef.Name = "changed"
	if sameBinding(a, b) {
		t.Fatal("changed capacity was accepted")
	}
}

func TestEnrollmentResolutionUsesCapacityDeviceNotCustomerTarget(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := fleetv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	capacity := &fleetv1.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Name: "allocated", Namespace: DefaultEnrollmentNamespace}, Spec: fleetv1.MachineCapacitySpec{DeviceRef: fleetv1.LocalReference{Name: "device-a"}}}
	enrollment := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "device-a", Namespace: DefaultEnrollmentNamespace, Labels: map[string]string{EnrollmentDeviceLabel: "device-a"}}, Data: map[string][]byte{"adapter": []byte("sushy-libvirt-v1"), "endpoint": []byte("https://private.invalid"), "system-id": []byte("physical-a"), "username": []byte("user"), "password": []byte("pass")}}
	foreign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "device-b", Namespace: DefaultEnrollmentNamespace, Labels: map[string]string{EnrollmentDeviceLabel: "device-b"}}, Data: map[string][]byte{"adapter": []byte("sushy-libvirt-v1"), "endpoint": []byte("https://foreign.invalid"), "system-id": []byte("physical-b"), "username": []byte("user"), "password": []byte("pass")}}
	r := &Reconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(capacity, enrollment, foreign).Build(), Config: Config{SupportedAdapter: "sushy-libvirt-v1"}}
	target, err := r.resolveTarget(context.Background(), coordinationv1.ProjectedMachineManagementSessionSpec{MachineCapacityRef: fleetv1alpha1.LocalReference{Name: "allocated"}})
	if err != nil {
		t.Fatal(err)
	}
	if target.DeviceName != "device-a" || target.SystemID != "physical-a" || target.BaseURL != "https://private.invalid" {
		t.Fatalf("wrong enrollment resolved: %+v", target)
	}
}
