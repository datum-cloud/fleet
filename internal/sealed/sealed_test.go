package sealed

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/catalog"
)

func reason(t *testing.T, err error) string {
	t.Helper()
	var refusal *catalog.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("err = %v, want a refusal", err)
	}
	return refusal.Reason
}

func TestValidateAcceptsOnlyABootstrapSecret(t *testing.T) {
	secret := func(kind corev1.SecretType, data map[string]string) *corev1.Secret {
		s := &corev1.Secret{Type: kind, Data: map[string][]byte{}}
		for k, v := range data {
			s.Data[k] = []byte(v)
		}
		return s
	}
	for _, test := range []struct {
		name   string
		secret *corev1.Secret
		ok     bool
	}{
		{"user data", secret(SecretType, map[string]string{KeyUserData: "#cloud-config\n"}), true},
		{"keys", secret(SecretType, map[string]string{KeySSHAuthorizedKeys: "ssh-ed25519 AAAAExampleOnly"}), true},
		{"another type", secret(corev1.SecretTypeOpaque, map[string]string{KeyUserData: "#cloud-config\n"}), false},
		{"no keys", secret(SecretType, nil), false},
		{"an unknown key", secret(SecretType, map[string]string{KeyUserData: "x", "password": "x"}), false},
		{"over 64 KiB", secret(SecretType, map[string]string{KeyUserData: strings.Repeat("x", MaxBytes), KeySSHAuthorizedKeys: "x"}), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Validate(test.secret)
			if test.ok != (err == nil) {
				t.Fatalf("err = %v", err)
			}
			if err != nil && reason(t, err) != ReasonBootstrapInvalid {
				t.Fatalf("reason = %v", err)
			}
		})
	}
}

func TestReadRefusesAMissingSecret(t *testing.T) {
	reader := fake.NewClientBuilder().Build()
	if _, err := Read(context.Background(), reader, "default", "absent"); reason(t, err) != ReasonBootstrapNotFound {
		t.Fatalf("err = %v", err)
	}
}

// TestVerifyRefusesEveryAlteredPayload: a payload must be the one the
// operation names, under its derived name, with a digest recomputed from its
// salt and data.
func TestVerifyRefusesEveryAlteredPayload(t *testing.T) {
	payload, err := Seal(Payload{SSHAuthorizedKeys: "ssh-ed25519 AAAAExampleOnly"}, "alloc-1-install-1", "alloc-1", "default")
	if err != nil {
		t.Fatal(err)
	}
	reference := ops.SealedInput{Ref: payload.Name, Digest: payload.Spec.Digest}
	if err := Verify(payload, "alloc-1-install-1", reference); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*ops.SealedPayload, *ops.SealedInput, *string){
		"other data":      func(p *ops.SealedPayload, _ *ops.SealedInput, _ *string) { p.Spec.Data = []byte(`{"userData":"x"}`) },
		"other salt":      func(p *ops.SealedPayload, _ *ops.SealedInput, _ *string) { p.Spec.Salt[0] ^= 1 },
		"other operation": func(_ *ops.SealedPayload, _ *ops.SealedInput, operation *string) { *operation = "alloc-1-install-2" },
		"other name": func(p *ops.SealedPayload, r *ops.SealedInput, _ *string) {
			p.Name, r.Ref = "sealed-other", "sealed-other"
		},
		"other digest": func(_ *ops.SealedPayload, r *ops.SealedInput, _ *string) {
			r.Digest = "sha256:" + strings.Repeat("0", 64)
		},
	} {
		t.Run(name, func(t *testing.T) {
			copy := payload.DeepCopyObject().(*ops.SealedPayload)
			ref, operation := reference, "alloc-1-install-1"
			change(copy, &ref, &operation)
			if err := Verify(copy, operation, ref); !errors.Is(err, ErrMismatch) {
				t.Fatalf("err = %v", err)
			}
		})
	}
	// Each seal has a new salt, so equal data never shares a digest.
	again, _ := Seal(Payload{SSHAuthorizedKeys: "ssh-ed25519 AAAAExampleOnly"}, "alloc-1-install-1", "alloc-1", "default")
	if again.Spec.Digest == payload.Spec.Digest || again.Name == payload.Name {
		t.Fatal("two seals of the same data share a digest")
	}
}

// TestSweepDeletesOnlyWhatNothingNeeds covers each rule: a payload whose
// operation ended, one its operation does not name, and one with no
// operation whose allocation is not Active are deleted. The payload of a
// running operation, and one waiting for its operation under an Active
// allocation, are kept.
func TestSweepDeletesOnlyWhatNothingNeeds(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{ops.AddToScheme, fleetv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	api := fake.NewClientBuilder().WithScheme(scheme).Build()
	ctx := context.Background()
	create := func(object client.Object) {
		t.Helper()
		if err := api.Create(ctx, object); err != nil {
			t.Fatal(err)
		}
	}
	allocation := func(name, phase string) {
		a := &fleetv1.MachineAllocation{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}, Status: fleetv1.MachineAllocationStatus{Phase: phase}}
		create(a)
	}
	seal := func(operation, allocation string) *ops.SealedPayload {
		p, err := Seal(Payload{UserData: "#cloud-config\n"}, operation, allocation, "default")
		if err != nil {
			t.Fatal(err)
		}
		p.UID = types.UID("uid-" + p.Name)
		create(p)
		return p
	}
	operation := func(name string, payload *ops.SealedPayload, phase ops.Phase) {
		o := &ops.DeviceInstallation{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}, Status: ops.OperationStatus{Phase: phase}}
		if payload != nil {
			o.Spec.Bootstrap = &ops.SealedInput{Ref: payload.Name, Digest: payload.Spec.Digest}
		}
		create(o)
	}
	allocation("alloc-active", fleetv1.AllocationActive)
	allocation("alloc-released", fleetv1.AllocationReleased)

	running := seal("op-running", "alloc-active")
	operation("op-running", running, ops.PhaseDispatched)
	ended := seal("op-ended", "alloc-active")
	operation("op-ended", ended, ops.PhaseOutcomeUnknown)
	loser := seal("op-named", "alloc-active")
	winner := seal("op-named", "alloc-active")
	operation("op-named", winner, ops.PhaseAdmitted)
	waiting := seal("op-not-yet-created", "alloc-active")
	released := seal("op-never-created", "alloc-released")
	gone := seal("op-never-created-2", "alloc-deleted")

	deleted, err := Sweep(ctx, api, "default")
	if err != nil {
		t.Fatal(err)
	}
	kept := map[string]bool{}
	var list ops.SealedPayloadList
	if err := api.List(ctx, &list); err != nil {
		t.Fatal(err)
	}
	for _, p := range list.Items {
		kept[p.Name] = true
	}
	for _, p := range []*ops.SealedPayload{running, winner, waiting} {
		if !kept[p.Name] {
			t.Errorf("%s was deleted", p.Spec.OperationName)
		}
	}
	for _, p := range []*ops.SealedPayload{ended, loser, released, gone} {
		if kept[p.Name] {
			t.Errorf("%s (%s) was kept", p.Name, p.Spec.OperationName)
		}
	}
	if deleted != 4 {
		t.Fatalf("deleted %d", deleted)
	}
}

func TestValidateRefusesDataThatWouldNotArriveExactly(t *testing.T) {
	for name, value := range map[string]string{
		"invalid UTF-8":             "#cloud-config\n\xff\xfe",
		"too large once escaped":    strings.Repeat("\x01", MaxBytes),
		"escaping right at the cap": strings.Repeat("\"", MaxEncodedBytes/2+1),
	} {
		t.Run(name, func(t *testing.T) {
			secret := &corev1.Secret{Type: SecretType, Data: map[string][]byte{KeyUserData: []byte(value)}}
			if _, err := Validate(secret); reason(t, err) != ReasonBootstrapInvalid {
				t.Fatalf("err = %v", err)
			}
		})
	}
	if _, err := Seal(Payload{UserData: strings.Repeat("\x01", MaxBytes)}, "op", "alloc", "default"); err == nil {
		t.Fatal("Seal accepted data too large to deliver")
	}
}
