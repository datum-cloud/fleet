package registration

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

type contendedLedgerClient struct {
	client.Client
	remaining int
	attempts  int
	conflicts int
}

func (contender *contendedLedgerClient) Update(ctx context.Context, object client.Object, options ...client.UpdateOption) error {
	contender.attempts++
	if contender.remaining > 0 {
		contender.remaining--
		completed := make(chan error, 1)
		go func() {
			current := &corev1.Secret{}
			if err := contender.Client.Get(ctx, client.ObjectKeyFromObject(object), current); err != nil {
				completed <- err
				return
			}
			current.Annotations = map[string]string{"competing-writer": fmt.Sprint(contender.attempts)}
			completed <- contender.Client.Update(ctx, current)
		}()
		if err := <-completed; err != nil {
			return err
		}
	}
	err := contender.Client.Update(ctx, object, options...)
	if apierrors.IsConflict(err) {
		contender.conflicts++
	}
	return err
}

func TestRealLedgerCASRetryBudget(t *testing.T) {
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
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	api, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, conflicts := range []int{5, 6} {
		t.Run(fmt.Sprintf("conflicts-%d", conflicts), func(t *testing.T) {
			handler, device := fixture(t)
			secret, _, err := handler.Store.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			namespace := fmt.Sprintf("cas-%d", conflicts)
			if err := api.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}); err != nil {
				t.Fatal(err)
			}
			secret.Namespace, secret.UID, secret.ResourceVersion = namespace, "", ""
			if err := api.Create(ctx, secret); err != nil {
				t.Fatal(err)
			}
			contender := &contendedLedgerClient{Client: api, remaining: conflicts}
			handler.Store.Client, handler.Store.Namespace = contender, namespace
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			defer slog.SetDefault(previous)
			before := exhaustedCount(t)
			response := handler.Handle(ctx, registrationRequest(t, device, "contended"))
			if response.Allowed != (conflicts == 5) || contender.attempts != 6 || contender.conflicts != conflicts {
				t.Fatalf("real CAS retry budget: allowed=%v attempts=%d conflicts=%d result=%v", response.Allowed, contender.attempts, contender.conflicts, response.Result)
			}
			_, ledger, err := handler.Store.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if conflicts == 5 && (len(ledger.Entries) != 1 || ledger.Entries[0].Outcome != "Unknown") {
				t.Fatal("successful CAS did not retain unknown reservation")
			}
			if conflicts == 6 && (len(ledger.Entries) != 0 || !strings.Contains(logs.String(), "CAS retries exhausted") || !strings.Contains(logs.String(), `"attempts":6`)) {
				t.Fatalf("exhaustion was not observable or wrote reservation: %s", logs.String())
			}
			expected := float64(0)
			if conflicts == 6 {
				expected = 1
			}
			if exhaustedCount(t)-before != expected {
				t.Fatal("real Secret conflicts did not report exactly one counter increment on exhaustion")
			}
		})
	}
}
