// Package registration admits inert, inventory-backed MachineCapacity records.
//
// Only inventory-backed capacity is governed: capacity that carries a
// registration annotation, or a create under the derived device- name.
// Other capacity, such as synthetic or hand-made capacity, is allowed without
// reading Devices, and the packaged webhook does not route it at all.
//
// The webhook keeps no state of its own. Every decision is made against live
// Devices and capacities in the configured provider projects, so the API
// server remains the only store. One capacity per Device is enforced by the
// API server itself: the capacity name is derived from the Device UID, and two
// creates for the same Device collide on that name.
package registration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	metal "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/capacity"
	"go.datum.net/fleet/internal/controller"
	iam "go.miloapis.com/milo/pkg/apis/iam/v1alpha1"
	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	DeviceUIDAnnotation     = capacity.RegisteredDeviceUID
	DeviceVersionAnnotation = "fleet.datumapis.com/registration-device-version"
	OperationAnnotation     = "fleet.datumapis.com/registration-operation"

	// RegistrationAnnotationPrefix starts every annotation registration sets.
	// Any annotation with this prefix marks capacity as inventory-backed, so
	// adding, changing or removing one is always gated.
	RegistrationAnnotationPrefix = "fleet.datumapis.com/registration-"

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
	Projects []string
	Resolve  func(context.Context, string) (Project, error)
	// Authorize runs a SubjectAccessReview for the caller: verb, group,
	// resource and object name.
	Authorize func(ctx context.Context, project Project, actor authenticationv1.UserInfo, verb, group, resource, name string) error
	// Cleaning resolves a cleaning policy name to its digest. It returns
	// capacity.ErrUnresolved for a name that does not resolve. Nil resolves
	// nothing, so no registered capacity can be enabled.
	Cleaning capacity.CleaningResolver
	// Projectors are the usernames of the capacity projector, the only
	// identity that may write the status of registered capacity.
	Projectors []string
}

// PoolLabel is the label a MachineClass selects capacity by.
const PoolLabel = "fleet.datumapis.com/capacity-pool"

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
	backed, err := inventoryBacked(request)
	if err != nil {
		return admission.Denied("capacity could not be decoded: " + err.Error())
	}
	if !backed {
		// Defence in depth: the packaged webhook does not route this request.
		return admission.Allowed("capacity is not inventory-backed; registration admission does not govern it")
	}
	name, err := projectName(request.UserInfo)
	if err != nil {
		return admission.Denied(err.Error())
	}
	if !slices.Contains(handler.Projects, name) {
		return admission.Denied("project is outside the configured provider registration scope")
	}
	switch {
	case request.SubResource == "status" && request.Operation == admissionv1.Update:
		// Only the capacity projector writes the status of registered
		// capacity. The status subresource ignores metadata and spec.
		if !slices.Contains(handler.Projectors, request.UserInfo.Username) {
			return admission.Denied("only the capacity projector writes the status of registered capacity")
		}
		return admission.Allowed("the capacity projector writes derived status")
	case request.SubResource != "":
		return admission.Denied(mutationDisabled)
	case request.Operation == admissionv1.Update:
		if err := handler.evaluateUpdate(ctx, name, request); err != nil {
			return admission.Denied(err.Error())
		}
		return admission.Allowed("only the allocation policy, cleaning policy and pool changed, under the stage-2 rules")
	case request.Operation == admissionv1.Delete:
		if err := handler.evaluateDelete(ctx, name, request); err != nil {
			return admission.Denied(err.Error())
		}
		return admission.Allowed("the capacity is Disabled and its Device is gone or Retired")
	case request.Operation != admissionv1.Create:
		return admission.Denied(mutationDisabled)
	}
	if err := handler.evaluate(ctx, name, request); err != nil {
		return admission.Denied(err.Error())
	}
	return admission.Allowed("inert inventory registration only; no availability or preparation authority")
}

// inventoryBacked reports whether a request concerns inventory-backed
// capacity. It matches the packaged webhook's match condition: a create
// under the derived name, or a registration annotation on the new or the old
// object. Checking both objects means an update that adds the marker, and an
// update or delete that removes or changes it, are both governed.
func inventoryBacked(request admission.Request) (bool, error) {
	newObject, err := registrationMetadata(request.Object.Raw)
	if err != nil {
		return false, err
	}
	oldObject, err := registrationMetadata(request.OldObject.Raw)
	if err != nil {
		return false, err
	}
	if request.Operation == admissionv1.Create && (strings.HasPrefix(request.Name, CapacityNamePrefix) || (newObject != nil && strings.HasPrefix(newObject.Name, CapacityNamePrefix))) {
		return true, nil
	}
	return marked(newObject) || marked(oldObject), nil
}

func registrationMetadata(raw []byte) (*metav1.PartialObjectMetadata, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	object := &metav1.PartialObjectMetadata{}
	if err := json.Unmarshal(raw, object); err != nil {
		return nil, err
	}
	return object, nil
}

func marked(object *metav1.PartialObjectMetadata) bool {
	if object == nil {
		return false
	}
	for key := range object.Annotations {
		if strings.HasPrefix(key, RegistrationAnnotationPrefix) {
			return true
		}
	}
	return false
}

const mutationDisabled = "registered capacity changes only its allocation policy, cleaning policy and pool label; registration annotations cannot be added, changed or removed"

// evaluateUpdate admits an update of registered capacity that changes only
// the allocation policy, the cleaning policy reference and the pool label.
// While the capacity is Enabled, the only change is to Disabled. Enabling
// requires, at the time of the update, the caller's update permission, the
// annotated Device incarnation Active, no duplicate identity in the provider
// scope, a resolving cleaning policy and a pool label.
func (handler *Handler) evaluateUpdate(ctx context.Context, name string, request admission.Request) error {
	var next, previous metal.MachineCapacity
	if err := json.Unmarshal(request.Object.Raw, &next); err != nil {
		return err
	}
	if err := json.Unmarshal(request.OldObject.Raw, &previous); err != nil {
		return err
	}
	if !onlyStageTwoFields(previous, next) {
		return errors.New(mutationDisabled)
	}
	enabled := func(capacity metal.MachineCapacity) bool { return capacity.Spec.AllocationPolicy == "Enabled" }
	unchanged := previous.Spec.AllocationPolicy == next.Spec.AllocationPolicy && reflect.DeepEqual(previous.Spec.CleaningPolicyRef, next.Spec.CleaningPolicyRef) && previous.Labels[PoolLabel] == next.Labels[PoolLabel]
	switch {
	case unchanged:
		// An identical update, such as an unchanged apply, changes nothing.
		return nil
	case next.Spec.AllocationPolicy != "Enabled" && next.Spec.AllocationPolicy != "Disabled":
		return errors.New("the allocation policy is Enabled or Disabled")
	case enabled(previous) && (enabled(next) || !reflect.DeepEqual(previous.Spec.CleaningPolicyRef, next.Spec.CleaningPolicyRef) || previous.Labels[PoolLabel] != next.Labels[PoolLabel]):
		return errors.New("enabled capacity changes only to Disabled; disable it before changing its cleaning policy or pool")
	case !enabled(next):
		return nil
	}
	project, err := handler.Resolve(ctx, name)
	if err != nil || !project.Identity.Valid() || project.Identity.RouteName != name {
		return errors.New("provider project lifetime is unavailable")
	}
	if err := handler.Authorize(ctx, project, request.UserInfo, "update", metal.GroupVersion.Group, "machinecapacities", next.Name); err != nil {
		return err
	}
	if err := handler.Authorize(ctx, project, request.UserInfo, "get", inventory.GroupVersion.Group, "devices", next.Spec.DeviceRef.Name); err != nil {
		return err
	}
	device := &inventory.Device{}
	if err := project.Client.Get(ctx, client.ObjectKey{Namespace: request.Namespace, Name: next.Spec.DeviceRef.Name}, device); err != nil {
		if apierrors.IsNotFound(err) {
			return errors.New("capacity cannot leave Disabled: its Device does not exist")
		}
		return errors.New("capacity cannot leave Disabled: its Device is unavailable")
	}
	if string(device.UID) != next.Annotations[DeviceUIDAnnotation] || !device.DeletionTimestamp.IsZero() {
		return errors.New("capacity cannot leave Disabled: its Device is another incarnation than the one registered")
	}
	if lifecycle := inventory.LifecycleOf(device); lifecycle != inventory.DeviceActive {
		return fmt.Errorf("capacity cannot leave Disabled: its Device is %s; only an Active Device can back available capacity", lifecycle)
	}
	if !eligible(device) {
		return errors.New("capacity cannot leave Disabled: its Device no longer records a complete, non-placeholder identity")
	}
	if next.Labels[PoolLabel] == "" {
		return errors.New("capacity cannot leave Disabled without a " + PoolLabel + " label")
	}
	resolve := handler.Cleaning
	if resolve == nil {
		resolve = capacity.Unresolved
	}
	if next.Spec.CleaningPolicyRef.Name == "" {
		return errors.New("capacity cannot leave Disabled without a cleaning policy")
	}
	if _, err := resolve(ctx, next.Spec.CleaningPolicyRef.Name); errors.Is(err, capacity.ErrUnresolved) {
		return fmt.Errorf("capacity cannot leave Disabled: cleaning policy %q does not resolve", next.Spec.CleaningPolicyRef.Name)
	} else if err != nil {
		return errors.New("capacity cannot leave Disabled: the cleaning policy owner is unavailable")
	}
	return handler.duplicates(ctx, name, device)
}

// onlyStageTwoFields reports whether an update changes nothing but the
// allocation policy, the cleaning policy reference and the pool label.
func onlyStageTwoFields(previous, next metal.MachineCapacity) bool {
	strip := func(capacity metal.MachineCapacity) metal.MachineCapacity {
		capacity.Spec.AllocationPolicy, capacity.Spec.CleaningPolicyRef = "", metal.LocalReference{}
		capacity.Status = metal.MachineCapacityStatus{}
		labels := map[string]string{}
		for key, value := range capacity.Labels {
			if key != PoolLabel {
				labels[key] = value
			}
		}
		capacity.Labels = labels
		capacity.ResourceVersion, capacity.Generation, capacity.ManagedFields = "", 0, nil
		return capacity
	}
	return reflect.DeepEqual(strip(previous), strip(next))
}

// evaluateDelete admits the delete of registered capacity only when it is
// Disabled, its annotated Device is missing, another incarnation or
// Retired, no MachineAllocation that is not Released names it, and the
// caller may delete it. Enabling needs an Active Device of the annotated
// incarnation, so the capacity stays Disabled. An allocator that selected
// the capacity while it was Enabled re-reads it after creating its
// allocation and withdraws the allocation if the capacity is gone or
// Disabled, before it claims the Device.
func (handler *Handler) evaluateDelete(ctx context.Context, name string, request admission.Request) error {
	var registered metal.MachineCapacity
	if err := json.Unmarshal(request.OldObject.Raw, &registered); err != nil {
		return err
	}
	if registered.Spec.AllocationPolicy != "Disabled" {
		return errors.New("disable the capacity before deleting it")
	}
	project, err := handler.Resolve(ctx, name)
	if err != nil || !project.Identity.Valid() || project.Identity.RouteName != name {
		return errors.New("provider project lifetime is unavailable")
	}
	if err := handler.Authorize(ctx, project, request.UserInfo, "delete", metal.GroupVersion.Group, "machinecapacities", registered.Name); err != nil {
		return err
	}
	if err := handler.Authorize(ctx, project, request.UserInfo, "get", inventory.GroupVersion.Group, "devices", registered.Spec.DeviceRef.Name); err != nil {
		return err
	}
	device := &inventory.Device{}
	switch err := project.Client.Get(ctx, client.ObjectKey{Namespace: request.Namespace, Name: registered.Spec.DeviceRef.Name}, device); {
	case apierrors.IsNotFound(err):
	case err != nil:
		return errors.New("the capacity's Device is unavailable")
	case string(device.UID) == registered.Annotations[DeviceUIDAnnotation] && inventory.LifecycleOf(device) != inventory.DeviceRetired:
		return errors.New("registered capacity is removed only after its Device is deleted or Retired")
	}
	var allocations metal.MachineAllocationList
	if err := project.Client.List(ctx, &allocations, client.InNamespace(request.Namespace)); err != nil {
		return errors.New("the allocations that may name the capacity are unavailable")
	}
	for _, allocation := range allocations.Items {
		reference := allocation.Spec.MachineCapacityRef
		if reference.Name == registered.Name && (reference.Namespace == "" || reference.Namespace == registered.Namespace) && allocation.Status.Phase != metal.AllocationReleased {
			return fmt.Errorf("allocation %s still names the capacity", allocation.Name)
		}
	}
	return nil
}

// duplicates refuses a Device that shares an identity alias with any other
// Device in the provider scope, Retired ones included.
func (handler *Handler) duplicates(ctx context.Context, selectedProject string, selected *inventory.Device) error {
	selectedAliases := inventory.Aliases(selected)
	for _, name := range handler.Projects {
		project, err := handler.Resolve(ctx, name)
		if err != nil {
			return errors.New("provider identity scope is unavailable")
		}
		var devices inventory.DeviceList
		if err := project.Client.List(ctx, &devices); err != nil {
			return errors.New("provider identity inventory is unavailable")
		}
		for index := range devices.Items {
			device := &devices.Items[index]
			if name == selectedProject && device.UID == selected.UID {
				continue
			}
			if overlaps(inventory.Aliases(device), selectedAliases) {
				return fmt.Errorf("another Device (%s) shares recorded hardware identity; resolve the duplicate before enabling", describe(name, selectedProject, device.Namespace, device.Name))
			}
		}
	}
	return nil
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
	if err := handler.Authorize(ctx, project, request.UserInfo, "create", metal.GroupVersion.Group, "machinecapacities", ""); err != nil {
		return err
	}
	if err := handler.Authorize(ctx, project, request.UserInfo, "get", inventory.GroupVersion.Group, "devices", capacity.Spec.DeviceRef.Name); err != nil {
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
	selectedAliases := inventory.Aliases(selected)
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
			if overlaps(inventory.Aliases(device), selectedAliases) {
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
			if overlaps(inventory.Aliases(matched), selectedAliases) {
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
