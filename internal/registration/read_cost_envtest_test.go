package registration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// syntheticDelta builds a ledger delta whose encoding is just under target
// bytes, shaped like real registrations: retained create input, actor and
// three hardware aliases per entry, plus a matching Device fence.
func syntheticDelta(t testing.TB, prefix string, target int) []byte {
	t.Helper()
	ledger := Ledger{Authority: "milo", Projects: []string{"provider"}}
	input, _ := json.Marshal(map[string]any{"metadata": map[string]any{"name": "capacity", "namespace": "default", "annotations": map[string]string{DeviceUIDAnnotation: "device", DeviceVersionAnnotation: "1", OperationAnnotation: "client-operation"}}, "spec": map[string]any{"deviceRef": map[string]string{"name": "device"}, "allocationPolicy": "Disabled", "reclaimPolicy": "Retain"}})
	var encoded []byte
	for index := 0; ; index++ {
		id := fmt.Sprintf("%s-%06d", prefix, index)
		aliases := []string{"provider:" + digest([]byte("p"+id)), "serial:" + digest([]byte("s"+id)), "uuid:" + digest([]byte("u"+id))}
		ledger.Entries = append(ledger.Entries, Entry{Operation: types.UID("operation-" + id), Actor: authenticationv1.UserInfo{Username: "operator", UID: "actor-1", Groups: []string{"system:authenticated"}}, Project: "provider", ProjectUID: "project-1", NamespaceUID: "namespace-1", Name: "capacity-" + id, CapacityUID: types.UID("capacity-" + id), DeviceName: "device-" + id, DeviceUID: types.UID("device-" + id), DeviceVersion: "1", Aliases: aliases, AssetID: aliases[1], Input: input, Outcome: "Recorded"})
		ledger.DeviceFences = append(ledger.DeviceFences, DeviceFence{Project: "provider", ProjectUID: "project-1", Namespace: "default", NamespaceUID: "namespace-1", Name: "device-" + id, UID: types.UID("device-" + id), Aliases: aliases, Version: "1"})
		if index%32 != 0 {
			continue
		}
		next, err := json.Marshal(ledger)
		if err != nil {
			t.Fatal(err)
		}
		if len(next) > target {
			if encoded == nil {
				t.Fatal("target too small")
			}
			return encoded
		}
		encoded = next
	}
}

// fullLedger writes a root with MaxSegments sealed segments of SegmentBudget
// bytes and a tail at TailBudget into namespace, using an admin client so the
// fixture reaches the bound without replaying sixteen signed expansions.
func fullLedger(t testing.TB, ctx context.Context, api client.Client, namespace string) {
	t.Helper()
	if err := api.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}); err != nil {
		t.Fatal(err)
	}
	sentinel := []byte(`{"authority":"v2-routing-required","projects":[]}`)
	root := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: LedgerName, Namespace: namespace}, Data: map[string][]byte{"ledger.json": sentinel}}
	if err := api.Create(ctx, root); err != nil {
		t.Fatal(err)
	}
	route := Routing{Version: 2, RootUID: root.UID}
	immutable := true
	for index := range MaxSegments {
		data := syntheticDelta(t, fmt.Sprint("segment-", index), SegmentBudget)
		segment := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: segmentName(root.UID, uint64(index)), Namespace: namespace}, Immutable: &immutable, Data: map[string][]byte{"ledger.json": data, "rootUID": []byte(root.UID)}}
		if err := api.Create(ctx, segment); err != nil {
			t.Fatal(err)
		}
		route.Segments = append(route.Segments, Segment{Name: segment.Name, UID: segment.UID, Digest: digest(data)})
	}
	route.Epoch = uint64(len(route.Segments))
	routing, _ := json.Marshal(route)
	root.Data["routing.json"] = routing
	root.Data["tail.json"] = syntheticDelta(t, "tail", TailBudget)
	if err := api.Update(ctx, root); err != nil {
		t.Fatal(err)
	}
}

// countingClient counts full segment reads so the test can prove warm reads
// stop transferring sealed segment payloads.
type countingClient struct {
	client.Client
	fullSegmentReads atomic.Int64
}

func (counting *countingClient) Get(ctx context.Context, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
	if _, full := object.(*corev1.Secret); full && key.Name != LedgerName {
		counting.fullSegmentReads.Add(1)
	}
	return counting.Client.Get(ctx, key, object, options...)
}

func startReadCostEnvironment(t testing.TB) (client.Client, context.Context) {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS required")
	}
	environment := &envtest.Environment{}
	config, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Error(err)
		}
	})
	scheme := k8sruntime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	api, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	return api, context.Background()
}

// TestFullLedgerReadCostIsBounded measures four concurrent admissions' ledger
// reads at the sixteen-segment bound. Before segment caching each read decoded
// every sealed segment and four concurrent reads allocated over 800MiB
// against a 512Mi pod limit.
func TestFullLedgerReadCostIsBounded(t *testing.T) {
	api, ctx := startReadCostEnvironment(t)
	fullLedger(t, ctx, api, "read-cost")
	counting := &countingClient{Client: api}
	store := Store{Client: counting, Namespace: "read-cost", Authority: "milo", Projects: []string{"provider"}, Cache: NewSegmentCache()}
	_, cold, err := store.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if loaded := counting.fullSegmentReads.Load(); loaded != MaxSegments {
		t.Fatalf("cold read loaded %d segments, want %d", loaded, MaxSegments)
	}
	const concurrent = 4
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	var workers sync.WaitGroup
	failures := make(chan error, concurrent)
	for range concurrent {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, warm, err := store.Read(ctx)
			if err == nil && (len(warm.Entries) != len(cold.Entries) || len(warm.DeviceFences) != len(cold.DeviceFences)) {
				err = fmt.Errorf("warm read returned %d entries, want %d", len(warm.Entries), len(cold.Entries))
			}
			failures <- err
		}()
	}
	workers.Wait()
	elapsed := time.Since(started)
	runtime.ReadMemStats(&after)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("%d concurrent warm reads at %d segments: %d entries, %.1f MiB allocated, %s elapsed", concurrent, MaxSegments, len(cold.Entries), float64(allocated)/(1<<20), elapsed)
	if loaded := counting.fullSegmentReads.Load(); loaded != MaxSegments {
		t.Fatalf("warm reads reloaded sealed segments: %d full reads", loaded)
	}
	// The pod limit is 512Mi; four concurrent admissions must stay far below it.
	if limit := uint64(64 << 20); allocated > limit {
		t.Fatalf("four concurrent warm reads allocated %d bytes, limit %d", allocated, limit)
	}
}

func BenchmarkFullLedgerRead(b *testing.B) {
	api, ctx := startReadCostEnvironment(b)
	fullLedger(b, ctx, api, "read-cost")
	store := Store{Client: api, Namespace: "read-cost", Authority: "milo", Projects: []string{"provider"}, Cache: NewSegmentCache()}
	if _, _, err := store.Read(ctx); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, _, err := store.Read(ctx); err != nil {
			b.Fatal(err)
		}
	}
}
