package custody

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The test-only kinds prove the four seams without editing the core. They
// are registered only here, and their CRDs live in testdata.

// testSpec is the spec of both test kinds.
type testSpec struct {
	ops.OperationEnvelope `json:",inline"`
	Input                 string `json:"input"`
}

// DeviceTestStaffOperation is a staff kind that acts from holder None.
type DeviceTestStaffOperation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              testSpec            `json:"spec"`
	Status            ops.OperationStatus `json:"status,omitempty"`
}

type DeviceTestStaffOperationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DeviceTestStaffOperation `json:"items"`
}

// DeviceTestProviderOperation is a provider kind that queues and supersedes,
// as power will.
type DeviceTestProviderOperation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              testSpec            `json:"spec"`
	Status            ops.OperationStatus `json:"status,omitempty"`
}

type DeviceTestProviderOperationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DeviceTestProviderOperation `json:"items"`
}

func (in *DeviceTestStaffOperation) Envelope() *ops.OperationEnvelope {
	return &in.Spec.OperationEnvelope
}
func (in *DeviceTestStaffOperation) OperationStatus() *ops.OperationStatus { return &in.Status }
func (in *DeviceTestProviderOperation) Envelope() *ops.OperationEnvelope {
	return &in.Spec.OperationEnvelope
}
func (in *DeviceTestProviderOperation) OperationStatus() *ops.OperationStatus { return &in.Status }
func (in *DeviceTestStaffOperation) DeepCopyObject() runtime.Object           { return copyOf(in) }
func (in *DeviceTestStaffOperationList) DeepCopyObject() runtime.Object       { return copyOf(in) }
func (in *DeviceTestProviderOperation) DeepCopyObject() runtime.Object        { return copyOf(in) }
func (in *DeviceTestProviderOperationList) DeepCopyObject() runtime.Object    { return copyOf(in) }

func addTestKinds(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(ops.GroupVersion, &DeviceTestStaffOperation{}, &DeviceTestStaffOperationList{}, &DeviceTestProviderOperation{}, &DeviceTestProviderOperationList{})
	return nil
}

const (
	testStaffKind    = "DeviceTestStaffOperation"
	testProviderKind = "DeviceTestProviderOperation"
	reasonTestRefuse = "TestRefused"
	testDeadline     = 10 * time.Minute
)

// testEvidence is the test kinds' typed result evidence.
type testEvidence struct {
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

func testKinds() []Kind {
	check := func(_ context.Context, admission *Admission) *Refusal {
		switch admission.Operation.(interface{ input() string }).input() {
		case "refuse":
			return refuse(reasonTestRefuse, "the test kind refuses this input")
		case "undeclared":
			return refuse("Unlisted", "the test kind refuses with a reason it never declared")
		}
		if hook := checkHook.Load(); hook != nil {
			(*hook)(admission)
		}
		return nil
	}
	// The staff kind adds fields to the snapshot, as power adds the reset
	// types discovery offered.
	extra := func(admission *Admission) map[string]string {
		switch admission.Operation.(interface{ input() string }).input() {
		case "extra":
			return map[string]string{"resetTypes": "On,ForceOff", "discoveryInterval": admission.DiscoveryInterval.String()}
		case "too-much":
			out := map[string]string{}
			for index := range 17 {
				out[fmt.Sprintf("field%d", index)] = "x"
			}
			return out
		}
		return nil
	}
	evidence := StrictEvidence(256, func(*testEvidence) error { return nil })
	staff := Kind{
		GVK:     ops.GroupVersion.WithKind(testStaffKind),
		New:     func() ops.Operation { return &DeviceTestStaffOperation{} },
		NewList: func() client.ObjectList { return &DeviceTestStaffOperationList{} },
		Items:   func(list client.ObjectList) []ops.Operation { return items(list.(*DeviceTestStaffOperationList).Items) },
		Digest: func(operation ops.Operation) (string, error) {
			return SpecDigest(operation.(*DeviceTestStaffOperation).Spec)
		},
		Naming:    GeneratedName,
		Deadline:  testDeadline,
		Effectful: true,
		Check:     check,
		Settles:   []SettleRule{{Held: testStaffKind}},
		Reasons:   []Reason{{Name: reasonTestRefuse, RaisedBy: RaisedByCoordinator}, {Name: "EvidenceNotCurrent", RaisedBy: RaisedByCoordinator}},
		Evidence:  evidence,
		// Seam 4: a hold of this kind settles with recovery evidence observed
		// after the operation ended, as power's discovery read will.
		SnapshotExtra: extra,
		RecoverySettle: func(_ context.Context, settle *RecoverySettleContext) *Refusal {
			// The hook sees when the held operation ended, its deadline and
			// its admission snapshot, and the Device as read now.
			if settle.Hold.Deadline == nil || settle.Hold.Snapshot == nil || settle.Device == nil {
				return refuse("EvidenceNotCurrent", "the hold does not carry its deadline and snapshot")
			}
			if !settle.Recovery.Spec.Evidence.ObservedAt.After(settle.Hold.Since.Time) || settle.Recovery.Spec.Evidence.Data["state"] == "" {
				return refuse("EvidenceNotCurrent", "the evidence must be a state observed after the operation ended")
			}
			return nil
		},
	}
	provider := Kind{
		GVK:     ops.GroupVersion.WithKind(testProviderKind),
		New:     func() ops.Operation { return &DeviceTestProviderOperation{} },
		NewList: func() client.ObjectList { return &DeviceTestProviderOperationList{} },
		Items: func(list client.ObjectList) []ops.Operation {
			return items(list.(*DeviceTestProviderOperationList).Items)
		},
		Digest: func(operation ops.Operation) (string, error) {
			return SpecDigest(operation.(*DeviceTestProviderOperation).Spec)
		},
		Naming: DeterministicName,
		Name: func(operation ops.Operation) string {
			return allocationName(operation) + "-test-" + operation.(*DeviceTestProviderOperation).Spec.Input
		},
		Deadline:   testDeadline,
		Effectful:  true,
		Supersedes: true,
		Check:      check,
		Settles:    []SettleRule{{Held: testProviderKind}},
		Reasons:    []Reason{{Name: reasonTestRefuse, RaisedBy: RaisedByCoordinator}},
		Evidence:   evidence,
	}
	return []Kind{staff, provider}
}

// checkHook, when set, runs inside the test kinds' admission check.
var checkHook atomic.Pointer[func(*Admission)]

func (in *DeviceTestStaffOperation) input() string    { return in.Spec.Input }
func (in *DeviceTestProviderOperation) input() string { return in.Spec.Input }

func testRules() []Rule {
	return []Rule{
		{Kind: testStaffKind, Creator: CreatorStaff, Authority: AuthorityService, Holders: []ops.HolderType{ops.HolderNone}},
		{Kind: testProviderKind, Creator: CreatorMetalProvider, Authority: AuthorityAllocation, Holders: []ops.HolderType{ops.HolderAllocation}, Queue: true},
	}
}

// testRegistry is the default registry plus the test kinds and their rows.
func testRegistry(t interface{ Fatal(...any) }) *Registry {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range testKinds() {
		if err := registry.Register(kind); err != nil {
			t.Fatal(err)
		}
	}
	for _, rule := range testRules() {
		if err := registry.AddRule(rule); err != nil {
			t.Fatal(err)
		}
	}
	return registry
}

func copyOf[T any](in *T) *T {
	out := new(T)
	data, _ := jsonMarshal(in)
	_ = jsonUnmarshal(data, out)
	return out
}
