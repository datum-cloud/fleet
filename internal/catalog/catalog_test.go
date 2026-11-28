package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/tenancy"
)

const (
	providerProject = "provider-example"
	providerNS      = "fleet-provider"
	imageName       = "example-linux-1-2"
)

var (
	digest   = "sha256:" + strings.Repeat("4f", 32)
	consumer = tenancy.Consumer{Ref: tenancy.Project("consumer-example"), UID: "00000000-0000-4000-8000-000000000501"}
)

type fixture struct {
	ctx      context.Context
	provider client.Client
	consumer client.Client
	catalog  *Catalog
	allowed  error
	// failRecord makes the next publication record create fail, as a
	// crash between Publish's two writes would.
	failRecord bool
	uids       int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := fleetv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	f := &fixture{ctx: context.Background()}
	// The fake API server assigns no UIDs; this one does, as a real one
	// would, so publication binds real incarnations.
	create := func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.CreateOption) error {
		if _, ok := object.(*fleetv1.MachineImagePublication); ok && f.failRecord {
			f.failRecord = false
			return errors.New("lost response")
		}
		if object.GetUID() == "" {
			f.uids++
			object.SetUID(types.UID(fmt.Sprintf("uid-%d", f.uids)))
		}
		return c.Create(ctx, object, opts...)
	}
	f.provider = fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&fleetv1.MachineImage{}).WithInterceptorFuncs(interceptor.Funcs{Create: create}).Build()
	f.consumer = fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&fleetv1.MachineImage{}).WithInterceptorFuncs(interceptor.Funcs{Create: create}).Build()
	f.catalog = &Catalog{Provider: f.provider, Publisher: tenancy.Project(providerProject), Namespace: providerNS, Authorize: func(context.Context, *fleetv1.Installation, *fleetv1.MachineImage, *fleetv1.MachineImage) error {
		return f.allowed
	}}
	f.define(imageName, digest, fleetv1.ImageAvailable)
	if err := f.catalog.Publish(f.ctx, f.provider, f.with(consumer), "default", imageName); err != nil {
		t.Fatal(err)
	}
	return f
}

// with is a consumer whose API is the fixture's consumer cluster.
func (f *fixture) with(consumer tenancy.Consumer) tenancy.Consumer {
	consumer.Client = f.consumer
	return consumer
}

func (f *fixture) define(name, digest, availability string) *fleetv1.MachineImage {
	image := &fleetv1.MachineImage{ObjectMeta: metav1.ObjectMeta{Namespace: providerNS, Name: name, UID: types.UID("uid-" + name + "-" + digest[len(digest)-4:])}, Spec: fleetv1.MachineImageSpec{
		Definition:   &fleetv1.ImageDefinition{Family: "example-linux", Version: "1.2", Architecture: "amd64", FirmwareModes: []string{fleetv1.FirmwareUEFI}, BootstrapFormats: []string{fleetv1.BootstrapNoCloud}, Digest: digest},
		Availability: availability,
	}}
	if err := f.provider.Create(f.ctx, image); err != nil {
		panic(err)
	}
	return image
}

// editCopy changes the published copy's source in place, keeping its UID.
// The API server's CEL rules forbid this; resolution must not rely on them.
func (f *fixture) editCopy(change func(*fleetv1.SourceReference)) {
	local := &fleetv1.MachineImage{}
	_ = f.consumer.Get(f.ctx, client.ObjectKey{Namespace: "default", Name: imageName}, local)
	change(local.Spec.Source)
	_ = f.consumer.Update(f.ctx, local)
}

func installation(image string) *fleetv1.Installation {
	return &fleetv1.Installation{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "example-os-1"}, Spec: fleetv1.InstallationSpec{ImageRef: fleetv1.LocalReference{Name: image}}}
}

func reason(t *testing.T, err error) string {
	t.Helper()
	var refusal *Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("err = %v, want a refusal", err)
	}
	return refusal.Reason
}

func TestResolvePinsThePublishedIncarnationAndDigest(t *testing.T) {
	f := newFixture(t)
	image, err := f.catalog.Resolve(f.ctx, f.with(consumer), installation(imageName))
	if err != nil {
		t.Fatal(err)
	}
	if image.Name != imageName || image.Definition.Digest != digest {
		t.Fatalf("image = %+v", image)
	}
	local := &fleetv1.MachineImage{}
	if err := f.consumer.Get(f.ctx, client.ObjectKey{Namespace: "default", Name: imageName}, local); err != nil {
		t.Fatal(err)
	}
	if local.Status.Resolved == nil || local.Status.Resolved.Digest != digest {
		t.Fatalf("published status = %+v", local.Status)
	}
}

// TestResolveRefusesEveryUnentitledImage covers each way a project can
// name an image it is not entitled to, and each refusal reason.
func TestResolveRefusesEveryUnentitledImage(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*fixture)
		image  string
		reason string
	}{
		{"no such image", nil, "missing", ReasonImageNotFound},
		{"a customer-written definition", func(f *fixture) {
			own := &fleetv1.MachineImage{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "own"}, Spec: fleetv1.MachineImageSpec{Definition: &fleetv1.ImageDefinition{Architecture: "amd64", Digest: digest}, Availability: fleetv1.ImageAvailable}}
			_ = f.consumer.Create(f.ctx, own)
		}, "own", ReasonImageNotEntitled},
		{"a copy naming another provider", func(f *fixture) {
			other := &fleetv1.MachineImage{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "foreign"}, Spec: fleetv1.MachineImageSpec{Source: &fleetv1.SourceReference{PublisherRef: tenancy.Project("provider-other"), Name: imageName}}}
			_ = f.consumer.Create(f.ctx, other)
		}, "foreign", ReasonImageNotEntitled},
		{"a copy with no publication record", func(f *fixture) {
			copy := &fleetv1.MachineImage{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "unpublished"}, Spec: fleetv1.MachineImageSpec{Source: &fleetv1.SourceReference{PublisherRef: tenancy.Project(providerProject), Name: imageName}}}
			_ = f.consumer.Create(f.ctx, copy)
		}, "unpublished", ReasonImageNotEntitled},
		{"a copy recreated after publication", func(f *fixture) {
			local := &fleetv1.MachineImage{}
			_ = f.consumer.Get(f.ctx, client.ObjectKey{Namespace: "default", Name: imageName}, local)
			_ = f.consumer.Delete(f.ctx, local)
			_ = f.consumer.Create(f.ctx, &fleetv1.MachineImage{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: imageName, UID: "recreated"}, Spec: local.Spec})
		}, imageName, ReasonImageNotEntitled},
		{"a provider image recreated under the same name", func(f *fixture) {
			_ = f.provider.Delete(f.ctx, &fleetv1.MachineImage{ObjectMeta: metav1.ObjectMeta{Namespace: providerNS, Name: imageName}})
			f.define(imageName, "sha256:"+strings.Repeat("9e", 32), fleetv1.ImageAvailable)
		}, imageName, ReasonImageNotEntitled},
		{"a provider image recreated with the same digest", func(f *fixture) {
			_ = f.provider.Delete(f.ctx, &fleetv1.MachineImage{ObjectMeta: metav1.ObjectMeta{Namespace: providerNS, Name: imageName}})
			image := f.define(imageName, digest, fleetv1.ImageAvailable)
			_ = f.provider.Delete(f.ctx, image)
			image.ResourceVersion, image.UID = "", "uid-recreated"
			_ = f.provider.Create(f.ctx, image)
		}, imageName, ReasonImageNotEntitled},
		{"a published copy edited to name the publisher by another kind", func(f *fixture) {
			f.editCopy(func(source *fleetv1.SourceReference) { source.PublisherRef = tenancy.Namespace(providerProject) })
		}, imageName, ReasonImageNotEntitled},
		{"a published copy edited to name another provider", func(f *fixture) {
			f.editCopy(func(source *fleetv1.SourceReference) { source.PublisherRef.Name = "provider-other" })
		}, imageName, ReasonImageNotEntitled},
		{"a published copy edited to name another image", func(f *fixture) {
			f.define("other-image", digest, fleetv1.ImageAvailable)
			f.editCopy(func(source *fleetv1.SourceReference) { source.Name = "other-image" })
		}, imageName, ReasonImageNotEntitled},
		{"a provider image whose digest changed in place", func(f *fixture) {
			source := &fleetv1.MachineImage{}
			_ = f.provider.Get(f.ctx, client.ObjectKey{Namespace: providerNS, Name: imageName}, source)
			source.Spec.Definition.Digest = "sha256:" + strings.Repeat("9e", 32)
			_ = f.provider.Update(f.ctx, source)
		}, imageName, ReasonImageNotEntitled},
		{"a deleted provider image", func(f *fixture) {
			_ = f.provider.Delete(f.ctx, &fleetv1.MachineImage{ObjectMeta: metav1.ObjectMeta{Namespace: providerNS, Name: imageName}})
		}, imageName, ReasonImageNotEntitled},
		{"a withdrawn image", func(f *fixture) {
			source := &fleetv1.MachineImage{}
			_ = f.provider.Get(f.ctx, client.ObjectKey{Namespace: providerNS, Name: imageName}, source)
			source.Spec.Availability = fleetv1.ImageWithdrawn
			_ = f.provider.Update(f.ctx, source)
		}, imageName, ReasonImageWithdrawn},
		{"a creator without use permission", func(f *fixture) { f.allowed = errors.New("denied") }, imageName, ReasonImageNotEntitled},
		{"an unavailable authorizer", func(f *fixture) { f.allowed = ErrAuthorizationUnavailable }, imageName, ReasonAuthorizationUnavailable},
		{"no authorizer", func(f *fixture) { f.catalog.Authorize = nil }, imageName, ReasonAuthorizationUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			if test.change != nil {
				test.change(f)
			}
			_, err := f.catalog.Resolve(f.ctx, f.with(consumer), installation(test.image))
			if got := reason(t, err); got != test.reason {
				t.Fatalf("reason = %s, want %s", got, test.reason)
			}
		})
	}
}

// TestAnotherProjectCannotUseThePublication proves the record binds the
// consumer project incarnation: a project of the same name with another
// UID, holding an identical copy, is not entitled.
func TestAnotherProjectCannotUseThePublication(t *testing.T) {
	f := newFixture(t)
	other := tenancy.Consumer{Ref: consumer.Ref, UID: "00000000-0000-4000-8000-000000000999"}
	if _, err := f.catalog.Resolve(f.ctx, f.with(other), installation(imageName)); reason(t, err) != ReasonImageNotEntitled {
		t.Fatalf("err = %v", err)
	}
}

func TestPublishIsIdempotentAndRefusesAnotherSource(t *testing.T) {
	f := newFixture(t)
	if err := f.catalog.Publish(f.ctx, f.provider, f.with(consumer), "default", imageName); err != nil {
		t.Fatalf("republish: %v", err)
	}
	f.define("other-image", digest, fleetv1.ImageAvailable)
	conflict := &fleetv1.MachineImage{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "other-image"}, Spec: fleetv1.MachineImageSpec{Source: &fleetv1.SourceReference{PublisherRef: tenancy.Project("provider-other"), Name: "other-image"}}}
	if err := f.consumer.Create(f.ctx, conflict); err != nil {
		t.Fatal(err)
	}
	if err := f.catalog.Publish(f.ctx, f.provider, f.with(consumer), "default", "other-image"); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want a conflict", err)
	}
}

// TestACrashBetweenPublishWritesFailsClosed loses the record write after
// the copy exists: the copy entitles nothing, and a retry completes it.
func TestACrashBetweenPublishWritesFailsClosed(t *testing.T) {
	f := newFixture(t)
	f.define("next-image", digest, fleetv1.ImageAvailable)
	f.failRecord = true
	if err := f.catalog.Publish(f.ctx, f.provider, f.with(consumer), "default", "next-image"); err == nil {
		t.Fatal("the lost record write was not reported")
	}
	if _, err := f.catalog.Resolve(f.ctx, f.with(consumer), installation("next-image")); reason(t, err) != ReasonImageNotEntitled {
		t.Fatalf("a copy without its record resolved: %v", err)
	}
	if err := f.catalog.Publish(f.ctx, f.provider, f.with(consumer), "default", "next-image"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if _, err := f.catalog.Resolve(f.ctx, f.with(consumer), installation("next-image")); err != nil {
		t.Fatalf("resolve after the retry: %v", err)
	}
}

// TestRepublishingARecreatedProviderImageWritesANewRecord: a provider
// image recreated under the same name is refused until it is published
// again, and publishing it writes a record of its own instead of taking
// over the first. The first record never entitles the new incarnation.
func TestRepublishingARecreatedProviderImageWritesANewRecord(t *testing.T) {
	f := newFixture(t)
	before := &fleetv1.MachineImagePublicationList{}
	if err := f.provider.List(f.ctx, before); err != nil || len(before.Items) != 1 {
		t.Fatalf("records = %d, %v", len(before.Items), err)
	}
	_ = f.provider.Delete(f.ctx, &fleetv1.MachineImage{ObjectMeta: metav1.ObjectMeta{Namespace: providerNS, Name: imageName}})
	image := f.define(imageName, digest, fleetv1.ImageAvailable)
	_ = f.provider.Delete(f.ctx, image)
	image.ResourceVersion, image.UID = "", "uid-recreated"
	if err := f.provider.Create(f.ctx, image); err != nil {
		t.Fatal(err)
	}
	if _, err := f.catalog.Resolve(f.ctx, f.with(consumer), installation(imageName)); reason(t, err) != ReasonImageNotEntitled {
		t.Fatalf("the recreated image resolved before it was published: %v", err)
	}
	if err := f.catalog.Publish(f.ctx, f.provider, f.with(consumer), "default", imageName); err != nil {
		t.Fatalf("republish: %v", err)
	}
	if _, err := f.catalog.Resolve(f.ctx, f.with(consumer), installation(imageName)); err != nil {
		t.Fatalf("resolve after republishing: %v", err)
	}
	after := &fleetv1.MachineImagePublicationList{}
	if err := f.provider.List(f.ctx, after); err != nil || len(after.Items) != 2 {
		t.Fatalf("records = %d, %v; republishing must not rewrite the first", len(after.Items), err)
	}
	for _, record := range after.Items {
		if record.Name == before.Items[0].Name && record.Spec != before.Items[0].Spec {
			t.Fatalf("the first record changed: %+v", record.Spec)
		}
	}
}

// TestNoPublicationSpecHoldsAUID: incarnations are pinned by the record's
// name, never by a UID in its spec.
func TestNoPublicationSpecHoldsAUID(t *testing.T) {
	f := newFixture(t)
	records := &fleetv1.MachineImagePublicationList{}
	if err := f.provider.List(f.ctx, records); err != nil || len(records.Items) != 1 {
		t.Fatalf("records = %d, %v", len(records.Items), err)
	}
	spec, _ := json.Marshal(records.Items[0].Spec)
	for _, uid := range []string{string(consumer.UID), "uid-", "\"uid\"", "UID", "\"project\""} {
		if strings.Contains(string(spec), uid) {
			t.Fatalf("spec %s holds %q", spec, uid)
		}
	}
}

func TestCompatibleChecksArchitectureFirmwareAndBootstrap(t *testing.T) {
	definition := fleetv1.ImageDefinition{Architecture: "amd64", FirmwareModes: []string{fleetv1.FirmwareUEFI}, BootstrapFormats: []string{fleetv1.BootstrapNoCloud}}
	noBootstrap := definition
	noBootstrap.BootstrapFormats = nil
	hardware := func(arch, mode string) fleetv1.Hardware {
		h := fleetv1.Hardware{CPU: fleetv1.HardwareCPU{Architecture: arch}}
		if mode != "" {
			h.Firmware = &fleetv1.HardwareFirmware{BootMode: mode}
		}
		return h
	}
	for _, test := range []struct {
		name       string
		definition fleetv1.ImageDefinition
		hardware   fleetv1.Hardware
		bootstrap  bool
		ok         bool
	}{
		{"matching", definition, hardware("amd64", fleetv1.FirmwareUEFI), true, true},
		{"another architecture", definition, hardware("arm64", fleetv1.FirmwareUEFI), false, false},
		{"another firmware mode", definition, hardware("amd64", fleetv1.FirmwareBIOS), false, false},
		{"an unknown firmware mode", definition, hardware("amd64", ""), false, false},
		{"bootstrap data for an image without a format", noBootstrap, hardware("amd64", fleetv1.FirmwareUEFI), true, false},
		{"no bootstrap data for an image without a format", noBootstrap, hardware("amd64", fleetv1.FirmwareUEFI), false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := Compatible(test.definition, test.hardware, test.bootstrap)
			if test.ok != (err == nil) {
				t.Fatalf("err = %v", err)
			}
			if err != nil && reason(t, err) != ReasonImageIncompatible {
				t.Fatalf("reason = %v", err)
			}
		})
	}
}

// unsupported are tenant references of kinds Fleet does not support.
var unsupported = map[string]fleetv1.TypedReference{
	"another kind":                 {APIGroup: "resourcemanager.miloapis.com", Kind: "Organization", Name: "consumer-example"},
	"a Project in the core group":  {Kind: tenancy.ProjectKind, Name: "consumer-example"},
	"a Namespace in another group": {APIGroup: "resourcemanager.miloapis.com", Kind: tenancy.NamespaceKind, Name: "consumer-example"},
	"no name":                      {APIGroup: "resourcemanager.miloapis.com", Kind: tenancy.ProjectKind},
}

// TestPublishRefusesAnUnsupportedTenant writes nothing for a consumer or a
// publisher of an unsupported kind.
func TestPublishRefusesAnUnsupportedTenant(t *testing.T) {
	for name, ref := range unsupported {
		for _, role := range []string{"consumer", "publisher"} {
			t.Run(name+" as "+role, func(t *testing.T) {
				f := newFixture(t)
				f.define("next-image", digest, fleetv1.ImageAvailable)
				who := tenancy.Consumer{Ref: ref, UID: consumer.UID}
				if role == "publisher" {
					who, f.catalog.Publisher = consumer, ref
				}
				if err := f.catalog.Publish(f.ctx, f.provider, f.with(who), "default", "next-image"); err == nil {
					t.Fatal("published to an unsupported tenant")
				}
				records := &fleetv1.MachineImagePublicationList{}
				copies := &fleetv1.MachineImageList{}
				_ = f.provider.List(f.ctx, records)
				_ = f.consumer.List(f.ctx, copies)
				if len(records.Items) != 1 || len(copies.Items) != 1 {
					t.Fatalf("records = %d, copies = %d; want only the fixture's", len(records.Items), len(copies.Items))
				}
			})
		}
	}
}

// TestResolveRefusesAnUnsupportedTenant refuses a consumer of an
// unsupported kind even when a matching copy and record exist, as they
// would if something other than Publish wrote them.
func TestResolveRefusesAnUnsupportedTenant(t *testing.T) {
	for name, ref := range unsupported {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			who := tenancy.Consumer{Ref: ref, UID: consumer.UID}
			local := &fleetv1.MachineImage{}
			source := &fleetv1.MachineImage{}
			_ = f.consumer.Get(f.ctx, client.ObjectKey{Namespace: "default", Name: imageName}, local)
			_ = f.provider.Get(f.ctx, client.ObjectKey{Namespace: providerNS, Name: imageName}, source)
			record := &fleetv1.MachineImagePublication{ObjectMeta: metav1.ObjectMeta{Namespace: providerNS, Name: PublicationName(who, local, source)}, Spec: publication(who, local, source)}
			if err := f.provider.Create(f.ctx, record); err != nil {
				t.Fatal(err)
			}
			if _, err := f.catalog.Resolve(f.ctx, f.with(who), installation(imageName)); reason(t, err) != ReasonImageNotEntitled {
				t.Fatalf("err = %v", err)
			}
		})
	}
	t.Run("an unsupported publisher", func(t *testing.T) {
		f := newFixture(t)
		f.catalog.Publisher = unsupported["another kind"]
		f.editCopy(func(source *fleetv1.SourceReference) { source.PublisherRef = f.catalog.Publisher })
		if _, err := f.catalog.Resolve(f.ctx, f.with(consumer), installation(imageName)); reason(t, err) != ReasonImageNotEntitled {
			t.Fatalf("err = %v", err)
		}
	})
}
