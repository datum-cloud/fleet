package standalone

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	"go.datum.net/fleet/internal/executor"
	"go.datum.net/fleet/internal/simulator"
)

type unavailableResultExecutor struct {
	storeExecutor
	beforeEffect func(simulator.Request)
}

type changedHistoryExecutor struct {
	storeExecutor
	change func(simulator.OperationResult) (simulator.OperationResult, bool, error)
}

func (e changedHistoryExecutor) Lookup(ctx context.Context, operationID string) (simulator.OperationResult, bool, error) {
	result, _, err := e.storeExecutor.Lookup(ctx, operationID)
	if err != nil {
		return result, false, err
	}
	return e.change(result)
}

func TestCompletedInstallationRequiresSimulatorHistory(t *testing.T) {
	for _, phase := range []string{PhaseInstall, PhaseReinstall} {
		for _, corruption := range []string{"missing", "unavailable", "identity", "inputs", "kind", "phase", "result", "completion"} {
			t.Run(phase+"/"+corruption, func(t *testing.T) {
				runner, store, _ := reinstallFixture(t)
				ctx := context.Background()
				if err := runner.Run(ctx, phase); err != nil {
					t.Fatal(err)
				}
				before := store.Snapshot()
				runner.Executor = changedHistoryExecutor{storeExecutor{store}, func(result simulator.OperationResult) (simulator.OperationResult, bool, error) {
					switch corruption {
					case "missing":
						return simulator.OperationResult{}, false, nil
					case "unavailable":
						return simulator.OperationResult{}, false, executor.ErrOutcomeUnknown
					case "identity":
						result.ID = "different-operation"
					case "inputs":
						result.InputDigest = "different-retained-request"
					case "kind":
						result.Kind = "cleanup"
					case "phase":
						result.Phase = "Pending"
					case "result":
						result.Result = "clean"
					case "completion":
						result.CompletedAt = nil
					}
					return result, true, nil
				}}
				if err := runner.Run(ctx, phase); !errors.Is(err, executor.ErrOutcomeUnknown) {
					t.Fatalf("unverified replay = %v", err)
				}
				claim, _, _, err := runner.claim(ctx, runner.Options.FirstNamespace)
				if err != nil {
					t.Fatal(err)
				}
				if !apiMeta.IsStatusConditionPresentAndEqual(claim.Status.Conditions, "Ready", metav1.ConditionUnknown) {
					t.Fatal("unverified history left machine ready")
				}
				if !reflect.DeepEqual(before, store.Snapshot()) {
					t.Fatal("history verification executed a new effect")
				}
				assertWaitingConsumerBlocked(t, runner)
			})
		}
	}
}

func (e unavailableResultExecutor) Execute(ctx context.Context, request simulator.Request) (simulator.OperationResult, error) {
	if e.beforeEffect != nil {
		e.beforeEffect(request)
	}
	if _, err := e.storeExecutor.Execute(ctx, request); err != nil {
		return simulator.OperationResult{}, err
	}
	return simulator.OperationResult{}, executor.ErrOutcomeUnknown
}

func (e unavailableResultExecutor) Lookup(context.Context, string) (simulator.OperationResult, bool, error) {
	return simulator.OperationResult{}, false, executor.ErrOutcomeUnknown
}

func reinstallFixture(t *testing.T) (Runner, *simulator.Store, string) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := fleetv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	options := DefaultOptions()
	objects := append(fixtureObjects(options), &fleetv1.Installation{
		ObjectMeta: metav1.ObjectMeta{Namespace: options.FirstNamespace, Name: options.ReinstallationName, UID: "reinstallation-uid", Generation: 1},
		Spec:       fleetv1.InstallationSpec{MachineRef: fleetv1.MachineReference{Name: options.ClaimName}, ExpectedPredecessor: fleetv1.InstallationPredecessor{Type: "Installation", Name: options.InstallationName}, ImageRef: fleetv1.LocalReference{Name: options.ImageName}},
	})
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithStatusSubresource(&fleetv1.MachineCapacity{}, &fleetv1.Machine{}, &fleetv1.Installation{}).Build()
	path := t.TempDir() + "/state.json"
	store, err := simulator.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	runner := Runner{Client: kube, Executor: storeExecutor{store}, Options: options, Now: time.Now}
	for _, phase := range []string{PhaseAllocate, PhaseInstall} {
		if err := runner.Run(context.Background(), phase); err != nil {
			t.Fatal(err)
		}
	}
	return runner, store, path
}

func retainedAsset(t *testing.T, runner Runner) controller.AssetRecord {
	t.Helper()
	var secrets corev1.SecretList
	if err := runner.Client.List(context.Background(), &secrets, client.InNamespace(runner.Options.PrivateNamespace), client.MatchingLabels{"fleet.datumapis.com/private-record": "asset"}); err != nil {
		t.Fatal(err)
	}
	if len(secrets.Items) != 1 {
		t.Fatalf("retained assets = %d, want 1", len(secrets.Items))
	}
	var record controller.AssetRecord
	if err := json.Unmarshal(secrets.Items[0].Data["record.json"], &record); err != nil {
		t.Fatal(err)
	}
	return record
}

func assertWaitingConsumerBlocked(t *testing.T, runner Runner) {
	t.Helper()
	ctx := context.Background()
	coordinator, err := controller.NewCoordinator(runner.Client, runner.Options.PrivateNamespace, runner.Executor, controller.LocalDigestImageResolver{}, nil, runner.Options.EvidenceAge)
	if err != nil {
		t.Fatal(err)
	}
	claim, namespace, admission, err := runner.claim(ctx, runner.Options.SecondNamespace)
	if err != nil {
		t.Fatal(err)
	}
	var class fleetv1.MachineClass
	var profile fleetv1.MachineProfile
	var machine fleetv1.MachineCapacity
	for _, object := range []client.Object{&class, &profile, &machine} {
		key := client.ObjectKey{}
		switch object.(type) {
		case *fleetv1.MachineClass:
			key.Name = runner.Options.ClassName
		case *fleetv1.MachineProfile:
			key.Name = runner.Options.ProfileName
		default:
			key = client.ObjectKey{Namespace: runner.Options.ProviderNamespace, Name: runner.Options.MachineName}
		}
		if err := runner.Client.Get(ctx, key, object); err != nil {
			t.Fatal(err)
		}
	}
	if apiMeta.IsStatusConditionTrue(machine.Status.Conditions, "Available") {
		t.Fatal("reinstall exposed occupied capacity as available")
	}
	apiMeta.SetStatusCondition(&machine.Status.Conditions, condition("Available", metav1.ConditionTrue, machine.Generation, "SyntheticStaleAvailability", "Synthetic stale projection cannot override custody."))
	if _, err := coordinator.Allocate(ctx, allocationRequest(claim, namespace, admission, &class, &profile, &machine, time.Now())); !errors.Is(err, controller.ErrInsufficientCapacity) {
		t.Fatalf("waiting allocation with stale Available=True: %v", err)
	}
}

func TestReinstallRecoversExactOperationAndRejectsStalePredecessor(t *testing.T) {
	ctx := context.Background()
	runner, store, path := reinstallFixture(t)
	before := retainedAsset(t, runner)
	var operationID string
	runner.Executor = unavailableResultExecutor{storeExecutor: storeExecutor{store}, beforeEffect: func(request simulator.Request) {
		asset := retainedAsset(t, runner)
		if asset.ActiveOperationID != request.ID || asset.CurrentInstallationName != runner.Options.InstallationName || asset.BindingID != before.BindingID || asset.Epoch != before.Epoch {
			t.Fatalf("effect lacks retained predecessor/authority: %#v", asset)
		}
		var secrets corev1.SecretList
		if err := runner.Client.List(ctx, &secrets, client.InNamespace(runner.Options.PrivateNamespace)); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, secret := range secrets.Items {
			var operation controller.InstallationRecord
			if json.Unmarshal(secret.Data["record.json"], &operation) == nil && operation.OperationID == request.ID {
				found = operation.InstallationUID == "reinstallation-uid" && operation.Predecessor == runner.Options.InstallationName && operation.BindingID == before.BindingID && operation.ImageName == runner.Options.ImageName && operation.Admission.DecisionID == "standalone-install-reinstallation-uid"
			}
		}
		if !found {
			t.Fatal("exact reinstall inputs not retained before effect")
		}
		operationID = request.ID
	}}
	if err := runner.Run(ctx, PhaseReinstall); !errors.Is(err, executor.ErrOutcomeUnknown) {
		t.Fatalf("lost result = %v", err)
	}
	claim, _, _, err := runner.claim(ctx, runner.Options.FirstNamespace)
	if err != nil {
		t.Fatal(err)
	}
	if !apiMeta.IsStatusConditionPresentAndEqual(claim.Status.Conditions, "Ready", metav1.ConditionUnknown) || claim.Status.CurrentInstallationRef.Name != runner.Options.InstallationName {
		t.Fatalf("unknown reinstall advanced readiness: %#v", claim.Status)
	}
	assertWaitingConsumerBlocked(t, runner)
	reopened, err := simulator.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	restarted := Runner{Client: runner.Client, Executor: storeExecutor{reopened}, Options: runner.Options, Now: time.Now}
	for range 2 {
		if err := restarted.Run(ctx, PhaseReinstall); err != nil {
			t.Fatal(err)
		}
	}
	after := retainedAsset(t, restarted)
	if after.BindingID != before.BindingID || after.Epoch != before.Epoch || after.ClaimUID != before.ClaimUID || after.CurrentInstallationName != runner.Options.ReinstallationName || after.ActiveOperationID != "" {
		t.Fatalf("reinstall changed custody or failed completion: %#v", after)
	}
	snapshot := reopened.Snapshot()
	if len(snapshot.Operations) != 2 || snapshot.Operations[operationID] == nil || !reflect.DeepEqual(snapshot.Operations[operationID], store.Snapshot().Operations[operationID]) {
		t.Fatal("retry/restart changed the retained effect")
	}
	assertWaitingConsumerBlocked(t, restarted)
	claim, _, _, err = restarted.claim(ctx, runner.Options.FirstNamespace)
	if err != nil {
		t.Fatal(err)
	}
	var installed fleetv1.Installation
	if err := runner.Client.Get(ctx, client.ObjectKey{Namespace: runner.Options.FirstNamespace, Name: runner.Options.ReinstallationName}, &installed); err != nil {
		t.Fatal(err)
	}
	if !apiMeta.IsStatusConditionTrue(claim.Status.Conditions, "Ready") || claim.Status.CurrentInstallationRef.Name != installed.Name || installed.Status.Phase != "Ready" || installed.Status.CompletedAt == nil {
		t.Fatal("verified reinstall did not project synthetic completion")
	}
	stale := &fleetv1.Installation{ObjectMeta: metav1.ObjectMeta{Namespace: runner.Options.FirstNamespace, Name: "stale-reinstall", UID: "stale-uid"}, Spec: fleetv1.InstallationSpec{MachineRef: fleetv1.MachineReference{Name: runner.Options.ClaimName}, ExpectedPredecessor: fleetv1.InstallationPredecessor{Type: "Installation", Name: runner.Options.InstallationName}, ImageRef: fleetv1.LocalReference{Name: runner.Options.ImageName}}}
	if err := runner.Client.Create(ctx, stale); err != nil {
		t.Fatal(err)
	}
	staleRunner := restarted
	staleRunner.Options.InstallationName = stale.Name
	if err := staleRunner.Run(ctx, PhaseInstall); !errors.Is(err, controller.ErrPredecessorConflict) {
		t.Fatalf("stale predecessor accepted: %v", err)
	}
	if !reflect.DeepEqual(snapshot, reopened.Snapshot()) {
		t.Fatal("stale predecessor produced a simulator effect")
	}
	if err := restarted.Run(ctx, PhaseReinstall); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Run(ctx, PhaseRelease); err != nil {
		t.Fatal(err)
	}
	if len(reopened.Snapshot().Operations) != 3 {
		t.Fatal("release did not retain exactly two installs and one cleanup")
	}
	assertRetainedOwner(t, runner.Client, runner.Options, "claim-b-uid", 2)
}

func TestUnknownReinstallBlocksReleaseAndCapacityReuse(t *testing.T) {
	runner, store, _ := reinstallFixture(t)
	runner.Executor = unavailableResultExecutor{storeExecutor: storeExecutor{store}}
	if err := runner.Run(context.Background(), PhaseReinstall); !errors.Is(err, executor.ErrOutcomeUnknown) {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), PhaseRelease); !errors.Is(err, executor.ErrOutcomeUnknown) {
		t.Fatalf("release with unresolved reinstall = %v", err)
	}
	assertRetainedOwner(t, runner.Client, runner.Options, "claim-a-uid", 1)
	assertWaitingConsumerBlocked(t, runner)
	if len(store.Snapshot().Operations) != 2 {
		t.Fatal("unknown reinstall allowed premature cleanup")
	}
}

func TestConcurrentReinstallConvergesWithoutDuplicateEffects(t *testing.T) {
	runner, store, _ := reinstallFixture(t)
	var workers sync.WaitGroup
	results := make(chan error, 4)
	for range cap(results) {
		workers.Go(func() { results <- runner.Run(context.Background(), PhaseReinstall) })
	}
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil && !apierrors.IsConflict(err) && !errors.Is(err, controller.ErrPredecessorConflict) {
			t.Fatalf("unexpected concurrent outcome: %v", err)
		}
	}
	if err := runner.Run(context.Background(), PhaseReinstall); err != nil {
		t.Fatal(err)
	}
	if len(store.Snapshot().Operations) != 2 {
		t.Fatal("concurrent retries duplicated installation effects")
	}
	assertRetainedOwner(t, runner.Client, runner.Options, "claim-a-uid", 1)
}

func TestReinstallRequiresBoundedSuccessorFixture(t *testing.T) {
	for _, variant := range []string{"missing", "same-name", "wrong-predecessor"} {
		t.Run(variant, func(t *testing.T) {
			runner, store, _ := reinstallFixture(t)
			switch variant {
			case "missing":
				runner.Options.ReinstallationName = ""
			case "same-name":
				runner.Options.ReinstallationName = runner.Options.InstallationName
			case "wrong-predecessor":
				var installation fleetv1.Installation
				if err := runner.Client.Get(context.Background(), client.ObjectKey{Namespace: runner.Options.FirstNamespace, Name: runner.Options.ReinstallationName}, &installation); err != nil {
					t.Fatal(err)
				}
				installation.Spec.ExpectedPredecessor = fleetv1.InstallationPredecessor{Type: "None"}
				if err := runner.Client.Update(context.Background(), &installation); err != nil {
					t.Fatal(err)
				}
			}
			if err := runner.Run(context.Background(), PhaseReinstall); err == nil {
				t.Fatal("invalid successor fixture accepted")
			}
			if len(store.Snapshot().Operations) != 1 {
				t.Fatal("invalid successor produced an effect")
			}
		})
	}
}
