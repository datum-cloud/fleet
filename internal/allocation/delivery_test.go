package allocation

import (
	"context"
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"go.datum.net/fleet/internal/custody"
)

// undeclared is a site that has not reported its delivery declaration.
type undeclared struct{ custody.Sites }

func (undeclared) DeliveryMethods(context.Context, string, *inventory.Device) ([]custody.OfferedMethod, bool, error) {
	return nil, false, nil
}

// TestNoInstallationUntilTheSiteDeclaresItsMethods creates no installation
// while the serving site has not reported its delivery declaration, and
// the same request installs once it has.
func TestNoInstallationUntilTheSiteDeclaresItsMethods(t *testing.T) {
	e := startEnv(t)
	e.server(1)
	allocation, err := e.allocator.Allocate(e.ctx, e.request(1))
	if err != nil {
		t.Fatal(err)
	}
	declared := e.allocator.Sites
	e.allocator.Sites = undeclared{declared}
	request := Installation{Installation: e.installation(1, "debian-12", "")}
	if _, err := e.allocator.Install(e.ctx, allocation, request); !errors.Is(err, ErrDeliveryUnknown) {
		t.Fatalf("install err = %v", err)
	}
	name := custody.InstallationName(allocation.Name, uid(701))
	if err := e.admin.Get(e.ctx, client.ObjectKey{Namespace: namespace, Name: name}, &ops.DeviceInstallation{}); !apierrors.IsNotFound(err) {
		t.Fatalf("an installation was created without a declaration: %v", err)
	}
	e.allocator.Sites = declared
	operation, err := e.allocator.Install(e.ctx, allocation, request)
	if err != nil || operation.Spec.Delivery.Method != ops.DeliveryNetworkBoot {
		t.Fatalf("install = %v, %v", operation, err)
	}
}
