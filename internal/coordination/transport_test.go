package coordination

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8sschema "k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/site"
	"go.datum.net/fleet/internal/site/fixture"
	"go.datum.net/fleet/internal/site/simwork"
)

const (
	namespace = "fleet-coordination"
	cellName  = "cell-example-a"
	device    = "fleet-provider/server-example-001"
	keyDigest = "1111111111111111111111111111111111111111111111111111111111111111"
)

func client() dynamic.Interface {
	return fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[k8sschema.GroupVersionResource]string{Resource: Kind + "List", PayloadResource: PayloadKind + "List"})
}

// federation copies hub handoffs to the cell and aggregates cell status
// back, as Karmada's propagation and status interpreter do.
type federation struct {
	t          *testing.T
	hub, cell  dynamic.Interface
	clusterFor string
}

func (f federation) sync() {
	f.t.Helper()
	ctx := context.Background()
	hub, cell := f.hub.Resource(Resource).Namespace(namespace), f.cell.Resource(Resource).Namespace(namespace)
	list, err := hub.List(ctx, metav1.ListOptions{})
	if err != nil {
		f.t.Fatal(err)
	}
	for index := range list.Items {
		object := list.Items[index]
		copied := &unstructured.Unstructured{Object: map[string]any{"apiVersion": object.GetAPIVersion(), "kind": Kind, "metadata": map[string]any{"name": object.GetName(), "namespace": namespace, "labels": toAny(object.GetLabels())}, "spec": object.Object["spec"]}}
		existing, err := cell.Get(ctx, object.GetName(), metav1.GetOptions{})
		if err != nil {
			if _, err := cell.Create(ctx, copied, metav1.CreateOptions{}); err != nil {
				f.t.Fatal(err)
			}
			continue
		}
		existing.SetLabels(object.GetLabels())
		if _, err := cell.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
			f.t.Fatal(err)
		}
		if observation, ok := existing.Object["status"].(map[string]any); ok && observation["observation"] != nil {
			object.Object["status"] = map[string]any{"reflected": []any{map[string]any{"clusterName": f.clusterFor, "observation": observation["observation"]}}}
			if _, err := hub.UpdateStatus(ctx, &object, metav1.UpdateOptions{}); err != nil {
				f.t.Fatal(err)
			}
		}
	}
}

// syncPayloads mirrors the hub's payload objects to the cell, creating
// those it lacks and deleting those the hub deleted, as Karmada's
// propagation does.
func (f federation) syncPayloads() {
	f.t.Helper()
	ctx := context.Background()
	hub, cell := f.hub.Resource(PayloadResource).Namespace(namespace), f.cell.Resource(PayloadResource).Namespace(namespace)
	list, err := hub.List(ctx, metav1.ListOptions{LabelSelector: RoleLabel + "=" + RolePayload + "," + CellLabel + "=" + f.clusterFor})
	if err != nil {
		f.t.Fatal(err)
	}
	keep := map[string]bool{}
	for index := range list.Items {
		object := list.Items[index]
		keep[object.GetName()] = true
		if _, err := cell.Get(ctx, object.GetName(), metav1.GetOptions{}); err == nil {
			continue
		}
		copied := &unstructured.Unstructured{Object: map[string]any{"apiVersion": object.GetAPIVersion(), "kind": PayloadKind, "metadata": map[string]any{"name": object.GetName(), "namespace": namespace, "labels": toAny(object.GetLabels())}, "spec": object.Object["spec"]}}
		if _, err := cell.Create(ctx, copied, metav1.CreateOptions{}); err != nil {
			f.t.Fatal(err)
		}
	}
	existing, err := cell.List(ctx, metav1.ListOptions{})
	if err != nil {
		f.t.Fatal(err)
	}
	for _, object := range existing.Items {
		if !keep[object.GetName()] {
			_ = cell.Delete(ctx, object.GetName(), metav1.DeleteOptions{})
		}
	}
}

func toAny(labels map[string]string) map[string]any {
	out := map[string]any{}
	for key, value := range labels {
		out[key] = value
	}
	return out
}

type rig struct {
	t        *testing.T
	ctx      context.Context
	hub      *Hub
	cell     *Cell
	fed      federation
	store    *site.Store
	results  *site.Results
	runner   *site.Runner
	snapshot ops.DeviceSnapshot
}

func newFixture(t *testing.T) *rig {
	hubAPI, cellAPI := client(), client()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := site.OpenStore(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := ops.DeviceSnapshot{BMCEndpoint: "https://[2001:db8::1]", SystemUUID: "00000000-0000-4000-8000-000000000001", BootMACAddress: "02:00:00:00:00:01", BMCPublicKeySHA256: keyDigest}
	enrollment := fixture.Enrollment{device: {BMCEndpoint: snapshot.BMCEndpoint, SystemUUID: snapshot.SystemUUID, BootMACAddress: snapshot.BootMACAddress, BMCPublicKeySHA256: keyDigest, Disks: []string{"disk-0"}}}
	results := site.NewResults(store)
	return &rig{
		t: t, ctx: context.Background(), store: store, results: results, snapshot: snapshot,
		hub:    &Hub{API: hubAPI, Namespace: namespace, Cell: cellName, MaxAge: time.Minute},
		cell:   &Cell{API: cellAPI, Namespace: namespace, Name: cellName},
		fed:    federation{t: t, hub: hubAPI, cell: cellAPI, clusterFor: cellName},
		runner: &site.Runner{Site: cellName, Store: store, Results: results, Access: fixture.Access{Enrollment: enrollment}, Kinds: map[string]site.Kind{"DeviceWipe": simwork.Wipe{}}, Poll: 10 * time.Millisecond},
	}
}

func (f *rig) wipe(fence int64, uid string) site.Assignment {
	spec := ops.DeviceWipeSpec{OperationEnvelope: ops.OperationEnvelope{Creator: ops.Creator{Username: "release", UID: "u"}, DeviceRef: ops.DeviceReference{Name: "server-example-001"}, Authority: ops.Authority{AllocationRef: &ops.LocalReference{Name: "alloc-example"}}},
		Step: 1, CleaningPolicyDigest: "sha256:" + keyDigest, TargetDisks: []string{"disk-0"}}
	inputs, _ := json.Marshal(spec)
	digest, _ := custody.SpecDigest(spec)
	now := time.Now().UTC()
	return site.Assignment{Device: device, Operation: ops.OperationReference{Kind: "DeviceWipe", Name: "alloc-example-wipe-1-" + uid}, Fence: fence, AdmittedAt: now, Deadline: now.Add(time.Minute), Snapshot: f.snapshot, InputDigest: digest, Inputs: inputs}
}

func (f *rig) observe() {
	f.t.Helper()
	list, err := f.cell.api().List(f.ctx, metav1.ListOptions{})
	if err != nil {
		f.t.Fatal(err)
	}
	for index := range list.Items {
		if err := f.cell.Publish(f.ctx, &list.Items[index], f.store, f.results); err != nil {
			f.t.Fatal(err)
		}
	}
	f.fed.sync()
}

// TestAssignmentRoundTrip delivers an assignment through the federation,
// runs it at the cell, reports its result back, and answers it.
func TestAssignmentRoundTrip(t *testing.T) {
	f := newFixture(t)
	if _, reported, err := f.hub.Fence(f.ctx, device); err != nil || reported {
		t.Fatalf("an unobserved report counts: %v %v", reported, err)
	}
	f.fed.sync()
	f.observe()
	if report, reported, err := f.hub.Fence(f.ctx, device); err != nil || !reported || report != (custody.SiteFence{}) {
		t.Fatalf("report = %+v %v %v", report, reported, err)
	}
	assignment := f.wipe(3, "00000000-0000-4000-8000-000000000101")
	if err := f.hub.Deliver(f.ctx, assignment, nil); err != nil {
		t.Fatal(err)
	}
	// Delivery is idempotent; other contents under the same name conflict.
	if err := f.hub.Deliver(f.ctx, assignment, nil); err != nil {
		t.Fatal(err)
	}
	changed := assignment
	changed.Deadline = changed.Deadline.Add(time.Second)
	if err := f.hub.Deliver(f.ctx, changed, nil); err != ErrConflict {
		t.Fatalf("changed delivery err = %v", err)
	}
	f.fed.sync()
	if err := f.cell.Work(f.ctx, AssignmentName(assignment), f.runner); err != nil {
		t.Fatal(err)
	}
	f.observe()
	held, err := f.hub.Results(f.ctx)
	if err != nil || len(held) != 1 || held[0].Result.Phase != ops.PhaseSucceeded || held[0].Result.Fence != 3 || held[0].Device != device {
		t.Fatalf("results = %+v, %v", held, err)
	}
	if report, _, _ := f.hub.Fence(f.ctx, device); report.Seen != 3 || report.Stopped != 3 {
		t.Fatalf("fence after the wipe = %+v", report)
	}
	if err := f.hub.Forget(f.ctx, held[0]); err != nil {
		t.Fatal(err)
	}
	f.fed.sync()
	f.observe()
	if _, found, _ := f.results.Get(device, assignment.Operation, 3); found {
		t.Fatal("the cell kept a result the coordinator answered")
	}
	if held, _ := f.hub.Results(f.ctx); len(held) != 0 {
		t.Fatalf("answered results are offered again: %+v", held)
	}
}

// TestReflectionIsRejectedUnlessItIsThisCellsObservation covers each way a
// reflected status can be stale, foreign or for other inputs.
func TestReflectionIsRejectedUnlessItIsThisCellsObservation(t *testing.T) {
	cases := map[string]func(*HubStatus, *Spec){
		"another cluster":  func(s *HubStatus, _ *Spec) { s.Reflected[0].ClusterName = "cell-example-b" },
		"another cell":     func(s *HubStatus, _ *Spec) { s.Reflected[0].Observation.Cell = "cell-example-b" },
		"two cells":        func(s *HubStatus, _ *Spec) { s.Reflected = append(s.Reflected, s.Reflected[0]) },
		"none":             func(s *HubStatus, _ *Spec) { s.Reflected = nil },
		"future":           func(s *HubStatus, _ *Spec) { s.Reflected[0].Observation.ObservedAt = time.Now().Add(time.Hour) },
		"other inputs":     func(s *HubStatus, _ *Spec) { s.Reflected[0].Observation.AssignmentDigest = keyDigest },
		"another fence":    func(s *HubStatus, _ *Spec) { s.Reflected[0].Observation.Result.Fence++ },
		"another op":       func(s *HubStatus, _ *Spec) { s.Reflected[0].Observation.Result.Operation.Name = "other" },
		"another device":   func(s *HubStatus, _ *Spec) { s.Reflected[0].Observation.Result.Device = "server-example-002" },
		"spec of another":  func(_ *HubStatus, spec *Spec) { spec.Cell = "cell-example-b" },
		"no observed time": func(s *HubStatus, _ *Spec) { s.Reflected[0].Observation.ObservedAt = time.Time{} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			assignment := f.wipe(2, "00000000-0000-4000-8000-000000000102")
			data, _ := json.Marshal(assignment)
			spec := Spec{Cell: cellName, Device: device, AssignmentJSON: string(data)}
			result := custody.SiteResult{Site: cellName, Device: "server-example-001", Operation: assignment.Operation, Fence: 2, Phase: ops.PhaseSucceeded}
			status := HubStatus{Reflected: []Reflected{{ClusterName: cellName, Observation: Observation{Cell: cellName, AssignmentDigest: Digest(spec.AssignmentJSON), ObservedAt: time.Now().Add(-time.Second), Result: &result}}}}
			// The unchanged reflection is accepted, so each case fails only
			// for its one change.
			if got := f.reflect(t, assignment, spec, status); len(got) != 1 {
				t.Fatalf("a valid reflection was rejected: %+v", got)
			}
			mutate(&status, &spec)
			if got := f.reflect(t, assignment, spec, status); len(got) != 0 {
				t.Fatalf("accepted %+v", got)
			}
		})
	}
}

func (f *rig) reflect(t *testing.T, assignment site.Assignment, spec Spec, status HubStatus) []site.Stored {
	t.Helper()
	api := f.hub.api()
	_ = api.Delete(f.ctx, AssignmentName(assignment), metav1.DeleteOptions{})
	object := newObject(namespace, AssignmentName(assignment), RoleAssignment, Spec{Cell: cellName, Device: spec.Device, AssignmentJSON: spec.AssignmentJSON})
	data, _ := json.Marshal(spec)
	var fields map[string]any
	_ = json.Unmarshal(data, &fields)
	object.Object["spec"] = fields
	created, err := api.Create(f.ctx, object, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	data, _ = json.Marshal(status)
	var raw map[string]any
	_ = json.Unmarshal(data, &raw)
	created.Object["status"] = raw
	if _, err := api.UpdateStatus(f.ctx, created, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	held, err := f.hub.Results(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	return held
}

// TestAStaleFenceReportIsNoReport keeps admission from deciding on an old
// fence.
func TestAStaleFenceReportIsNoReport(t *testing.T) {
	f := newFixture(t)
	if _, _, err := f.hub.Fence(f.ctx, device); err != nil {
		t.Fatal(err)
	}
	f.fed.sync()
	f.observe()
	f.hub.Now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if _, reported, err := f.hub.Fence(f.ctx, device); err != nil || reported {
		t.Fatalf("a stale report counts: %v %v", reported, err)
	}
}

// TestTheCellRunsOnlyItsOwnAssignments refuses a handoff for another cell
// or under another name, before anything is sent.
func TestTheCellRunsOnlyItsOwnAssignments(t *testing.T) {
	f := newFixture(t)
	assignment := f.wipe(4, "00000000-0000-4000-8000-000000000104")
	data, _ := json.Marshal(assignment)
	for name, object := range map[string]*unstructured.Unstructured{
		"another cell": newObject(namespace, AssignmentName(assignment), RoleAssignment, Spec{Cell: "cell-example-b", Device: device, AssignmentJSON: string(data), Credential: site.CredentialSecretName(device)}),
		// The worker would be granted get on another Device's credential.
		"another credential": newObject(namespace, AssignmentName(assignment), RoleAssignment, Spec{Cell: cellName, Device: device, AssignmentJSON: string(data), Credential: site.CredentialSecretName("fleet-provider/server-example-002")}),
		"another name":       newObject(namespace, "op-"+fmt.Sprintf("%040d", 1), RoleAssignment, Spec{Cell: cellName, Device: device, AssignmentJSON: string(data), Credential: site.CredentialSecretName(device)}),
		"a report":           newObject(namespace, ReportName(device)+"x", RoleReport, Spec{Cell: cellName, Device: device, AssignmentJSON: string(data)}),
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			if _, err := f.cell.api().Create(f.ctx, object, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			if err := f.cell.Work(f.ctx, object.GetName(), f.runner); err != ErrNotDelivered {
				t.Fatalf("err = %v", err)
			}
			if journal, _ := f.store.Journal(device); len(journal) != 0 {
				t.Fatalf("journal = %+v", journal)
			}
		})
	}
}

// sealedWipe is a wipe assignment whose admitted inputs name a sealed
// payload, and the payload.
func (f *rig) sealedWipe(fence int64, uid string) (site.Assignment, site.Payload) {
	assignment := f.wipe(fence, uid)
	salt := make([]byte, 32)
	data := []byte(`{"userData":"#cloud-config\n"}`)
	hash := sha256.New()
	hash.Write(salt)
	hash.Write(data)
	payload := site.Payload{Ref: "sealed-example", Digest: "sha256:" + hex.EncodeToString(hash.Sum(nil)), Salt: salt, Data: data}
	var inputs map[string]any
	_ = json.Unmarshal(assignment.Inputs, &inputs)
	inputs["bootstrap"] = map[string]string{"ref": payload.Ref, "digest": payload.Digest}
	assignment.Inputs, _ = json.Marshal(inputs)
	sum := sha256.Sum256(assignment.Inputs)
	assignment.InputDigest = "sha256:" + hex.EncodeToString(sum[:])
	return assignment, payload
}

// TestFederationCarriesAPayloadBesideItsHandoff: the hub writes the payload
// object before the handoff that names it; a worker that runs before the
// object reaches the cell stores nothing and is retried; the worker reads
// it; and answering the result deletes the hub's copy and so the cell's.
func TestFederationCarriesAPayloadBesideItsHandoff(t *testing.T) {
	f := newFixture(t)
	payloads := HandoffPayloads{API: f.cell.API, Namespace: namespace, Cell: cellName}
	f.runner.Payloads = payloads
	assignment, payload := f.sealedWipe(3, "00000000-0000-4000-8000-000000000121")
	if err := f.hub.Deliver(f.ctx, assignment, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("an assignment naming a payload was delivered without it: %v", err)
	}
	altered := payload
	altered.Data = []byte(`{"userData":"other"}`)
	if err := f.hub.Deliver(f.ctx, assignment, &altered); !errors.Is(err, ErrConflict) {
		t.Fatalf("an altered payload was delivered: %v", err)
	}
	if err := f.hub.Deliver(f.ctx, assignment, &payload); err != nil {
		t.Fatal(err)
	}
	if err := f.hub.Deliver(f.ctx, assignment, &payload); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	handoff, err := f.hub.api().Get(f.ctx, AssignmentName(assignment), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if spec, _ := decode(handoff, nil); spec.Payload != PayloadName(assignment) || strings.Contains(spec.AssignmentJSON, string(payload.Data)) {
		t.Fatalf("handoff spec = %+v", spec)
	}
	// The handoff arrives first: the worker sends nothing and stores
	// nothing.
	f.fed.sync()
	if err := f.cell.Work(f.ctx, AssignmentName(assignment), f.runner); !errors.Is(err, ErrPayloadPending) {
		t.Fatalf("err = %v", err)
	}
	if _, found, _ := f.results.Get(device, assignment.Operation, assignment.Fence); found {
		t.Fatal("a result was stored without the payload")
	}
	f.fed.syncPayloads()
	if err := f.cell.Work(f.ctx, AssignmentName(assignment), f.runner); err != nil {
		t.Fatal(err)
	}
	f.observe()
	f.fed.sync()
	held, err := f.hub.Results(f.ctx)
	if err != nil || len(held) != 1 {
		t.Fatalf("results = %+v, %v", held, err)
	}
	if err := f.hub.Forget(f.ctx, held[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.hub.payloads().Get(f.ctx, PayloadName(assignment), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the hub kept the payload: %v", err)
	}
	f.fed.syncPayloads()
	if got, err := payloads.Get(f.ctx, assignment); err != nil || got != nil {
		t.Fatalf("the cell kept its copy: %v, %v", got, err)
	}
}

// TestACellRefusesAHandoffNamingAnotherPayload: the worker would be granted
// get on whatever payload the handoff names.
func TestACellRefusesAHandoffNamingAnotherPayload(t *testing.T) {
	f := newFixture(t)
	assignment, _ := f.sealedWipe(3, "00000000-0000-4000-8000-000000000131")
	data, _ := json.Marshal(assignment)
	for name, spec := range map[string]Spec{
		"another Secret": {Cell: cellName, Device: device, AssignmentJSON: string(data), Credential: site.CredentialSecretName(device), Payload: "bmc-0123456789abcdef0123"},
		"no Secret":      {Cell: cellName, Device: device, AssignmentJSON: string(data), Credential: site.CredentialSecretName(device)},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			if _, err := f.cell.api().Create(f.ctx, newObject(namespace, AssignmentName(assignment), RoleAssignment, spec), metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			if err := f.cell.Work(f.ctx, AssignmentName(assignment), f.runner); !errors.Is(err, ErrNotDelivered) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// TestTheHubSweepsPayloadsNoOperationNeeds deletes every payload of its
// cell not in the keep set: an operation that ended without an answered
// result, or a payload written before a crash left it without a handoff.
// It keeps the payloads still needed and touches no other cell's.
func TestTheHubSweepsPayloadsNoOperationNeeds(t *testing.T) {
	f := newFixture(t)
	kept, keptPayload := f.sealedWipe(3, "00000000-0000-4000-8000-000000000141")
	ended, endedPayload := f.sealedWipe(4, "00000000-0000-4000-8000-000000000142")
	for _, pair := range []struct {
		a site.Assignment
		p site.Payload
	}{{kept, keptPayload}, {ended, endedPayload}} {
		if err := f.hub.Deliver(f.ctx, pair.a, &pair.p); err != nil {
			t.Fatal(err)
		}
	}
	other := &Hub{API: f.hub.API, Namespace: namespace, Cell: "cell-example-b", MaxAge: time.Minute}
	foreign, foreignPayload := f.sealedWipe(5, "00000000-0000-4000-8000-000000000143")
	if err := other.Deliver(f.ctx, foreign, &foreignPayload); err != nil {
		t.Fatal(err)
	}
	if err := f.hub.SweepPayloads(f.ctx, []site.Assignment{kept}); err != nil {
		t.Fatal(err)
	}
	exists := func(a site.Assignment) bool {
		_, err := f.hub.payloads().Get(f.ctx, PayloadName(a), metav1.GetOptions{})
		return err == nil
	}
	if !exists(kept) || exists(ended) || !exists(foreign) {
		t.Fatalf("kept %v, ended %v, other cell %v", exists(kept), exists(ended), exists(foreign))
	}
	// The next delivery writes a swept payload again, so a sweep racing a
	// delivery leaves the worker waiting, not failed.
	if err := f.hub.Deliver(f.ctx, ended, &endedPayload); err != nil || !exists(ended) {
		t.Fatalf("redelivery after a sweep: %v", err)
	}
}

// TestOnlyAMissingPayloadIsPending: a worker retries only while the payload
// has not arrived. A read it may not make is an error, and a payload that
// does not decode, or names another cell, refuses the handoff.
func TestOnlyAMissingPayloadIsPending(t *testing.T) {
	assignment, payload := newFixture(t).sealedWipe(3, "00000000-0000-4000-8000-000000000151")
	cases := map[string]struct {
		mutate func(f *rig)
		want   error
	}{
		"a payload that does not decode": {func(f *rig) {
			object, _ := f.cell.API.Resource(PayloadResource).Namespace(namespace).Get(f.ctx, PayloadName(assignment), metav1.GetOptions{})
			_ = unstructured.SetNestedField(object.Object, "not base64!", "spec", "payload")
			_, _ = f.cell.API.Resource(PayloadResource).Namespace(namespace).Update(f.ctx, object, metav1.UpdateOptions{})
		}, ErrNotDelivered},
		"a payload for another cell": {func(f *rig) {
			object, _ := f.cell.API.Resource(PayloadResource).Namespace(namespace).Get(f.ctx, PayloadName(assignment), metav1.GetOptions{})
			_ = unstructured.SetNestedField(object.Object, "cell-example-b", "spec", "cell")
			_, _ = f.cell.API.Resource(PayloadResource).Namespace(namespace).Update(f.ctx, object, metav1.UpdateOptions{})
		}, ErrNotDelivered},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.runner.Payloads = HandoffPayloads{API: f.cell.API, Namespace: namespace, Cell: cellName}
			if err := f.hub.Deliver(f.ctx, assignment, &payload); err != nil {
				t.Fatal(err)
			}
			f.fed.sync()
			f.fed.syncPayloads()
			test.mutate(f)
			if err := f.cell.Work(f.ctx, AssignmentName(assignment), f.runner); !errors.Is(err, test.want) {
				t.Fatalf("err = %v, want %v", err, test.want)
			}
		})
	}
	t.Run("a read the worker may not make", func(t *testing.T) {
		f := newFixture(t)
		f.runner.Payloads = failingPayloads{errors.New("forbidden")}
		if err := f.hub.Deliver(f.ctx, assignment, &payload); err != nil {
			t.Fatal(err)
		}
		f.fed.sync()
		if err := f.cell.Work(f.ctx, AssignmentName(assignment), f.runner); err == nil || errors.Is(err, ErrPayloadPending) {
			t.Fatalf("err = %v, want the read error", err)
		}
	})
}

type failingPayloads struct{ err error }

func (p failingPayloads) Get(context.Context, site.Assignment) (*site.Payload, error) {
	return nil, p.err
}
func (failingPayloads) Delete(context.Context, site.Assignment) error { return nil }
func (failingPayloads) Prune(context.Context, string) error           { return nil }
