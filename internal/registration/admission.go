// Package registration admits inert, inventory-backed MachineCapacity records.
//
// The webhook keeps no state of its own. Every decision is made against live
// Devices and capacities in the configured provider projects, so the API
// server remains the only store. One capacity per Device is enforced by the
// API server itself: the capacity name is derived from the Device UID, and two
// creates for the same Device collide on that name.
package registration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	metal "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	iam "go.miloapis.com/milo/pkg/apis/iam/v1alpha1"
	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	DeviceUIDAnnotation     = "fleet.datumapis.com/registration-device-uid"
	DeviceVersionAnnotation = "fleet.datumapis.com/registration-device-version"
	OperationAnnotation     = "fleet.datumapis.com/registration-operation"

	// CapacityNamePrefix starts every registered capacity name. The rest of
	// the name is the backing Device's UID.
	CapacityNamePrefix = "device-"

	// InventoryNamespace is the only namespace registration reads and writes.
	InventoryNamespace = "default"
)

// CapacityName is the only name a registered capacity for this Device
// incarnation may have. Because the API server rejects a second object with
// the same name, concurrent registrations of one Device cannot both persist.
func CapacityName(deviceUID types.UID) string {
	return CapacityNamePrefix + string(deviceUID)
}

type Project struct {
	Identity controller.ProjectIdentity
	Client   client.Client
}

type Handler struct {
	// Projects is the complete provider scope. Duplicate identities are
	// searched across all of it.
	Projects  []string
	Resolve   func(context.Context, string) (Project, error)
	Authorize func(context.Context, Project, authenticationv1.UserInfo, string, string, string) error
}

// Aliases returns the normalized hardware identifiers of a Device: serial,
// system UUID and, when present, the provider host reference. Two Devices
// sharing any alias may be the same machine.
func Aliases(device *inventory.Device) []string {
	var result []string
	pairs := [][2]string{{"serial", strings.ToLower(strings.TrimSpace(device.Spec.Identity.Serial))}, {"uuid", strings.ToLower(strings.TrimSpace(device.Spec.Identity.SystemUUID))}}
	if reference := device.Spec.ProviderHostRef; reference != nil {
		encoded, _ := json.Marshal([]string{strings.ToLower(strings.TrimSpace(reference.System)), strings.ToLower(strings.TrimSpace(reference.Type)), strings.ToLower(strings.TrimSpace(reference.ID))})
		pairs = append(pairs, [2]string{"provider", string(encoded)})
	}
	for _, pair := range pairs {
		if pair[1] != "" {
			digest := sha256.Sum256([]byte(pair[0] + ":" + pair[1]))
			result = append(result, pair[0]+":"+hex.EncodeToString(digest[:]))
		}
	}
	return result
}

var systemUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func eligible(device *inventory.Device) bool {
	if reference := device.Spec.ProviderHostRef; reference != nil && (strings.TrimSpace(reference.System) == "" || strings.TrimSpace(reference.Type) == "" || strings.TrimSpace(reference.ID) == "") {
		return false
	}
	serial := strings.ToLower(strings.TrimSpace(device.Spec.Identity.Serial))
	uuid := device.Spec.Identity.SystemUUID
	return inventory.CapacityBackingError(device) == nil && serial != "" && !slices.Contains([]string{"unknown", "none", "n/a", "default string", "to be filled by o.e.m."}, serial) && systemUUIDPattern.MatchString(uuid) && uuid != "00000000-0000-0000-0000-000000000000" && uuid != "ffffffff-ffff-ffff-ffff-ffffffffffff"
}

func projectName(actor authenticationv1.UserInfo) (string, error) {
	for key, expected := range map[string]string{iam.ParentKindExtraKey: "Project", iam.ParentAPIGroupExtraKey: "resourcemanager.miloapis.com"} {
		if values := actor.Extra[key]; len(values) != 1 || values[0] != expected {
			return "", errors.New("authenticated Milo project context is required")
		}
	}
	values := actor.Extra[iam.ParentNameExtraKey]
	if len(values) != 1 || values[0] == "" || actor.Username == "" || actor.UID == "" {
		return "", errors.New("authenticated actor and project identity are required")
	}
	return values[0], nil
}

func (handler *Handler) Handle(ctx context.Context, request admission.Request) admission.Response {
	if request.Resource.Group != metal.GroupVersion.Group || request.Resource.Resource != "machinecapacities" {
		return admission.Denied("unsupported registration admission resource")
	}
	if request.SubResource != "" || (request.Operation != admissionv1.Create && request.Operation != admissionv1.Update) {
		return admission.Denied(mutationDisabled)
	}
	name, err := projectName(request.UserInfo)
	if err != nil {
		return admission.Denied(err.Error())
	}
	if !slices.Contains(handler.Projects, name) {
		return admission.Denied("project is outside the configured provider registration scope")
	}
	if request.Operation == admissionv1.Update {
		return admission.Denied(handler.evaluateUpdate(ctx, name, request).Error())
	}
	if err := handler.evaluate(ctx, name, request); err != nil {
		return admission.Denied(err.Error())
	}
	return admission.Allowed("inert inventory registration only; no availability or preparation authority")
}

const mutationDisabled = "capacity mutation and deletion are disabled; registered and legacy capacity require operator reconciliation"

// evaluateUpdate explains why a capacity update is denied. Stage 1 denies
// every update. An update that would move capacity out of Disabled for a
// Device that is not Active is denied for that reason first, so the rule
// already holds when a future stage allows enabling capacity. It always
// returns an error.
func (handler *Handler) evaluateUpdate(ctx context.Context, name string, request admission.Request) error {
	var capacity metal.MachineCapacity
	if err := json.Unmarshal(request.Object.Raw, &capacity); err != nil {
		return err
	}
	if capacity.Spec.AllocationPolicy == "Disabled" {
		return errors.New(mutationDisabled)
	}
	project, err := handler.Resolve(ctx, name)
	if err != nil || !project.Identity.Valid() || project.Identity.RouteName != name {
		return errors.New("provider project lifetime is unavailable")
	}
	// The API server has authorized the capacity update itself. Reading the
	// backing Device additionally requires the caller to be able to read it.
	if err := handler.Authorize(ctx, project, request.UserInfo, inventory.GroupVersion.Group, "devices", capacity.Spec.DeviceRef.Name); err != nil {
		return err
	}
	device := &inventory.Device{}
	if err := project.Client.Get(ctx, client.ObjectKey{Namespace: request.Namespace, Name: capacity.Spec.DeviceRef.Name}, device); err != nil {
		if apierrors.IsNotFound(err) {
			return errors.New("capacity cannot leave Disabled: its Device does not exist")
		}
		return errors.New("capacity cannot leave Disabled: its Device is unavailable")
	}
	if lifecycle := inventory.LifecycleOf(device); lifecycle != inventory.DeviceActive {
		return fmt.Errorf("capacity cannot leave Disabled: its Device is %s; only an Active Device can back available capacity", lifecycle)
	}
	return errors.New(mutationDisabled)
}

func (handler *Handler) evaluate(ctx context.Context, name string, request admission.Request) error {
	if request.Namespace != InventoryNamespace {
		return errors.New("registration requires the default inventory namespace")
	}
	var capacity metal.MachineCapacity
	if err := json.Unmarshal(request.Object.Raw, &capacity); err != nil {
		return err
	}
	if capacity.Name == "" || capacity.Name != request.Name || capacity.Namespace != InventoryNamespace || capacity.GenerateName != "" || !capacity.DeletionTimestamp.IsZero() {
		return errors.New("an explicit capacity name in the default namespace is required")
	}
	if capacity.Spec.AllocationPolicy != "Disabled" || capacity.Spec.ReclaimPolicy != "Retain" || capacity.Spec.CleaningPolicyRef.Name != "" || !reflect.DeepEqual(capacity.Status, metal.MachineCapacityStatus{}) || len(capacity.OwnerReferences) != 0 || len(capacity.Finalizers) != 0 {
		return errors.New("only Disabled/Retain registration with an explicitly unconfigured cleaning policy and no status or ownership is supported")
	}
	annotations := capacity.Annotations
	if len(annotations) != 3 || annotations[DeviceUIDAnnotation] == "" || annotations[DeviceVersionAnnotation] == "" || annotations[OperationAnnotation] == "" {
		return errors.New("exact Device UID/resourceVersion and a client operation ID are required")
	}
	if len(annotations[OperationAnnotation]) > 128 {
		return errors.New("registration operation is too long")
	}
	if capacity.Name != CapacityName(types.UID(annotations[DeviceUIDAnnotation])) {
		return fmt.Errorf("capacity name must be %q, derived from the selected Device UID, so one Device can back only one capacity", CapacityName(types.UID(annotations[DeviceUIDAnnotation])))
	}
	project, err := handler.Resolve(ctx, name)
	if err != nil {
		return errors.New("provider project lifetime is unavailable")
	}
	if !project.Identity.Valid() || project.Identity.RouteName != name {
		return errors.New("provider project lifetime is unavailable")
	}
	// Authorize before reading the Device so an unauthorized caller cannot
	// learn whether it exists.
	if err := handler.Authorize(ctx, project, request.UserInfo, metal.GroupVersion.Group, "machinecapacities", ""); err != nil {
		return err
	}
	if err := handler.Authorize(ctx, project, request.UserInfo, inventory.GroupVersion.Group, "devices", capacity.Spec.DeviceRef.Name); err != nil {
		return err
	}
	device := &inventory.Device{}
	if err := project.Client.Get(ctx, client.ObjectKey{Namespace: InventoryNamespace, Name: capacity.Spec.DeviceRef.Name}, device); err != nil {
		if apierrors.IsNotFound(err) {
			return errors.New("selected Device does not exist in the provider project")
		}
		return errors.New("selected Device is unavailable")
	}
	if device.UID != types.UID(annotations[DeviceUIDAnnotation]) || device.ResourceVersion != annotations[DeviceVersionAnnotation] || !device.DeletionTimestamp.IsZero() {
		return errors.New("selected Device incarnation or version changed; do not silently refresh the selection")
	}
	if lifecycle := inventory.LifecycleOf(device); lifecycle != inventory.DeviceActive {
		return fmt.Errorf("selected Device is %s; only an Active Device can be registered as capacity", lifecycle)
	}
	if !eligible(device) {
		return errors.New("registration requires non-placeholder serial, canonical system UUID, and complete provider aliases; incomplete identity is ambiguous")
	}
	return handler.scan(ctx, name, device)
}

// scan compares the selected Device with every live Device and capacity in the
// provider scope. It is a point-in-time read: see the stage-1 contract for the
// race with a concurrently created duplicate Device.
func (handler *Handler) scan(ctx context.Context, selectedProject string, selected *inventory.Device) error {
	selectedAliases := Aliases(selected)
	for _, name := range handler.Projects {
		project, err := handler.Resolve(ctx, name)
		if err != nil {
			return errors.New("provider identity scope is unavailable")
		}
		var devices inventory.DeviceList
		var capacities metal.MachineCapacityList
		if err := project.Client.List(ctx, &devices); err != nil {
			return errors.New("provider identity inventory is unavailable")
		}
		if err := project.Client.List(ctx, &capacities); err != nil {
			return errors.New("provider capacity inventory is unavailable")
		}
		for index := range devices.Items {
			device := &devices.Items[index]
			if name == selectedProject && device.UID == selected.UID {
				continue
			}
			if overlaps(Aliases(device), selectedAliases) {
				return fmt.Errorf("another Device (%s) shares recorded hardware identity; resolve the duplicate before registration", describe(name, selectedProject, device.Namespace, device.Name))
			}
		}
		for _, capacity := range capacities.Items {
			if name == selectedProject && capacity.Namespace == selected.Namespace && (capacity.Spec.DeviceRef.Name == selected.Name || capacity.Annotations[DeviceUIDAnnotation] == string(selected.UID)) {
				return fmt.Errorf("Device already backs capacity %s; one Device can back only one capacity", capacity.Name)
			}
			var matched *inventory.Device
			for index := range devices.Items {
				device := &devices.Items[index]
				if device.Name == capacity.Spec.DeviceRef.Name && device.Namespace == capacity.Namespace {
					matched = device
					break
				}
			}
			if matched == nil || inventory.CapacityBackingError(matched) != nil {
				return fmt.Errorf("capacity %s has unresolved physical identity; registration cannot establish non-conflict", describe(name, selectedProject, capacity.Namespace, capacity.Name))
			}
			if overlaps(Aliases(matched), selectedAliases) {
				return fmt.Errorf("capacity %s is backed by a Device sharing recorded hardware identity", describe(name, selectedProject, capacity.Namespace, capacity.Name))
			}
		}
	}
	return nil
}

// describe names a conflicting record for the caller. Records in other
// provider projects are not named, because the caller may not be able to read
// them.
func describe(project, selectedProject, namespace, name string) string {
	if project != selectedProject {
		return "in another provider project"
	}
	return namespace + "/" + name
}

func overlaps(left, right []string) bool {
	for _, first := range left {
		if slices.Contains(right, first) {
			return true
		}
	}
	return false
}
