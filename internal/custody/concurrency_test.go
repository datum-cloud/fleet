package custody

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// client returns a separate client, so concurrent callers use separate
// connections.
func (e *env) client() client.Client {
	e.t.Helper()
	api, err := client.New(e.config, client.Options{Scheme: e.scheme})
	if err != nil {
		e.t.Fatal(err)
	}
	return api
}

// settleQuiet reconciles until nothing changes, returning the first error.
func settleQuiet(ctx context.Context, coord *Coordinator, name string) error {
	for range 60 {
		outcome, err := coord.Reconcile(ctx, name)
		if err != nil {
			return err
		}
		if !outcome.Changed {
			return nil
		}
	}
	return errors.New("did not settle")
}

func TestConcurrency(t *testing.T) {
	e := startEnv(t)
	n := 400
	fresh := func() *inventory.Device {
		n++
		return e.verified(fmt.Sprintf("server-example-%03d", n), uid(n), fmt.Sprintf("%064x", n))
	}

	t.Run("two allocators race for one server and exactly one wins", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		var wins atomic.Int32
		var group sync.WaitGroup
		for index := range 8 {
			allocator := e.coordinator(e.client())
			group.Add(1)
			go func() {
				defer group.Done()
				err := allocator.Claim(e.ctx, device.Name, fmt.Sprintf("alloc-race-%d", index))
				switch {
				case err == nil:
					wins.Add(1)
				case !errors.Is(err, ErrUnavailable):
					t.Errorf("claim: %v", err)
				}
			}()
		}
		group.Wait()
		if wins.Load() != 1 || e.custody(device.Name).Spec.Holder.Type != ops.HolderAllocation {
			t.Fatalf("%d allocators won", wins.Load())
		}
	})

	t.Run("an allocator races a staff operation and exactly one takes the server", func(t *testing.T) {
		e := e.sub(t)
		for range 5 {
			device := fresh()
			operation := e.staffOp(device, "ok")
			coordinator := e.coordinator(e.client())
			allocator := e.coordinator(e.client())
			var claimErr, reconcileErr error
			var group sync.WaitGroup
			group.Add(2)
			go func() { defer group.Done(); reconcileErr = settleQuiet(e.ctx, coordinator, device.Name) }()
			go func() { defer group.Done(); claimErr = allocator.Claim(e.ctx, device.Name, "alloc-staff-race") }()
			group.Wait()
			if reconcileErr != nil {
				t.Fatal(reconcileErr)
			}
			e.settle(device.Name)
			status := e.status(operation)
			custody := e.custody(device.Name)
			switch {
			case claimErr == nil:
				if status.Phase != ops.PhaseFailed || status.Reason != ReasonHolderMismatch || custody.Spec.Holder.Type != ops.HolderAllocation || custody.Spec.ActiveOperation != nil {
					t.Fatalf("claim won but the staff operation is %s/%s and custody %+v", status.Phase, status.Reason, custody.Spec)
				}
			case errors.Is(claimErr, ErrUnavailable):
				if status.Phase != ops.PhaseAdmitted || custody.Spec.Holder.Type != ops.HolderService {
					t.Fatalf("staff won but the operation is %s and custody %+v", status.Phase, custody.Spec)
				}
			default:
				t.Fatal(claimErr)
			}
		}
	})

	t.Run("two coordinators admit from one custody exactly once", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		before := e.custody(device.Name).Spec.Fence
		var operations []*DeviceTestStaffOperation
		for range 3 {
			operations = append(operations, e.staffOp(device, "ok"))
		}
		var group sync.WaitGroup
		for range 2 {
			coordinator := e.coordinator(e.client())
			group.Add(1)
			go func() {
				defer group.Done()
				if err := settleQuiet(e.ctx, coordinator, device.Name); err != nil {
					t.Error(err)
				}
			}()
		}
		group.Wait()
		e.settle(device.Name)
		custody := e.custody(device.Name)
		admitted := 0
		for _, operation := range operations {
			if status := e.status(operation); status.Phase == ops.PhaseAdmitted {
				admitted++
			} else if status.Phase != ops.PhaseFailed && status.Phase != ops.PhaseCancelled {
				t.Fatalf("%s is %s", operation.Name, status.Phase)
			}
		}
		if admitted != 1 || custody.Spec.Fence != before+1 || custody.Spec.ActiveOperation == nil {
			t.Fatalf("%d admitted, fence %d from %d", admitted, custody.Spec.Fence, before)
		}
	})

	t.Run("an admission races a holder change", func(t *testing.T) {
		e := e.sub(t)
		for range 5 {
			device := fresh()
			allocation := "alloc-hc-" + device.Name
			if err := e.coord.Claim(e.ctx, device.Name, allocation); err != nil {
				t.Fatal(err)
			}
			before := e.custody(device.Name).Spec.Fence
			operation := e.installation(device, allocation, "request-race")
			coordinator := e.coordinator(e.client())
			releaser := e.coordinator(e.client())
			var releaseErr, reconcileErr error
			var group sync.WaitGroup
			group.Add(2)
			go func() { defer group.Done(); reconcileErr = settleQuiet(e.ctx, coordinator, device.Name) }()
			go func() { defer group.Done(); releaseErr = releaser.BeginRelease(e.ctx, device.Name, allocation) }()
			group.Wait()
			if reconcileErr != nil || releaseErr != nil {
				t.Fatal(reconcileErr, releaseErr)
			}
			e.settle(device.Name)
			status := e.status(operation)
			custody := e.custody(device.Name)
			if custody.Spec.Holder.Type != ops.HolderRelease {
				t.Fatalf("holder = %s", custody.Spec.Holder.Type)
			}
			switch status.Phase {
			case ops.PhaseAdmitted:
				// Admitted under Allocation, then released while running.
				if custody.Spec.ActiveOperation == nil || custody.Spec.ActiveOperation.Name != operation.Name || custody.Spec.Fence != before+1 {
					t.Fatalf("admitted but custody %+v", custody.Spec)
				}
			case ops.PhaseFailed, ops.PhaseCancelled:
				if status.Reason != ReasonHolderMismatch || custody.Spec.ActiveOperation != nil || custody.Spec.Fence != before {
					t.Fatalf("refused %s but custody %+v", status.Reason, custody.Spec)
				}
			default:
				t.Fatalf("operation is %s", status.Phase)
			}
		}
	})

	t.Run("operations on two Devices do not block each other", func(t *testing.T) {
		e := e.sub(t)
		first, second := fresh(), fresh()
		a, b := e.staffOp(first, "ok"), e.staffOp(second, "ok")
		var group sync.WaitGroup
		for _, device := range []*inventory.Device{first, second} {
			coordinator := e.coordinator(e.client())
			group.Add(1)
			go func() {
				defer group.Done()
				if err := settleQuiet(e.ctx, coordinator, device.Name); err != nil {
					t.Error(err)
				}
			}()
		}
		group.Wait()
		e.requirePhase(a, ops.PhaseAdmitted, "")
		e.requirePhase(b, ops.PhaseAdmitted, "")
	})

	t.Run("an allocator retries after a lost response and claims no second server", func(t *testing.T) {
		e := e.sub(t)
		device, other := fresh(), fresh()
		lossy := &faultClient{Client: e.client(), failAt: 1, lost: true}
		allocator := e.coordinator(lossy)
		if err := allocator.Claim(e.ctx, device.Name, "alloc-lost"); err == nil {
			t.Fatal("the lost response was not reported")
		}
		if err := allocator.Claim(e.ctx, device.Name, "alloc-lost"); err != nil {
			t.Fatalf("retry: %v", err)
		}
		if holder := e.custody(device.Name).Spec.Holder; holder.Type != ops.HolderAllocation || holder.AllocationRef.Name != "alloc-lost" {
			t.Fatalf("holder = %+v", holder)
		}
		if holder := e.custody(other.Name).Spec.Holder.Type; holder != ops.HolderNone {
			t.Fatalf("a second server was claimed: %s", holder)
		}
	})
}

// errInjected is the fault faultClient returns.
var errInjected = errors.New("injected fault")

// faultClient fails the failAt-th write. A dropped write is not performed;
// a lost write is performed and its response lost.
type faultClient struct {
	client.Client
	mu     sync.Mutex
	writes int
	failAt int
	lost   bool
}

func (f *faultClient) fault(write func() error) error {
	f.mu.Lock()
	f.writes++
	hit := f.writes == f.failAt
	f.mu.Unlock()
	if !hit {
		return write()
	}
	if f.lost {
		if err := write(); err != nil {
			return err
		}
	}
	return errInjected
}

func (f *faultClient) Create(ctx context.Context, object client.Object, options ...client.CreateOption) error {
	return f.fault(func() error { return f.Client.Create(ctx, object, options...) })
}

func (f *faultClient) Update(ctx context.Context, object client.Object, options ...client.UpdateOption) error {
	return f.fault(func() error { return f.Client.Update(ctx, object, options...) })
}

func (f *faultClient) Delete(ctx context.Context, object client.Object, options ...client.DeleteOption) error {
	return f.fault(func() error { return f.Client.Delete(ctx, object, options...) })
}

func (f *faultClient) Status() client.SubResourceWriter {
	return &faultStatus{SubResourceWriter: f.Client.Status(), parent: f}
}

type faultStatus struct {
	client.SubResourceWriter
	parent *faultClient
}

func (s *faultStatus) Update(ctx context.Context, object client.Object, options ...client.SubResourceUpdateOption) error {
	return s.parent.fault(func() error { return s.SubResourceWriter.Update(ctx, object, options...) })
}

// TestWriteOrderCrashRecovery crashes between each pair of writes in "admit
// or finish an operation" (custody, then operation status) and in result
// acceptance, then finishes with a healthy coordinator. The outcome must be
// the same as a run without a crash.
func TestWriteOrderCrashRecovery(t *testing.T) {
	e := startEnv(t)
	n := 500
	for _, lost := range []bool{false, true} {
		// Writes: 1 admit custody, 2 Admitted status, 3 result custody,
		// 4 Succeeded status.
		for failAt := 1; failAt <= 4; failAt++ {
			t.Run(fmt.Sprintf("lost=%v crash at write %d", lost, failAt), func(t *testing.T) {
				e := e.sub(t)
				n++
				device := e.verified(fmt.Sprintf("server-example-%03d", n), uid(n), fmt.Sprintf("%064x", n))
				before := e.custody(device.Name).Spec.Fence
				operation := e.staffOp(device, "ok")
				faulty := e.coordinator(&faultClient{Client: e.client(), failAt: failAt, lost: lost})
				crashed := false
				for range 10 {
					if _, err := faulty.Reconcile(e.ctx, device.Name); err != nil {
						if !errors.Is(err, errInjected) {
							t.Fatal(err)
						}
						crashed = true
						break
					}
				}
				if !crashed {
					active := e.custody(device.Name).Spec.ActiveOperation
					err := faulty.AcceptResult(e.ctx, SiteResult{Site: testSite, Device: device.Name, Operation: active.OperationReference, Fence: active.Fence, Phase: ops.PhaseSucceeded, Evidence: []byte(`{"after":"On"}`)})
					if err != nil && !errors.Is(err, errInjected) {
						t.Fatal(err)
					}
					for range 10 {
						if _, err := faulty.Reconcile(e.ctx, device.Name); err != nil {
							if !errors.Is(err, errInjected) {
								t.Fatal(err)
							}
							break
						}
					}
				}
				// A healthy coordinator recovers from custody; the site retries
				// its result.
				e.settle(device.Name)
				if active := e.custody(device.Name).Spec.ActiveOperation; active != nil {
					if err := e.coord.AcceptResult(e.ctx, SiteResult{Site: testSite, Device: device.Name, Operation: active.OperationReference, Fence: active.Fence, Phase: ops.PhaseSucceeded, Evidence: []byte(`{"after":"On"}`)}); err != nil {
						t.Fatal(err)
					}
				} else if err := e.coord.AcceptResult(e.ctx, SiteResult{Site: testSite, Device: device.Name, Operation: reference(testStaffKind, operation), Fence: before + 1, Phase: ops.PhaseSucceeded, Evidence: []byte(`{"after":"On"}`)}); err != nil {
					t.Fatalf("a retried result after a lost response: %v", err)
				}
				e.settle(device.Name)
				status := e.requirePhase(operation, ops.PhaseSucceeded, "")
				custody := e.custody(device.Name)
				if custody.Spec.Fence != before+1 || custody.Spec.Holder.Type != ops.HolderNone || custody.Spec.ActiveOperation != nil || custody.Spec.RejectedResults != nil || status.Fence != before+1 || evidenceOf(t, status.Evidence.Raw).After != "On" {
					t.Fatalf("after recovery: custody %+v status %+v", custody.Spec, status)
				}
			})
		}
	}
}
