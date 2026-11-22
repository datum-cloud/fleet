package registration

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type retryMetricClient struct {
	client.Client
	remaining int
}

func (contender *retryMetricClient) Update(ctx context.Context, object client.Object, options ...client.UpdateOption) error {
	if contender.remaining > 0 {
		contender.remaining--
		return apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, object.GetName(), errors.New("competing writer"))
	}
	return contender.Client.Update(ctx, object, options...)
}

func exhaustedCount(t *testing.T) float64 {
	t.Helper()
	registry := prometheus.NewRegistry()
	registry.MustRegister(casRetriesExhausted)
	families, err := registry.Gather()
	if err != nil || len(families) != 1 || len(families[0].Metric) != 1 {
		t.Fatalf("counter collection: %v, %v", families, err)
	}
	if len(families[0].Metric[0].Label) != 0 {
		t.Fatal("counter must not expose actor, project, or operation labels")
	}
	return families[0].Metric[0].GetCounter().GetValue()
}

func TestCASExhaustionMetric(t *testing.T) {
	for _, conflicts := range []int{0, 5, 6} {
		handler, device := fixture(t)
		handler.Store.Client = &retryMetricClient{Client: handler.Store.Client, remaining: conflicts}
		before := exhaustedCount(t)
		response := handler.Handle(context.Background(), registrationRequest(t, device, "metric"))
		expected := float64(0)
		if conflicts == 6 {
			expected = 1
		}
		if response.Allowed != (conflicts < 6) || exhaustedCount(t)-before != expected {
			t.Fatalf("conflicts=%d allowed=%v counter delta=%v", conflicts, response.Allowed, exhaustedCount(t)-before)
		}
	}
	handler, device := fixture(t)
	request := registrationRequest(t, device, "denied")
	request.UserInfo.UID = ""
	before := exhaustedCount(t)
	if handler.Handle(context.Background(), request).Allowed || exhaustedCount(t) != before {
		t.Fatal("identity denial must not count as CAS exhaustion")
	}
	recorder := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), "# TYPE fleet_registration_cas_retries_exhausted_total counter") {
		t.Fatalf("metrics exposition failed: %s", recorder.Body.String())
	}
}

type segmentCreateFailure struct {
	client.Client
	fail bool
}

func (failing *segmentCreateFailure) Create(ctx context.Context, object client.Object, options ...client.CreateOption) error {
	if failing.fail && strings.HasPrefix(object.GetName(), SegmentPrefix) {
		return errors.New("segment create unavailable")
	}
	// The fake client does not assign UIDs, which segment read-back requires.
	if object.GetUID() == "" {
		object.SetUID(types.UID("uid-" + object.GetName()))
	}
	return failing.Client.Create(ctx, object, options...)
}

func ledgerGauges(t *testing.T, handler *Handler) map[string]float64 {
	t.Helper()
	registry := prometheus.NewRegistry()
	registry.MustRegister(ledgerCollector{handler: handler, ctx: context.Background()})
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]float64{}
	for _, family := range families {
		if family.GetType() != dto.MetricType_GAUGE || len(family.Metric) != 1 || len(family.Metric[0].Label) != 0 {
			t.Fatalf("%s must be one unlabeled gauge: %v", family.GetName(), family)
		}
		values[family.GetName()] = family.Metric[0].GetGauge().GetValue()
	}
	return values
}

func expectGauges(t *testing.T, step string, actual map[string]float64, expected map[string]float64) {
	t.Helper()
	if len(actual) != len(expected) {
		t.Fatalf("%s: gauges %v, expected %v", step, actual, expected)
	}
	for name, value := range expected {
		if got, exists := actual[name]; !exists || got != value {
			t.Fatalf("%s: %s=%v (present=%v), expected %v", step, name, got, exists, value)
		}
	}
}

func TestLedgerGaugesFollowStorageLifecycle(t *testing.T) {
	ctx := context.Background()
	handler, _ := fixture(t)
	private := controlFixture(t, handler)
	failing := &segmentCreateFailure{Client: handler.Store.Client}
	handler.Store.Client = failing
	secret, _, err := handler.Store.root(ctx)
	if err != nil {
		t.Fatal(err)
	}
	legacy := float64(len(secret.Data["ledger.json"]))
	state := func(tail, sealed, pending, activated float64) map[string]float64 {
		return map[string]float64{
			"fleet_registration_ledger_readable":            1,
			"fleet_registration_ledger_tail_bytes":          tail,
			"fleet_registration_ledger_tail_headroom_bytes": TailBudget - tail,
			"fleet_registration_ledger_sealed_segments":     sealed,
			"fleet_registration_ledger_remaining_segments":  MaxSegments - sealed,
			"fleet_registration_ledger_epoch":               sealed,
			"fleet_registration_ledger_expansion_pending":   pending,
			"fleet_registration_activation_verified":        activated,
		}
	}
	expectGauges(t, "bootstrap v1 root", ledgerGauges(t, handler), state(legacy, 0, 0, 0))
	if err := handler.ApplyControl(ctx, controlFor(t, handler, private, "activate")); err != nil {
		t.Fatal(err)
	}
	expectGauges(t, "activated", ledgerGauges(t, handler), state(legacy, 0, 0, 1))

	failing.fail = true
	if handler.ApplyControl(ctx, controlFor(t, handler, private, "expand")) == nil {
		t.Fatal("expansion completed without its segment")
	}
	expectGauges(t, "pending expansion", ledgerGauges(t, handler), state(legacy, 0, 1, 0))
	failing.fail = false
	if err := handler.Store.ResumeExpansion(ctx); err != nil {
		t.Fatal(err)
	}
	secret, _, err = handler.Store.root(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expectGauges(t, "expanded", ledgerGauges(t, handler), state(float64(len(secret.Data["tail.json"])), 1, 0, 1))

	handler.VerifyFence = func(context.Context) error { return errors.New("installed fence changed") }
	expectGauges(t, "fence lost", ledgerGauges(t, handler), state(float64(len(secret.Data["tail.json"])), 1, 0, 0))

	if err := failing.Client.Delete(ctx, secret); err != nil {
		t.Fatal(err)
	}
	expectGauges(t, "ledger unreadable", ledgerGauges(t, handler), map[string]float64{"fleet_registration_ledger_readable": 0})
}

func TestTransportExposesLedgerMetrics(t *testing.T) {
	handler, _ := fixture(t)
	controlFixture(t, handler)
	server := httptest.NewServer(handler.Transport())
	defer server.Close()
	response, err := server.Client().Get(server.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("metrics route: %d %v", response.StatusCode, err)
	}
	for _, line := range []string{
		"# TYPE fleet_registration_cas_retries_exhausted_total counter",
		"fleet_registration_ledger_readable 1",
		"fleet_registration_ledger_tail_headroom_bytes ",
		"fleet_registration_ledger_remaining_segments 16",
		"fleet_registration_ledger_sealed_segments 0",
		"fleet_registration_ledger_epoch 0",
		"fleet_registration_ledger_expansion_pending 0",
		"fleet_registration_activation_verified 0",
	} {
		if !strings.Contains(string(body), line) {
			t.Fatalf("missing %q in:\n%s", line, body)
		}
	}
	for _, private := range []string{"milo", "provider", "ledger-1"} {
		if strings.Contains(string(body), private) {
			t.Fatalf("metrics exposed authority, scope or root identity %q", private)
		}
	}
}

type blockingGetClient struct {
	client.Client
	entered chan struct{}
	release chan struct{}
}

func (blocking *blockingGetClient) Get(ctx context.Context, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
	select {
	case blocking.entered <- struct{}{}:
		<-blocking.release
	default:
	}
	return blocking.Client.Get(ctx, key, object, options...)
}

func TestMetricsRefuseConcurrentScrape(t *testing.T) {
	handler, _ := fixture(t)
	controlFixture(t, handler)
	blocking := &blockingGetClient{Client: handler.Store.Client, entered: make(chan struct{}), release: make(chan struct{})}
	handler.Store.Client = blocking
	metrics := handler.Transport()
	first := make(chan int, 1)
	go func() {
		recorder := httptest.NewRecorder()
		metrics.ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
		first <- recorder.Code
	}()
	<-blocking.entered
	second := httptest.NewRecorder()
	metrics.ServeHTTP(second, httptest.NewRequest("GET", "/metrics", nil))
	if second.Code != 503 {
		t.Fatalf("concurrent scrape: got %d, want 503", second.Code)
	}
	close(blocking.release)
	if code := <-first; code != 200 {
		t.Fatalf("first scrape: got %d, want 200", code)
	}
	third := httptest.NewRecorder()
	metrics.ServeHTTP(third, httptest.NewRequest("GET", "/metrics", nil))
	if third.Code != 200 {
		t.Fatalf("scrape after release: got %d, want 200", third.Code)
	}
}
