// Package inventoryobserver refreshes InventoryObservation status from an
// external inventory system. It is the sole status writer for those objects. It
// never creates, deletes, or edits the spec of any object, and it writes no
// other kind: observations confer no supply, trust, or authority on hardware.
package inventoryobserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	inventoryv1 "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	"go.datum.net/fleet/internal/netbox"
)

// Source is the read-only subset of the NetBox client the observer uses.
type Source interface {
	Device(ctx context.Context, id int64) (*netbox.Device, error)
	Interfaces(ctx context.Context, deviceID int64) ([]netbox.Interface, error)
}

type Observer struct {
	Client    client.Client
	Namespace string
	NetBox    Source
	Now       func() time.Time

	// rejected stops contact with the source for the rest of a run after it
	// rejects the credential. Sources commonly ban clients that repeat failed
	// authentication, and a ban would also block every other client sharing
	// the observer's egress address.
	rejected bool
}

// Result reports one observation's refresh. Reason is the Observed condition
// reason written; WriteErr is set when that status could not be persisted.
type Result struct {
	Name     string
	Reason   string
	Message  string
	WriteErr error
}

// Succeeded reports whether the observation now holds a current, matching read.
func (r Result) Succeeded() bool { return r.WriteErr == nil && r.Reason == inventoryv1.ReasonObserved }

// RefreshAll refreshes every NetBox observation in the namespace. A failure for
// one observation is recorded in its status and does not stop the others.
func (o *Observer) RefreshAll(ctx context.Context) ([]Result, error) {
	if o.Client == nil || o.NetBox == nil || o.Namespace == "" {
		return nil, errors.New("observer requires a client, a source, and a namespace")
	}
	var list inventoryv1.InventoryObservationList
	if err := o.Client.List(ctx, &list, client.InNamespace(o.Namespace)); err != nil {
		return nil, fmt.Errorf("list inventory observations: %w", err)
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })
	o.rejected = false
	results := make([]Result, 0, len(list.Items))
	for i := range list.Items {
		results = append(results, o.refresh(ctx, &list.Items[i]))
	}
	return results, nil
}

func (o *Observer) now() time.Time {
	if o.Now != nil {
		return o.Now().UTC()
	}
	return time.Now().UTC()
}

func (o *Observer) refresh(ctx context.Context, observation *inventoryv1.InventoryObservation) Result {
	now := metav1.NewTime(o.now().Truncate(time.Second))
	status := observation.DeepCopy().Status
	status.TrustProfile = inventoryv1.TrustProfileObservedOnly
	status.ObservedGeneration = observation.Generation
	status.LastAttemptTime = &now

	reason, message := o.observe(ctx, observation, &status, now)
	apimeta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               inventoryv1.ConditionObserved,
		Status:             conditionStatus(reason == inventoryv1.ReasonObserved),
		Reason:             reason,
		Message:            message,
		ObservedGeneration: observation.Generation,
		LastTransitionTime: now,
	})
	observation.Status = status
	result := Result{Name: observation.Name, Reason: reason, Message: message}
	// Update carries the listed resourceVersion: a concurrent spec or status
	// change makes this write fail instead of overwriting newer state.
	if err := o.Client.Status().Update(ctx, observation); err != nil {
		result.WriteErr = err
	}
	return result
}

func (o *Observer) observe(ctx context.Context, observation *inventoryv1.InventoryObservation, status *inventoryv1.InventoryObservationStatus, now metav1.Time) (string, string) {
	source := observation.Spec.Source
	if source.System != inventoryv1.SourceSystemNetBox || source.Kind != inventoryv1.SourceKindDevice {
		return inventoryv1.ReasonUnsupportedSource, fmt.Sprintf("source %s/%s is not supported by this observer", source.System, source.Kind)
	}
	if o.rejected {
		return inventoryv1.ReasonSourceUnauthorized, "source not contacted: it rejected the observer credential earlier in this run; previously observed facts are retained"
	}
	device, err := o.NetBox.Device(ctx, source.ID)
	if err != nil {
		return o.sourceFailure(err)
	}
	if source.ExpectedName != "" && device.Name != source.ExpectedName {
		return inventoryv1.ReasonIdentityMismatch, fmt.Sprintf("source object %d is named %q, expected %q; facts were not updated", source.ID, device.Name, source.ExpectedName)
	}
	if previous := status.Facts; previous != nil && previous.Serial != "" && device.Serial != previous.Serial {
		return inventoryv1.ReasonIdentityMismatch, fmt.Sprintf("source object %d now reports a different serial than previously observed; facts were not updated, create a new observation for replaced hardware", source.ID)
	}
	interfaces, err := o.NetBox.Interfaces(ctx, source.ID)
	if err != nil {
		return o.sourceFailure(err)
	}
	facts := Normalize(device, interfaces)
	digest, err := Digest(facts)
	if err != nil {
		return inventoryv1.ReasonSourceUnavailable, "normalize source facts: " + err.Error()
	}
	status.Facts = facts
	status.FactsDigest = digest
	status.ObservedAt = &now
	status.SourceUpdatedAt = nil
	if device.LastUpdated != nil {
		updated := metav1.NewTime(device.LastUpdated.UTC())
		status.SourceUpdatedAt = &updated
	}
	return inventoryv1.ReasonObserved, "read the source object; facts are observed, not verified"
}

func (o *Observer) sourceFailure(err error) (string, string) {
	if errors.Is(err, netbox.ErrUnauthorized) {
		o.rejected = true
	}
	switch {
	case errors.Is(err, netbox.ErrNotFound):
		return inventoryv1.ReasonSourceObjectMissing, "source object was not found; previously observed facts are retained"
	case errors.Is(err, netbox.ErrUnauthorized):
		return inventoryv1.ReasonSourceUnauthorized, "source rejected the observer credential; previously observed facts are retained"
	default:
		return inventoryv1.ReasonSourceUnavailable, "source read failed; previously observed facts are retained: " + err.Error()
	}
}

func conditionStatus(ok bool) metav1.ConditionStatus {
	if ok {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}

// Normalize converts allowlisted NetBox fields into deterministic facts.
func Normalize(device *netbox.Device, interfaces []netbox.Interface) *inventoryv1.ObservedDeviceFacts {
	facts := &inventoryv1.ObservedDeviceFacts{
		Name:         device.Name,
		Manufacturer: device.DeviceType.Manufacturer.Name,
		Model:        device.DeviceType.Model,
		Serial:       strings.TrimSpace(device.Serial),
		Role:         nameOf(device.Role),
		Status:       valueOf(device.Status),
		Placement: inventoryv1.ObservedPlacement{
			Site: slugOf(device.Site),
			Rack: nameOf(device.Rack),
			Face: valueOf(device.Face),
		},
	}
	if device.AssetTag != nil {
		facts.AssetTag = strings.TrimSpace(*device.AssetTag)
	}
	if device.Position != nil {
		facts.Placement.Position = strconv.FormatFloat(*device.Position, 'f', -1, 64)
	}
	addAddress := func(role string, value string) {
		if value != "" {
			facts.Addresses = append(facts.Addresses, inventoryv1.ObservedAddress{Role: role, Address: value})
		}
	}
	if device.PrimaryIP4 != nil {
		addAddress("PrimaryIPv4", device.PrimaryIP4.Address)
	}
	if device.PrimaryIP6 != nil {
		addAddress("PrimaryIPv6", device.PrimaryIP6.Address)
	}
	if device.OOBIP != nil {
		addAddress("OutOfBand", device.OOBIP.Address)
	}
	for _, source := range interfaces {
		observed := inventoryv1.ObservedInterface{
			Name:           source.Name,
			Type:           valueOf(source.Type),
			Enabled:        source.Enabled,
			ManagementOnly: source.MgmtOnly,
			LAG:            nameOf(source.LAG),
			MACAddresses:   macAddresses(source),
		}
		if source.Speed != nil {
			observed.SpeedKbps = *source.Speed
		}
		for _, peer := range source.LinkPeers {
			port := peer.Name
			if port == "" {
				port = peer.Display
			}
			observed.Peers = append(observed.Peers, inventoryv1.ObservedPeer{Kind: source.LinkPeersType, Device: nameOf(peer.Device), Port: port})
		}
		sort.Slice(observed.Peers, func(i, j int) bool {
			if observed.Peers[i].Device != observed.Peers[j].Device {
				return observed.Peers[i].Device < observed.Peers[j].Device
			}
			return observed.Peers[i].Port < observed.Peers[j].Port
		})
		facts.Interfaces = append(facts.Interfaces, observed)
	}
	sort.Slice(facts.Interfaces, func(i, j int) bool { return facts.Interfaces[i].Name < facts.Interfaces[j].Name })
	return facts
}

func macAddresses(source netbox.Interface) []string {
	seen := map[string]bool{}
	add := func(value string) {
		if parsed, err := net.ParseMAC(strings.TrimSpace(value)); err == nil && len(parsed) == 6 {
			seen[parsed.String()] = true
		}
	}
	if source.MACAddress != nil {
		add(*source.MACAddress)
	}
	if source.PrimaryMAC != nil {
		add(source.PrimaryMAC.MACAddress)
	}
	for _, entry := range source.MACAddresses {
		add(entry.MACAddress)
	}
	out := make([]string, 0, len(seen))
	for value := range seen {
		out = append(out, value)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

// Digest is a stable content digest of normalized facts.
func Digest(facts *inventoryv1.ObservedDeviceFacts) (string, error) {
	encoded, err := json.Marshal(facts)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func nameOf(ref *netbox.Ref) string {
	if ref == nil {
		return ""
	}
	return ref.Name
}

func slugOf(ref *netbox.Ref) string {
	if ref == nil {
		return ""
	}
	if ref.Slug != "" {
		return ref.Slug
	}
	return ref.Name
}

func valueOf(ref *netbox.Ref) string {
	if ref == nil {
		return ""
	}
	return ref.Value
}
