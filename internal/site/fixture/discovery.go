package fixture

import (
	"context"
	"errors"
	"strings"
	"time"

	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
)

// Discover is fixture discovery for a site's synthetic Devices. BMC
// discovery runs at the site and writes only the status of the Devices that
// site serves; a synthetic Device has no BMC, so this writes what discovery
// would observe: the enrolled key at the recorded endpoint, the enrolled
// system identity, and power off with no reset types offered. A Device
// whose recorded identity differs from the enrollment gets IdentityMatched
// False.
func Discover(ctx context.Context, api client.Client, enrollment Enrollment, now time.Time) error {
	return DiscoverPower(ctx, api, enrollment, now, nil)
}

// PowerObserver reports a synthetic Device's power state and the reset
// types its site's power worker offers, as BMC discovery would read them.
type PowerObserver func(device string) (state string, resetTypes []string, err error)

// DiscoverPower is Discover with the power each Device reports. Without an
// observer, every Device reports power off and offers no reset types, so no
// power operation is admitted.
func DiscoverPower(ctx context.Context, api client.Client, enrollment Enrollment, now time.Time, observe PowerObserver) error {
	var failed error
	for key, target := range enrollment {
		namespace, name, _ := strings.Cut(key, "/")
		device := &inventory.Device{}
		if err := api.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, device); err != nil {
			failed = errors.Join(failed, client.IgnoreNotFound(err))
			continue
		}
		if device.Spec.BMC == nil {
			continue
		}
		observed := metav1.NewTime(now.UTC().Truncate(time.Second))
		endpoint := device.Spec.BMC.Endpoint
		matched := endpoint == target.BMCEndpoint && strings.EqualFold(device.Spec.Identity.SystemUUID, target.SystemUUID)
		device.Status.Discovery = &inventory.DeviceDiscovery{
			LastAttemptAt:       &observed,
			LastAttemptEndpoint: endpoint,
			TLS:                 &inventory.ObservedTLS{ObservedAt: observed, Endpoint: endpoint, PublicKeySHA256: target.BMCPublicKeySHA256},
			Inventory: &inventory.ObservedInventory{ObservedAt: observed, Endpoint: endpoint, Systems: []inventory.ObservedSystem{{
				ID: "simulated", UUID: target.SystemUUID, PowerState: "Off",
			}}},
		}
		if observe != nil {
			// A server the observer cannot read keeps the default: power off,
			// with nothing offered.
			if state, offered, err := observe(key); err != nil {
				failed = errors.Join(failed, err)
			} else {
				system := &device.Status.Discovery.Inventory.Systems[0]
				system.PowerState, system.ResetTypes = state, offered
			}
		}
		status, reason := metav1.ConditionTrue, "FixtureEnrollment"
		if !matched {
			status, reason = metav1.ConditionFalse, "FixtureEnrollmentDiffers"
		}
		apiMeta.SetStatusCondition(&device.Status.Conditions, metav1.Condition{Type: inventory.ConditionIdentityMatched, Status: status, Reason: reason, Message: "Synthetic discovery from the simulator site's enrollment.", ObservedGeneration: device.Generation})
		if err := api.Status().Update(ctx, device); err != nil {
			failed = errors.Join(failed, err)
		}
	}
	return failed
}

// Run writes fixture discovery now and then every interval, as BMC
// discovery refreshes a real Device, until the context ends.
func Run(ctx context.Context, api client.Client, enrollment Enrollment, interval time.Duration, onError func(error)) {
	for {
		if err := Discover(ctx, api, enrollment, time.Now()); err != nil && ctx.Err() == nil && onError != nil {
			onError(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
