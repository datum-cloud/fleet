package custody

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/bootaddress"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const mediaService = "https://192.0.2.10:8443"

var virtualMedia = OfferedMethod{Method: ops.DeliveryVirtualMedia, Addresses: ops.AddressesDataNetwork, MediaService: mediaService}.Input()

func TestSelectMethod(t *testing.T) {
	both := []OfferedMethod{{Method: ops.DeliveryNetworkBoot}, {Method: ops.DeliveryVirtualMedia, Addresses: ops.AddressesDataNetwork, MediaService: mediaService}}
	free := &ops.DeviceCustody{}
	held := func(method ops.DeliveryMethod) *ops.DeviceCustody {
		return &ops.DeviceCustody{Spec: ops.DeviceCustodySpec{RecoveryHold: &ops.RecoveryHold{OperationReference: ops.OperationReference{Kind: "DeviceInstallation"},
			Snapshot: &ops.DeviceSnapshot{Extra: map[string]string{ExtraDeliveryMethod: string(method)}}}}}
	}
	for _, tc := range []struct {
		name      string
		offered   []OfferedMethod
		qualified []ops.DeliveryMethod
		custody   *ops.DeviceCustody
		want      ops.DeliveryInput
		ok        bool
	}{
		{"the site's first qualified method", both, []ops.DeliveryMethod{ops.DeliveryNetworkBoot, ops.DeliveryVirtualMedia}, free, ops.DeliveryInput{Method: ops.DeliveryNetworkBoot}, true},
		{"the first method the server qualified for", both, []ops.DeliveryMethod{ops.DeliveryVirtualMedia}, free, virtualMedia, true},
		{"nothing the server qualified for", NetworkBootOnly, []ops.DeliveryMethod{ops.DeliveryVirtualMedia}, free, ops.DeliveryInput{}, false},
		{"a settling install keeps the held method", both, []ops.DeliveryMethod{ops.DeliveryNetworkBoot}, held(ops.DeliveryVirtualMedia), virtualMedia, true},
		{"and waits while the site does not offer it", NetworkBootOnly, []ops.DeliveryMethod{ops.DeliveryNetworkBoot}, held(ops.DeliveryVirtualMedia), ops.DeliveryInput{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := SelectMethod(tc.offered, tc.qualified, tc.custody)
			if !equalInput(got, tc.want) || ok != tc.ok {
				t.Fatalf("SelectMethod = %+v, %v; want %+v, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestQualifiedMethods(t *testing.T) {
	device := &inventory.Device{}
	if got := QualifiedMethods(device); len(got) != 1 || got[0] != ops.DeliveryNetworkBoot {
		t.Fatalf("no qualification record = %v", got)
	}
	device.Annotations = map[string]string{QualifiedMethodsAnnotation: "VirtualMedia, Unknown,VirtualMedia"}
	if got := QualifiedMethods(device); len(got) != 1 || got[0] != ops.DeliveryVirtualMedia {
		t.Fatalf("qualified = %v", got)
	}
}

// TestDeliveryMethodAdmission covers the delivery checks admission runs
// under the custody update. Each refusal sends nothing and leaves custody's
// fence where it was.
func TestDeliveryMethodAdmission(t *testing.T) {
	e := startEnv(t)
	n := 900
	held := func(e *env, qualified string) *inventory.Device {
		e.t.Helper()
		n++
		device := e.device(fmt.Sprintf("server-example-%03d", n), uid(n), fmt.Sprintf("%064x", n))
		if qualified != "" {
			device.Annotations = map[string]string{QualifiedMethodsAnnotation: qualified}
			if err := e.admin.Update(e.ctx, device); err != nil {
				e.t.Fatal(err)
			}
			e.discover(device, fmt.Sprintf("%064x", n), e.clock.Now())
		}
		e.allocation(device, "alloc-d-"+device.Name, "Active")
		e.sites.report(device.Name, 0)
		e.settle(device.Name)
		return device
	}
	install := func(e *env, device *inventory.Device, request string, delivery ops.DeliveryInput, network *ops.BootNetworkInput) *ops.DeviceInstallation {
		e.t.Helper()
		allocation := "alloc-d-" + device.Name
		if delivery.Method == ops.DeliveryVirtualMedia && delivery.ReportAddress == nil {
			// The Machine's address, bound to the operation's own claim.
			name := InstallationName(allocation, request)
			claim := e.ipClaim(bootaddress.ClaimName(name), name, "DeviceInstallation", "Bound", "198.51.100.40")
			delivery.ReportAddress = &ops.BootNetworkInput{AddressClaim: bootaddress.Reference(claim.GetName()), Address: "198.51.100.40"}
		}
		operation := &ops.DeviceInstallation{ObjectMeta: metav1.ObjectMeta{Name: InstallationName(allocation, request), Namespace: "default"}, Spec: ops.DeviceInstallationSpec{
			OperationEnvelope: envelope(device, providerUser, allocated(allocation)), RequestUID: request,
			Image: ops.ImageInput{Name: "debian-12", Digest: imageDigest}, BootInterface: "primary", TargetDisks: []string{"disk-0"}, BootNetwork: network, Delivery: delivery,
		}}
		e.create(operation)
		return operation
	}

	t.Run("a qualified virtual media install is admitted and records its method", func(t *testing.T) {
		e := e.sub(t)
		device := held(e, "VirtualMedia")
		operation := install(e, device, "request-1", virtualMedia, nil)
		e.settle(device.Name)
		active := e.custody(device.Name).Spec.ActiveOperation
		if active == nil || active.UID != string(operation.UID) || active.Snapshot.Extra[ExtraDeliveryMethod] != "VirtualMedia" {
			t.Fatalf("the snapshot records %q", active.Snapshot.Extra[ExtraDeliveryMethod])
		}
	})

	t.Run("a method no serving site offers is refused", func(t *testing.T) {
		e := e.sub(t)
		e.sites.mu.Lock()
		e.sites.methods = NetworkBootOnly
		e.sites.mu.Unlock()
		defer func() {
			e.sites.mu.Lock()
			e.sites.methods = nil
			e.sites.mu.Unlock()
		}()
		device := held(e, "NetworkBoot,VirtualMedia")
		fence := e.custody(device.Name).Spec.Fence
		// As the lab's refused installation does, it names a claim nothing
		// made: the method is refused before the claim is read.
		unclaimed := virtualMedia
		unclaimed.ReportAddress = &ops.BootNetworkInput{AddressClaim: bootaddress.Reference(bootaddress.ClaimName("never-claimed")), Address: "198.51.100.99"}
		operation := install(e, device, "request-1", unclaimed, nil)
		e.settle(device.Name)
		e.requirePhase(operation, ops.PhaseFailed, ReasonDeliveryNotOffered)
		e.requireUntouched(device.Name, fence)
	})

	t.Run("a method the server is not qualified for is refused", func(t *testing.T) {
		e := e.sub(t)
		device := held(e, "")
		fence := e.custody(device.Name).Spec.Fence
		operation := install(e, device, "request-1", virtualMedia, nil)
		e.settle(device.Name)
		e.requirePhase(operation, ops.PhaseFailed, ReasonDeliveryUnqualified)
		e.requireUntouched(device.Name, fence)
	})

	t.Run("a method that does not match the other inputs is refused", func(t *testing.T) {
		e := e.sub(t)
		device := held(e, "VirtualMedia")
		fence := e.custody(device.Name).Spec.Fence
		notURL := virtualMedia
		notURL.MediaService = "http://media.example"
		offers, _, _ := e.sites.DeliveryMethods(e.ctx, "", device)
		if refusal := checkDelivery(&Admission{Device: device, Custody: e.custody(device.Name), Offered: offers, OfferedKnown: true}, &ops.DeviceInstallationSpec{Delivery: notURL}); refusal == nil || refusal.Reason != ReasonDeliveryInputs {
			t.Fatalf("a media service that is not https://IPv4:port: %+v", refusal)
		}
		network := &ops.BootNetworkInput{AddressClaim: fleetv1.TypedReference{APIGroup: "ipam.miloapis.com", Kind: "IPAddressClaim", Name: "claim-example"}, Address: "198.51.100.30"}
		operation := install(e, device, "request-1", virtualMedia, network)
		e.settle(device.Name)
		e.requirePhase(operation, ops.PhaseFailed, ReasonDeliveryInputs)
		e.requireUntouched(device.Name, fence)
	})

	t.Run("a virtual media install reports only from its own claimed address", func(t *testing.T) {
		e := e.sub(t)
		device := held(e, "VirtualMedia")
		fence := e.custody(device.Name).Spec.Fence
		if refusal := checkDelivery(&Admission{Device: device, Custody: e.custody(device.Name), Offered: e.sites.DeliveryMethods("")}, &ops.DeviceInstallationSpec{Delivery: virtualMedia}); refusal == nil || refusal.Reason != ReasonReportAddressRequired {
			t.Fatalf("virtual media with no report address: %+v", refusal)
		}
		// Another operation's claim does not hold this one's address.
		other := e.ipClaim(bootaddress.ClaimName("alloc-d-other-install"), "alloc-d-other-install", "DeviceInstallation", "Bound", "198.51.100.41")
		borrowed := virtualMedia
		borrowed.ReportAddress = &ops.BootNetworkInput{AddressClaim: bootaddress.Reference(other.GetName()), Address: "198.51.100.41"}
		operation := install(e, device, "request-1", borrowed, nil)
		e.settle(device.Name)
		e.requirePhase(operation, ops.PhaseFailed, ReasonBootAddressMismatch)
		e.requireUntouched(device.Name, fence)
	})

	t.Run("a held virtual media install is settled only by virtual media", func(t *testing.T) {
		e := e.sub(t)
		device := held(e, "VirtualMedia")
		install(e, device, "request-1", virtualMedia, nil)
		e.settle(device.Name)
		if err := e.result(device.Name, ops.PhaseOutcomeUnknown, ""); err != nil {
			t.Fatal(err)
		}
		e.settle(device.Name)
		custody := e.custody(device.Name)
		if method, ok := HeldMethod(custody, "DeviceInstallation"); !ok || method != ops.DeliveryVirtualMedia {
			t.Fatalf("the hold records %q, %v", method, ok)
		}
		fence := custody.Spec.Fence
		// The server lost its virtual media qualification and the site's
		// preference changed; neither matters to the settling install.
		device.Annotations = map[string]string{QualifiedMethodsAnnotation: "NetworkBoot"}
		if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(device), device); err != nil {
			t.Fatal(err)
		}
		device.Annotations = map[string]string{QualifiedMethodsAnnotation: "NetworkBoot"}
		if err := e.admin.Update(e.ctx, device); err != nil {
			t.Fatal(err)
		}
		e.discover(device, fmt.Sprintf("%064x", n), e.clock.Now())
		other := install(e, device, "request-2", ops.DeliveryInput{Method: ops.DeliveryNetworkBoot}, nil)
		e.settle(device.Name)
		e.requirePhase(other, ops.PhaseFailed, ReasonDeliveryMismatch)
		e.requireUntouched(device.Name, fence)
		// The settling install still takes its inputs from the site's
		// offer: another media service is refused.
		elsewhere := virtualMedia
		elsewhere.MediaService = "https://198.51.100.9:8443"
		redirected := install(e, device, "request-4", elsewhere, nil)
		e.settle(device.Name)
		e.requirePhase(redirected, ops.PhaseFailed, ReasonDeliveryInputs)
		e.requireUntouched(device.Name, fence)
		settling := install(e, device, "request-3", virtualMedia, nil)
		e.settle(device.Name)
		if active := e.custody(device.Name).Spec.ActiveOperation; active == nil || active.UID != string(settling.UID) {
			t.Fatalf("the virtual media settling install was not admitted: %+v, %+v", active, e.status(settling))
		}
	})

	t.Run("a hold whose method no site offers says what it waits for", func(t *testing.T) {
		e := e.sub(t)
		device := held(e, "VirtualMedia")
		install(e, device, "request-1", virtualMedia, nil)
		e.settle(device.Name)
		if err := e.result(device.Name, ops.PhaseOutcomeUnknown, ""); err != nil {
			t.Fatal(err)
		}
		e.settle(device.Name)
		if waiting := e.custody(device.Name).Spec.RecoveryHold.Waiting; waiting != nil {
			t.Fatalf("a hold whose method is offered waits: %+v", waiting)
		}
		e.sites.mu.Lock()
		e.sites.methods = NetworkBootOnly
		e.sites.mu.Unlock()
		defer func() {
			e.sites.mu.Lock()
			e.sites.methods = nil
			e.sites.mu.Unlock()
		}()
		e.settle(device.Name)
		waiting := e.custody(device.Name).Spec.RecoveryHold.Waiting
		if waiting == nil || waiting.Reason != ReasonHoldMethodNotOffered || !strings.Contains(waiting.Message, "VirtualMedia") {
			t.Fatalf("waiting = %+v", waiting)
		}
		e.sites.mu.Lock()
		e.sites.methods = nil
		e.sites.mu.Unlock()
		e.settle(device.Name)
		if waiting := e.custody(device.Name).Spec.RecoveryHold.Waiting; waiting != nil {
			t.Fatalf("the hold still waits once the site offers the method again: %+v", waiting)
		}
	})

	t.Run("an install waits while a serving site has not reported its methods", func(t *testing.T) {
		e := e.sub(t)
		device := held(e, "VirtualMedia")
		fence := e.custody(device.Name).Spec.Fence
		e.sites.mu.Lock()
		e.sites.unknown = true
		e.sites.mu.Unlock()
		operation := install(e, device, "request-1", virtualMedia, nil)
		e.settle(device.Name)
		e.requirePhase(operation, ops.PhasePending, ReasonDeliveryNotOffered)
		e.requireUntouched(device.Name, fence)
		e.sites.mu.Lock()
		e.sites.unknown = false
		e.sites.mu.Unlock()
		e.settle(device.Name)
		if active := e.custody(device.Name).Spec.ActiveOperation; active == nil || active.UID != string(operation.UID) {
			t.Fatalf("the install was not admitted once the site reported its methods: %+v", active)
		}
	})

	t.Run("a wipe cannot settle a held virtual media install", func(t *testing.T) {
		e := e.sub(t)
		device := held(e, "VirtualMedia")
		allocation := "alloc-d-" + device.Name
		install(e, device, "request-1", virtualMedia, nil)
		e.settle(device.Name)
		if err := e.result(device.Name, ops.PhaseDispatched, ""); err != nil {
			t.Fatal(err)
		}
		if err := e.coord.BeginRelease(e.ctx, device.Name, allocation); err != nil {
			t.Fatal(err)
		}
		e.settle(device.Name)
		e.clock.Advance(DeliveryDeadline(virtualMedia) + DefaultSkewMargin)
		e.settle(device.Name)
		custody := e.custody(device.Name)
		if custody.Spec.Holder.Type != ops.HolderRelease || custody.Spec.RecoveryHold == nil {
			t.Fatalf("after the install deadline: %+v", custody.Spec)
		}
		e.discover(device, fmt.Sprintf("%064x", n), e.clock.Now())
		wipe := e.wipe(device, allocation, 1)
		e.settle(device.Name)
		e.requirePhase(wipe, ops.PhaseFailed, ReasonDeliveryMismatch)
		e.requireUntouched(device.Name, custody.Spec.Fence)
	})

	t.Run("an unpinned installer download is refused", func(t *testing.T) {
		e := e.sub(t)
		device := held(e, "VirtualMedia")
		fence := e.custody(device.Name).Spec.Fence
		allocation := "alloc-d-" + device.Name
		delivery := virtualMedia
		delivery.ReportAddress = &ops.BootNetworkInput{AddressClaim: bootaddress.Reference(bootaddress.ClaimName(InstallationName(allocation, "request-1"))), Address: "198.51.100.40"}
		operation := &ops.DeviceInstallation{ObjectMeta: metav1.ObjectMeta{Name: InstallationName(allocation, "request-1"), Namespace: "default"}, Spec: ops.DeviceInstallationSpec{
			OperationEnvelope: envelope(device, providerUser, allocated(allocation)), RequestUID: "request-1",
			Image:         ops.ImageInput{Name: "talos", Digest: imageDigest, Downloads: []ops.DownloadInput{{Name: "installer", Reference: "registry.example/installer:v1.0.0"}}},
			BootInterface: "primary", TargetDisks: []string{"disk-0"}, Delivery: delivery,
		}}
		e.create(operation)
		e.settle(device.Name)
		e.requirePhase(operation, ops.PhaseFailed, ReasonDownloadUnpinned)
		e.requireUntouched(device.Name, fence)
	})

	t.Run("a BMC that does not verify the media service is refused", func(t *testing.T) {
		e := e.sub(t)
		device := held(e, "VirtualMedia")
		device.Annotations[MediaTLSAnnotation] = "Unverified"
		if err := e.admin.Update(e.ctx, device); err != nil {
			t.Fatal(err)
		}
		e.discover(device, fmt.Sprintf("%064x", n), e.clock.Now())
		fence := e.custody(device.Name).Spec.Fence
		operation := install(e, device, "request-1", virtualMedia, nil)
		e.settle(device.Name)
		e.requirePhase(operation, ops.PhaseFailed, ReasonMediaTLSUnverified)
		e.requireUntouched(device.Name, fence)
	})

	t.Run("a virtual media install's deadline is its phases", func(t *testing.T) {
		e := e.sub(t)
		device := held(e, "VirtualMedia")
		operation := install(e, device, "request-1", virtualMedia, nil)
		e.settle(device.Name)
		active := e.custody(device.Name).Spec.ActiveOperation
		if active == nil || active.UID != string(operation.UID) || active.Deadline.Sub(active.AdmittedAt.Time) != ops.DefaultFetchWindow+ops.DefaultReportWindow {
			t.Fatalf("active = %+v", active)
		}
	})
}

// twoSites serves one Device from two sites with their own declarations.
type twoSites map[string][]OfferedMethod

func (t twoSites) Serving(*inventory.Device) []string { return []string{"site-a", "site-b"} }
func (t twoSites) Fence(context.Context, string, *inventory.Device) (SiteFence, bool, error) {
	return SiteFence{}, false, nil
}
func (t twoSites) DeliveryMethods(_ context.Context, site string, _ *inventory.Device) ([]OfferedMethod, bool, error) {
	methods, known := t[site]
	return methods, known, nil
}

func TestOfferedIsWhatEveryServingSiteOffers(t *testing.T) {
	window := func(d time.Duration) *metav1.Duration { return &metav1.Duration{Duration: d} }
	media := func() OfferedMethod {
		// Each site's declaration holds its own window values.
		return OfferedMethod{Method: ops.DeliveryVirtualMedia, Addresses: ops.AddressesDataNetwork, MediaService: mediaService, FetchWindow: window(time.Minute), ReportWindow: window(time.Hour)}
	}
	got, known, _ := Offered(context.Background(), twoSites{"site-a": {{Method: ops.DeliveryNetworkBoot}, media()}, "site-b": {media()}}, &inventory.Device{})
	if !known || len(got) != 1 || got[0].Method != ops.DeliveryVirtualMedia {
		t.Fatalf("offered = %+v, want virtual media alone", got)
	}
	other := media()
	other.ReportWindow = window(time.Minute)
	if got, _, _ := Offered(context.Background(), twoSites{"site-a": {media()}, "site-b": {other}}, &inventory.Device{}); len(got) != 0 {
		t.Fatalf("sites with different windows offered %+v", got)
	}
	if _, known, _ := Offered(context.Background(), twoSites{"site-a": {media()}}, &inventory.Device{}); known {
		t.Fatal("a site that has reported no declaration counted as known")
	}
}

func TestCheckDownloads(t *testing.T) {
	digest := "@sha256:" + strings.Repeat("a", 64)
	for name, downloads := range map[string][]ops.DownloadInput{
		"a tag":              {{Name: "installer", Reference: "registry.example/installer:v1"}},
		"a newline in name":  {{Name: "installer\ntoken=x", Reference: "registry.example/installer" + digest}},
		"a newline in ref":   {{Name: "installer", Reference: "registry.example/installer\n" + digest}},
		"too many downloads": slices.Repeat([]ops.DownloadInput{{Name: "installer", Reference: "registry.example/installer" + digest}}, maxDownloads+1),
	} {
		if checkDownloads(downloads) == nil {
			t.Errorf("%s was not refused", name)
		}
	}
	if refusal := checkDownloads([]ops.DownloadInput{{Name: "installer", Reference: "registry.example/installer" + digest}}); refusal != nil {
		t.Fatalf("a pinned download was refused: %+v", refusal)
	}
}

func TestParseOffered(t *testing.T) {
	got, err := ParseOffered([]byte(`[{"method":"NetworkBoot"},{"method":"VirtualMedia","addresses":"DataNetwork","mediaService":"https://192.0.2.10:8443","fetchWindow":"5m"}]`))
	if err != nil || len(got) != 2 || got[0].Method != ops.DeliveryNetworkBoot || got[1].FetchWindow.Duration != 5*time.Minute {
		t.Fatalf("declaration = %+v, %v", got, err)
	}
	if got, err := ParseOffered([]byte(`[]`)); err != nil || len(got) != 0 {
		t.Fatalf("an empty declaration offers none: %+v, %v", got, err)
	}
	for name, data := range map[string]string{
		"not a list":            `{"method":"NetworkBoot"}`,
		"unknown method":        `[{"method":"Floppy"}]`,
		"repeated method":       `[{"method":"NetworkBoot"},{"method":"NetworkBoot"}]`,
		"unknown field":         `[{"method":"NetworkBoot","priority":1}]`,
		"network boot settings": `[{"method":"NetworkBoot","mediaService":"https://192.0.2.10:8443"}]`,
		"no address mode":       `[{"method":"VirtualMedia","mediaService":"https://192.0.2.10:8443"}]`,
		"plain http service":    `[{"method":"VirtualMedia","addresses":"DataNetwork","mediaService":"http://192.0.2.10:8080"}]`,
		"service with a path":   `[{"method":"VirtualMedia","addresses":"DataNetwork","mediaService":"https://192.0.2.10:8443/media"}]`,
		"zero report window":    `[{"method":"VirtualMedia","addresses":"DataNetwork","mediaService":"https://192.0.2.10:8443","reportWindow":"0s"}]`,
		"trailing data":         `[{"method":"NetworkBoot"}] []`,
		"null":                  `null`,
		"service with a user":   `[{"method":"VirtualMedia","addresses":"DataNetwork","mediaService":"https://user:secret@192.0.2.10:8443"}]`,
		"service with a query":  `[{"method":"VirtualMedia","addresses":"DataNetwork","mediaService":"https://192.0.2.10:8443?x=1"}]`,
	} {
		if _, err := ParseOffered([]byte(data)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
