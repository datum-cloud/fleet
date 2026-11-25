package bmcdiscovery

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	"go.datum.net/fleet/internal/redfish"
)

// BMC is the read-only Redfish surface discovery uses.
type BMC interface {
	// Probe completes an unauthenticated TLS handshake and one anonymous GET
	// of the service root. It never holds a credential.
	Probe(ctx context.Context, endpoint string) (redfish.Certificate, redfish.ServiceRoot, error)
	// Inventory reads the BMC with the credential, over a connection whose
	// handshake fails unless the leaf public key matches pin.
	Inventory(ctx context.Context, endpoint, user, password, pin string) (redfish.Inventory, error)
}

// RedfishBMC is the BMC over internal/redfish.
type RedfishBMC struct{ Timeout time.Duration }

func (b RedfishBMC) Probe(ctx context.Context, endpoint string) (redfish.Certificate, redfish.ServiceRoot, error) {
	return redfish.Probe(ctx, endpoint, b.Timeout)
}

func (b RedfishBMC) Inventory(ctx context.Context, endpoint, user, password, pin string) (redfish.Inventory, error) {
	c, err := redfish.NewPinnedClient(endpoint, user, password, pin, b.Timeout)
	if err != nil {
		return redfish.Inventory{}, err
	}
	return c.Inventory(ctx)
}

// Condition reasons.
const (
	ReasonEndpointInvalid       = "EndpointInvalid"
	ReasonConnectFailed         = "ConnectFailed"
	ReasonServiceRootRead       = "ServiceRootRead"
	ReasonServiceRootUnreadable = "ServiceRootUnreadable"
	ReasonNotAttempted          = "NotAttempted"
	ReasonCredentialConflict    = "CredentialConflict"
	ReasonCredentialInvalid     = "CredentialInvalid"
	ReasonPinNotApproved        = "PinNotApproved"
	ReasonPinMismatch           = "PinMismatch"
	ReasonCredentialRejected    = "CredentialRejected"
	ReasonCredentialAccepted    = "CredentialAccepted"
	ReasonReadFailed            = "ReadFailed"
	ReasonUnreachable           = "Unreachable"
	ReasonInventoryRead         = "InventoryRead"
	ReasonNotObserved           = "NotObserved"
	ReasonStaleObservation      = "StaleObservation"
	ReasonAmbiguousSystem       = "AmbiguousSystem"
	ReasonMatched               = "Matched"
	ReasonSystemUUIDMismatch    = "SystemUUIDMismatch"
	ReasonSerialMismatch        = "SerialMismatch"
	ReasonModelMismatch         = "ModelMismatch"
	ReasonUnique                = "Unique"
	ReasonSharedPublicKey       = "SharedPublicKey"
)

// attempt is the outcome of contacting one BMC once.
type attempt struct {
	at       metav1.Time
	endpoint string
	cert     *redfish.Certificate
	root     *redfish.ServiceRoot
	inv      *redfish.Inventory
	// conditions set by the attempt: Reachable, Authenticated, Discovered.
	conditions []metav1.Condition
	// uncertain attempts are retried with backoff; definite refusals wait for
	// the interval or a credential change. A rejected credential is never
	// retried quickly, so discovery cannot lock out the BMC account.
	uncertain bool
}

func cond(kind string, status metav1.ConditionStatus, reason, message string) metav1.Condition {
	return metav1.Condition{Type: kind, Status: status, Reason: reason, Message: message}
}

// contact runs one attempt. A credential is passed to the BMC only after the
// anonymous probe observed exactly the approved pin, and the authenticated
// connection re-checks that pin in its own handshake before any request.
func contact(ctx context.Context, bmc BMC, endpoint string, cred Credential, credErr error, now time.Time) attempt {
	a := attempt{at: metav1.NewTime(now.UTC().Truncate(time.Second)), endpoint: endpoint}
	set := func(reachable, authenticated, discovered metav1.Condition) attempt {
		a.conditions = []metav1.Condition{reachable, authenticated, discovered}
		return a
	}
	notAttempted := cond(inventory.ConditionAuthenticated, metav1.ConditionUnknown, ReasonNotAttempted, "no credential was sent")
	canonical, err := redfish.ParseEndpoint(endpoint)
	if err != nil || !strings.HasPrefix(canonical, "https://") {
		msg := "spec.bmc.endpoint must be https://IPv4:port"
		return set(cond(inventory.ConditionReachable, metav1.ConditionFalse, ReasonEndpointInvalid, msg), notAttempted,
			cond(inventory.ConditionDiscovered, metav1.ConditionFalse, ReasonEndpointInvalid, msg))
	}
	cert, root, err := bmc.Probe(ctx, canonical)
	if errors.Is(err, redfish.ErrNoHandshake) || (err != nil && cert.PublicKeySHA256 == "") {
		a.uncertain = true
		return set(cond(inventory.ConditionReachable, metav1.ConditionFalse, ReasonConnectFailed, "no TLS handshake completed with the BMC endpoint"), notAttempted,
			cond(inventory.ConditionDiscovered, metav1.ConditionUnknown, ReasonUnreachable, "the BMC was not reached; earlier observations are retained"))
	}
	a.cert = &cert
	reachable := cond(inventory.ConditionReachable, metav1.ConditionTrue, ReasonServiceRootRead, "TLS handshake completed and the anonymous service root was read")
	if err != nil {
		// The service root may be briefly unavailable (for example a 503
		// while the BMC boots), so retry with backoff whatever the credential
		// state turns out to be.
		a.uncertain = true
		reachable = cond(inventory.ConditionReachable, metav1.ConditionTrue, ReasonServiceRootUnreadable, "TLS handshake completed; the anonymous service root read failed: "+safe(err))
	} else {
		a.root = &root
	}
	refuse := func(reason, msg string) attempt {
		return set(reachable, cond(inventory.ConditionAuthenticated, metav1.ConditionFalse, reason, msg+"; no credential was sent"),
			cond(inventory.ConditionDiscovered, metav1.ConditionFalse, reason, msg))
	}
	switch {
	case errors.Is(credErr, errCredentialConflict):
		return refuse(ReasonCredentialConflict, "more than one credential Secret names this Device")
	case credErr != nil:
		return refuse(ReasonCredentialInvalid, "the credential Secret needs a single-line username and password and, if set, a hex SHA-256 tlsPublicKeySHA256")
	case cred.Pin == "":
		return refuse(ReasonPinNotApproved, "the observed public key is in status.discovery.tls; verify it out of band and approve it as tlsPublicKeySHA256 in the credential Secret")
	case cred.Pin != cert.PublicKeySHA256:
		return refuse(ReasonPinMismatch, "the BMC presented a public key other than the approved pin")
	}
	inv, err := bmc.Inventory(ctx, canonical, cred.Username, cred.Password, cred.Pin)
	switch {
	case errors.Is(err, redfish.ErrPinMismatch):
		return refuse(ReasonPinMismatch, "the BMC presented a public key other than the approved pin")
	case errors.Is(err, redfish.ErrUnauthorized):
		return set(reachable, cond(inventory.ConditionAuthenticated, metav1.ConditionFalse, ReasonCredentialRejected, "the BMC rejected the credential; it is retried only after the interval or a Secret change"),
			cond(inventory.ConditionDiscovered, metav1.ConditionFalse, ReasonCredentialRejected, "the BMC rejected the credential"))
	case err != nil:
		a.uncertain = true
		return set(reachable, cond(inventory.ConditionAuthenticated, metav1.ConditionUnknown, ReasonReadFailed, "the authenticated read did not complete: "+safe(err)),
			cond(inventory.ConditionDiscovered, metav1.ConditionUnknown, ReasonReadFailed, "the inventory read did not complete; earlier observations are retained: "+safe(err)))
	}
	a.inv = &inv
	return set(reachable, cond(inventory.ConditionAuthenticated, metav1.ConditionTrue, ReasonCredentialAccepted, "the BMC accepted the credential over the pinned connection"),
		cond(inventory.ConditionDiscovered, metav1.ConditionTrue, ReasonInventoryRead, "managers, systems and their Ethernet interfaces were read"))
}

// safe keeps only redfish's own outcome classes, which never carry a URL,
// header or response body.
func safe(err error) string {
	var status redfish.Status
	switch {
	case errors.As(err, &status):
		return status.Error()
	case errors.Is(err, redfish.ErrIdentity):
		return "the BMC returned a link outside the Redfish service"
	default:
		return "transport failure or unreadable response"
	}
}

// apply records an attempt into status. Each observation replaces only its
// own retained section, so a failed read keeps the previous one.
func apply(status *inventory.DeviceStatus, a attempt, generation int64) {
	if status.Discovery == nil {
		status.Discovery = &inventory.DeviceDiscovery{}
	}
	d := status.Discovery
	d.LastAttemptAt, d.LastAttemptEndpoint = &a.at, a.endpoint
	if a.cert != nil {
		t := &inventory.ObservedTLS{ObservedAt: a.at, Endpoint: a.endpoint, PublicKeySHA256: a.cert.PublicKeySHA256, SelfSigned: a.cert.SelfSigned}
		if !a.cert.NotAfter.IsZero() {
			notAfter := metav1.NewTime(a.cert.NotAfter)
			t.NotAfter = &notAfter
		}
		d.TLS = t
	}
	if a.root != nil {
		d.ServiceRoot = &inventory.ObservedServiceRoot{ObservedAt: a.at, Endpoint: a.endpoint, RedfishVersion: clip(a.root.RedfishVersion, 63),
			UUID: canonicalUUIDOrEmpty(a.root.UUID), Vendor: clip(a.root.Vendor, 253), Product: clip(a.root.Product, 253)}
	}
	if a.inv != nil {
		d.Inventory = observedInventory(*a.inv, a.at, a.endpoint)
	}
	for _, c := range a.conditions {
		setCondition(status, c, generation, a.at)
	}
}

func setCondition(status *inventory.DeviceStatus, c metav1.Condition, generation int64, now metav1.Time) {
	c.ObservedGeneration, c.LastTransitionTime = generation, now
	meta.SetStatusCondition(&status.Conditions, c)
}

func observedInventory(inv redfish.Inventory, at metav1.Time, endpoint string) *inventory.ObservedInventory {
	nics := func(in []redfish.NetworkInterface) []inventory.ObservedNetworkInterface {
		var out []inventory.ObservedNetworkInterface
		for _, n := range in {
			if n.ID == "" || len(out) == 32 {
				continue
			}
			out = append(out, inventory.ObservedNetworkInterface{ID: clip(n.ID, 253), MACAddress: n.MACAddress, LinkStatus: clip(n.LinkStatus, 63), SpeedMbps: min(n.SpeedMbps, 100000000)})
		}
		return out
	}
	out := &inventory.ObservedInventory{ObservedAt: at, Endpoint: endpoint}
	for _, m := range inv.Managers {
		if m.ID != "" && len(out.Managers) < 16 {
			out.Managers = append(out.Managers, inventory.ObservedManager{ID: clip(m.ID, 253), Model: clip(m.Model, 253), FirmwareVersion: clip(m.FirmwareVersion, 253), NetworkInterfaces: nics(m.NetworkInterfaces)})
		}
	}
	for _, s := range inv.Systems {
		if s.ID != "" && len(out.Systems) < 16 {
			out.Systems = append(out.Systems, inventory.ObservedSystem{ID: clip(s.ID, 253), UUID: canonicalUUIDOrEmpty(s.UUID), Manufacturer: clip(s.Manufacturer, 253),
				Model: clip(s.Model, 253), SerialNumber: clip(s.SerialNumber, 253), SKU: clip(s.SKU, 253), PowerState: clip(s.PowerState, 63), NetworkInterfaces: nics(s.NetworkInterfaces),
				Storage: storage(s)})
		}
	}
	return out
}

// storage keeps the drives a System reports. A System that links no Storage
// collection keeps nil, so a reader can tell unknown from empty.
func storage(s redfish.InventorySystem) *inventory.ObservedStorage {
	if !s.StorageReported {
		return nil
	}
	out := &inventory.ObservedStorage{}
	if len(s.Drives) > redfish.MaxInventoryDrives {
		return nil
	}
	for _, d := range s.Drives {
		// A drive that cannot be recorded makes the list incomplete, so
		// storage stays unknown rather than looking complete.
		if clip(d.ID, 253) == "" {
			return nil
		}
		out.Drives = append(out.Drives, inventory.ObservedDrive{ID: clip(d.ID, 253), SerialNumber: clip(d.SerialNumber, 253), Model: clip(d.Model, 253), Firmware: clip(d.Revision, 63),
			Protocol: clip(d.Protocol, 31), MediaType: clip(d.MediaType, 31), CapacityBytes: max(d.CapacityBytes, 0), EncryptionStatus: clip(d.EncryptionStatus, 63), InVolume: d.InVolume})
	}
	return out
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}

func canonicalUUIDOrEmpty(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) != 36 || strings.Count(s, "-") != 4 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' || strings.Trim(s, "0123456789abcdef-") != "" {
		return ""
	}
	return s
}

// derive recomputes the conditions that compare observations with spec and
// with other Devices. It never changes an observation.
func derive(status *inventory.DeviceStatus, device *inventory.Device, sharing []string, now metav1.Time) {
	setCondition(status, identityCondition(status, device), device.Generation, now)
	setCondition(status, uniqueCondition(status, sharing), device.Generation, now)
}

func normalize(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

// modelMatches allows a vendor family prefix on either side ("PowerEdge X1"
// against "X1"): one normalized model must equal the other or end with it as
// whole words.
func modelMatches(expected, observed string) bool {
	e, o := normalize(expected), normalize(observed)
	return e == o || (e != "" && strings.HasSuffix(o, " "+e)) || (o != "" && strings.HasSuffix(e, " "+o))
}

// serialMatches compares the spec serial with the System serial and SKU:
// some vendors report the chassis service tag as the SKU.
func serialMatches(expected string, s inventory.ObservedSystem) bool {
	e := normalize(expected)
	return e != "" && (e == normalize(s.SerialNumber) || e == normalize(s.SKU))
}

func identityCondition(status *inventory.DeviceStatus, device *inventory.Device) metav1.Condition {
	kind := inventory.ConditionIdentityMatched
	if status.Discovery == nil || status.Discovery.Inventory == nil {
		return cond(kind, metav1.ConditionUnknown, ReasonNotObserved, "no inventory has been read")
	}
	inv := status.Discovery.Inventory
	if device.Spec.BMC == nil || inv.Endpoint != device.Spec.BMC.Endpoint {
		return cond(kind, metav1.ConditionUnknown, ReasonStaleObservation, "the retained inventory was read from another endpoint")
	}
	want := device.Spec.Identity
	var candidates []inventory.ObservedSystem
	switch {
	case want.SystemUUID != "":
		for _, s := range inv.Systems {
			if s.UUID == want.SystemUUID {
				candidates = append(candidates, s)
			}
		}
		if len(candidates) == 0 {
			return cond(kind, metav1.ConditionFalse, ReasonSystemUUIDMismatch, "no System reports spec.identity.systemUUID; spec is not changed")
		}
	case want.Serial != "":
		for _, s := range inv.Systems {
			if serialMatches(want.Serial, s) {
				candidates = append(candidates, s)
			}
		}
		if len(candidates) == 0 {
			return cond(kind, metav1.ConditionFalse, ReasonSerialMismatch, "no System reports spec.identity.serial as its serial number or SKU; spec is not changed")
		}
	default:
		candidates = inv.Systems
	}
	if len(candidates) != 1 {
		return cond(kind, metav1.ConditionUnknown, ReasonAmbiguousSystem, fmt.Sprintf("%d Systems could be this Device", len(candidates)))
	}
	s := candidates[0]
	var compared []string
	switch {
	case want.SystemUUID != "" && want.Serial != "" && !serialMatches(want.Serial, s):
		return cond(kind, metav1.ConditionFalse, ReasonSerialMismatch, "the System with spec.identity.systemUUID reports another serial; spec is not changed")
	case !modelMatches(want.Model, s.Model):
		return cond(kind, metav1.ConditionFalse, ReasonModelMismatch, "the System reports a model other than spec.identity.model; spec is not changed")
	}
	if want.SystemUUID != "" {
		compared = append(compared, "systemUUID")
	}
	if want.Serial != "" {
		compared = append(compared, "serial")
	}
	compared = append(compared, "model")
	return cond(kind, metav1.ConditionTrue, ReasonMatched, "System "+s.ID+" matches spec.identity ("+strings.Join(compared, ", ")+")")
}

func uniqueCondition(status *inventory.DeviceStatus, sharing []string) metav1.Condition {
	kind := inventory.ConditionPublicKeyUnique
	if status.Discovery == nil || status.Discovery.TLS == nil {
		return cond(kind, metav1.ConditionUnknown, ReasonNotObserved, "no TLS public key has been observed")
	}
	if len(sharing) == 0 {
		return cond(kind, metav1.ConditionTrue, ReasonUnique, "no other discovered Device presents this TLS public key")
	}
	sort.Strings(sharing)
	names := sharing
	if len(names) > 3 {
		names = names[:3]
	}
	return cond(kind, metav1.ConditionFalse, ReasonSharedPublicKey, fmt.Sprintf("%d other Devices present this TLS public key (%s); the pin cannot tell these BMCs apart",
		len(sharing), strings.Join(names, ", ")))
}

// sharingKey lists the other Devices whose retained TLS observation has the
// same public key as device.
func sharingKey(device *inventory.Device, status *inventory.DeviceStatus, all []inventory.Device) []string {
	if status.Discovery == nil || status.Discovery.TLS == nil {
		return nil
	}
	var out []string
	for i := range all {
		other := &all[i]
		if other.Namespace == device.Namespace && other.Name == device.Name {
			continue
		}
		if d := other.Status.Discovery; d != nil && d.TLS != nil && d.TLS.PublicKeySHA256 == status.Discovery.TLS.PublicKeySHA256 {
			out = append(out, other.Namespace+"/"+other.Name)
		}
	}
	return out
}
