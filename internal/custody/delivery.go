package custody

import (
	"context"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
)

// OfferedMethod is one boot delivery method a site offers, with what the
// method needs from the site.
type OfferedMethod struct {
	Method ops.DeliveryMethod `json:"method"`
	// Addresses is how the installed OS gets its addresses under virtual
	// media. Network boot has none.
	Addresses ops.AddressMode `json:"addresses,omitempty"`
	// MediaService is the central media service virtual media uses at this
	// site, as https://IPv4:port.
	MediaService string `json:"mediaService,omitempty"`
	// FetchWindow and ReportWindow replace the default virtual media
	// phases at this site, such as shorter ones in a lab.
	FetchWindow  *metav1.Duration `json:"fetchWindow,omitempty"`
	ReportWindow *metav1.Duration `json:"reportWindow,omitempty"`
}

// Input is the delivery input an operation records for this offer, with
// the default phases where the site sets none.
func (o OfferedMethod) Input() ops.DeliveryInput {
	if o.Method != ops.DeliveryVirtualMedia {
		return ops.DeliveryInput{Method: o.Method}
	}
	fetch, report := &metav1.Duration{Duration: ops.DefaultFetchWindow}, &metav1.Duration{Duration: ops.DefaultReportWindow}
	if o.FetchWindow != nil {
		fetch = o.FetchWindow
	}
	if o.ReportWindow != nil {
		report = o.ReportWindow
	}
	return ops.DeliveryInput{Method: o.Method, Addresses: o.Addresses, MediaService: o.MediaService, FetchWindow: fetch, ReportWindow: report}
}

// DeliveryDeadline is how long an installation may run: the sum of its
// virtual media phases, or zero for the kind's own deadline.
func DeliveryDeadline(delivery ops.DeliveryInput) time.Duration {
	if delivery.Method != ops.DeliveryVirtualMedia || delivery.FetchWindow == nil || delivery.ReportWindow == nil {
		return 0
	}
	return delivery.FetchWindow.Duration + delivery.ReportWindow.Duration
}

// Reasons for an installation's delivery method. Each is raised at
// admission, before anything is sent.
const (
	// ReasonDeliveryNotOffered means no site serving the Device offers the
	// operation's method.
	ReasonDeliveryNotOffered = "DeliveryMethodNotOffered"
	// ReasonDeliveryUnqualified means the server is not qualified for the
	// operation's method.
	ReasonDeliveryUnqualified = "DeliveryMethodUnqualified"
	// ReasonDeliveryMismatch means the operation settles a hold whose
	// operation was prepared with another method.
	ReasonDeliveryMismatch = "DeliveryMethodMismatch"
	// ReasonDeliveryInputs means the method does not match the operation's
	// other inputs, such as a virtual media operation with a boot network.
	ReasonDeliveryInputs = "DeliveryInputsMismatch"
	// ReasonReportAddressRequired means a virtual media installation
	// records no IPAM address for its first-boot report, so the media
	// service could not tell its report from another server's.
	ReasonReportAddressRequired = "ReportAddressRequired"
	// ReasonDownloadUnpinned means the installer would download an
	// artifact that the operation does not pin by digest.
	ReasonDownloadUnpinned = "InstallDownloadUnpinned"
	// ReasonMediaWindowRefused means the central media service refused the
	// operation's window before anything reached the BMC.
	ReasonMediaWindowRefused = "MediaWindowRefused"
	// ReasonMediaTLSUnverified means the server's BMC fetches media without
	// verifying the media service's certificate. The fallback that serves
	// such a BMC only public media is not implemented.
	ReasonMediaTLSUnverified = "VirtualMediaTLSUnverified"
)

// MediaTLSAnnotation records, as Unverified, a BMC that fetches virtual
// media without verifying the media service's certificate. Like the
// qualification annotation, it stands in for commissioning evidence.
const MediaTLSAnnotation = "fleet.datumapis.com/virtual-media-tls"

// mediaEndpoint is a media service as https://IPv4:port.
var mediaEndpoint = regexp.MustCompile(`^https://(\d{1,3}\.){3}\d{1,3}:\d{1,5}$`)

// downloadName is a name the line-oriented config medium can carry.
var downloadName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,62}$`)

// maxDownloads bounds what one config medium lists.
const maxDownloads = 8

// pinned is a reference that names exactly one digest.
var pinned = regexp.MustCompile(`^[^@\s]+@sha256:[0-9a-f]{64}$`)

// checkDownloads refuses an installer download that is not pinned by
// digest, so nothing the server installs is chosen after admission.
func checkDownloads(downloads []ops.DownloadInput) *Refusal {
	if len(downloads) > maxDownloads {
		return refuse(ReasonDeliveryInputs, "an installer lists at most %d downloads", maxDownloads)
	}
	for _, download := range downloads {
		if !downloadName.MatchString(download.Name) {
			return refuse(ReasonDeliveryInputs, "an installer download name is not a lowercase name")
		}
		if !pinned.MatchString(download.Reference) {
			return refuse(ReasonDownloadUnpinned, "the installer download %q is not pinned by digest", download.Name)
		}
	}
	return nil
}

var deliveryReasons = []Reason{{Name: ReasonDeliveryNotOffered, RaisedBy: RaisedByCoordinator}, {Name: ReasonDeliveryUnqualified, RaisedBy: RaisedByCoordinator},
	{Name: ReasonDeliveryMismatch, RaisedBy: RaisedByCoordinator}, {Name: ReasonDeliveryInputs, RaisedBy: RaisedByBoth},
	{Name: ReasonDownloadUnpinned, RaisedBy: RaisedByCoordinator}, {Name: ReasonReportAddressRequired, RaisedBy: RaisedByBoth}, {Name: ReasonMediaTLSUnverified, RaisedBy: RaisedByCoordinator},
	{Name: ReasonMediaWindowRefused, RaisedBy: RaisedByWorker}}

// ExtraDeliveryMethod is the snapshot field that records the method an
// admitted operation prepares. A recovery hold keeps the held operation's
// snapshot, so the hold records the method it must be settled with.
const ExtraDeliveryMethod = "deliveryMethod"

// QualifiedMethodsAnnotation lists, comma separated, the delivery methods
// the Device's server is qualified for.
//
// It stands in for commissioning, which is not implemented: commissioning
// will record qualification as evidence. A Device without the annotation is
// qualified only for network boot, which discovery's boot target check
// already covers.
const QualifiedMethodsAnnotation = "fleet.datumapis.com/qualified-delivery-methods"

// QualifiedMethods returns the methods the Device's server is qualified for.
func QualifiedMethods(device *inventory.Device) []ops.DeliveryMethod {
	value, ok := device.Annotations[QualifiedMethodsAnnotation]
	if !ok {
		return []ops.DeliveryMethod{ops.DeliveryNetworkBoot}
	}
	var out []ops.DeliveryMethod
	for _, method := range strings.Split(value, ",") {
		if method := ops.DeliveryMethod(strings.TrimSpace(method)); slices.Contains(ops.DeliveryMethods, method) && !slices.Contains(out, method) {
			out = append(out, method)
		}
	}
	return out
}

// HeldMethod returns the delivery method a recovery hold of the kind
// records, and false if custody holds no such hold with a recorded method.
func HeldMethod(custody *ops.DeviceCustody, kind string) (ops.DeliveryMethod, bool) {
	hold := custody.Spec.RecoveryHold
	if hold == nil || hold.Kind != kind || hold.Snapshot == nil || hold.Snapshot.Extra[ExtraDeliveryMethod] == "" {
		return "", false
	}
	return ops.DeliveryMethod(hold.Snapshot.Extra[ExtraDeliveryMethod]), true
}

// SelectMethod is the requester's choice for a new operation: the method a
// held installation was prepared with, or else the first method in the
// site's order that the server is qualified for. Admission checks the
// recorded choice again.
func SelectMethod(offered []OfferedMethod, qualified []ops.DeliveryMethod, custody *ops.DeviceCustody) (ops.DeliveryInput, bool) {
	held, settling := HeldMethod(custody, "DeviceInstallation")
	for _, offer := range offered {
		if (settling && offer.Method == held) || (!settling && slices.Contains(qualified, offer.Method)) {
			return offer.Input(), true
		}
	}
	// A held method the site no longer offers is still the only one that
	// can prove its release, but its media service is no longer declared:
	// the hold waits until the site offers the method again.
	return ops.DeliveryInput{}, false
}

// offered returns the methods every site serving the Device offers, in the
// first site's order. A Device no site serves is offered nothing.
func offered(sites Sites, device *inventory.Device) []OfferedMethod {
	if sites == nil || device == nil {
		return nil
	}
	serving := sites.Serving(device)
	if len(serving) == 0 {
		return nil
	}
	var out []OfferedMethod
	for _, offer := range sites.DeliveryMethods(serving[0]) {
		everywhere := true
		for _, other := range serving[1:] {
			// Offers hold window pointers, so they compare by value.
			everywhere = everywhere && slices.ContainsFunc(sites.DeliveryMethods(other), func(o OfferedMethod) bool { return equalInput(o.Input(), offer.Input()) })
		}
		if everywhere {
			out = append(out, offer)
		}
	}
	return out
}

// checkDelivery is the installation's delivery check. An installation that
// settles a held installation must use the held method, and skips the site
// and qualification checks, which the held operation passed. Any other
// installation must use a method every serving site offers and the server
// is qualified for.
func checkDelivery(admission *Admission, spec *ops.DeviceInstallationSpec) *Refusal {
	method := spec.Delivery.Method
	switch method {
	case ops.DeliveryNetworkBoot:
		if spec.Delivery != (ops.DeliveryInput{Method: ops.DeliveryNetworkBoot}) {
			return refuse(ReasonDeliveryInputs, "network boot records no virtual media inputs")
		}
	case ops.DeliveryVirtualMedia:
		if spec.BootNetwork != nil {
			return refuse(ReasonDeliveryInputs, "a virtual media installation carries no boot network")
		}
		if spec.Delivery.Addresses != ops.AddressesDataNetwork {
			return refuse(ReasonDeliveryInputs, "virtual media supports only the DataNetwork address mode")
		}
		if !mediaEndpoint.MatchString(spec.Delivery.MediaService) || DeliveryDeadline(spec.Delivery) <= 0 || spec.Delivery.FetchWindow.Duration <= 0 || spec.Delivery.ReportWindow.Duration <= 0 {
			return refuse(ReasonDeliveryInputs, "virtual media records its media service and both phase windows")
		}
		if report := spec.Delivery.ReportAddress; report == nil || !validHostAddress(report.Address) {
			return refuse(ReasonReportAddressRequired, "virtual media records the Machine's IPAM address, which the installed OS reports from")
		}
	default:
		return refuse(ReasonDeliveryInputs, "the delivery method %q is not known", method)
	}
	if refusal := checkDownloads(spec.Image.Downloads); refusal != nil {
		return refusal
	}
	if method == ops.DeliveryVirtualMedia && admission.Device.Annotations[MediaTLSAnnotation] == "Unverified" {
		return refuse(ReasonMediaTLSUnverified, "the server's BMC does not verify the media service's certificate, and the unverified fallback is not implemented")
	}
	held, settling := HeldMethod(admission.Custody, "DeviceInstallation")
	if settling && held != method {
		return refuse(ReasonDeliveryMismatch, "the held installation was prepared with %s, so only %s can prove its release", held, held)
	}
	// A settling installation still takes its inputs from the site's offer;
	// only the qualification check, which the held operation passed, is
	// skipped.
	index := slices.IndexFunc(admission.Offered, func(offer OfferedMethod) bool { return offer.Method == method })
	if index < 0 {
		return refuse(ReasonDeliveryNotOffered, "no site serving the Device offers %s", method)
	}
	if want := admission.Offered[index].Input(); !equalInput(want, spec.Delivery) {
		return refuse(ReasonDeliveryInputs, "the site's inputs for %s are not the recorded ones", method)
	}
	if !settling && !slices.Contains(QualifiedMethods(admission.Device), method) {
		return refuse(ReasonDeliveryUnqualified, "the server is not qualified for %s", method)
	}
	return nil
}

// checkWipeDelivery refuses a wipe that would settle an installation hold
// prepared with a method other than network boot, the only method a wipe
// uses: it could not prove that method's release.
func checkWipeDelivery(admission *Admission) *Refusal {
	if held, ok := HeldMethod(admission.Custody, "DeviceInstallation"); ok && held != ops.DeliveryNetworkBoot {
		return refuse(ReasonDeliveryMismatch, "the held installation was prepared with %s, which a wipe cannot release", held)
	}
	return nil
}

// NetworkBootOnly is the declaration of a site that offers only network
// boot.
var NetworkBootOnly = []OfferedMethod{{Method: ops.DeliveryNetworkBoot}}

// OnlyNetworkBoot refuses an installation whose method is not network boot.
// Every site worker that delivers only network boot calls it.
func OnlyNetworkBoot(delivery ops.DeliveryInput) *Refusal {
	if delivery != (ops.DeliveryInput{Method: ops.DeliveryNetworkBoot}) {
		return refuse(ReasonDeliveryInputs, "this site's workers deliver only network boot")
	}
	return nil
}

// validHostAddress reports whether address is one IP address without a
// prefix length or zone.
func validHostAddress(address string) bool {
	parsed, err := netip.ParseAddr(address)
	return err == nil && parsed.Zone() == "" && parsed.String() == address
}

// equalInput compares the site's inputs for a method. The report address
// is the operation's own, not the site's, so it is not compared.
func equalInput(a, b ops.DeliveryInput) bool {
	duration := func(d *metav1.Duration) time.Duration {
		if d == nil {
			return -1
		}
		return d.Duration
	}
	return a.Method == b.Method && a.Addresses == b.Addresses && a.MediaService == b.MediaService &&
		duration(a.FetchWindow) == duration(b.FetchWindow) && duration(a.ReportWindow) == duration(b.ReportWindow)
}

// ReasonHoldMethodNotOffered says a hold waits until a site serving the
// Device offers the held installation's delivery method again: only that
// method can prove its release.
const ReasonHoldMethodNotOffered = "HeldMethodNotOffered"

// holdWaiting records on the recovery hold, for staff, whether it waits for
// a site to offer the held method again, and clears it once one does.
func (c *Coordinator) holdWaiting(ctx context.Context, custody *ops.DeviceCustody, device *inventory.Device) (Outcome, error) {
	hold := custody.Spec.RecoveryHold
	if hold == nil || custody.Spec.ActiveOperation != nil {
		return Outcome{}, nil
	}
	var want *ops.HoldWaiting
	if method, held := HeldMethod(custody, "DeviceInstallation"); held {
		if !slices.ContainsFunc(offered(c.Sites, device), func(offer OfferedMethod) bool { return offer.Method == method }) {
			want = &ops.HoldWaiting{Reason: ReasonHoldMethodNotOffered, Message: fmt.Sprintf("waits for a site serving the Device to offer %s again", method)}
		}
	}
	if equalWaiting(hold.Waiting, want) {
		return Outcome{}, nil
	}
	hold.Waiting = want
	return c.writeCustody(ctx, custody)
}

func equalWaiting(a, b *ops.HoldWaiting) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
