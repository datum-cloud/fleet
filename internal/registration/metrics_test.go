package registration

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
