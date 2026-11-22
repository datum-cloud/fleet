package registration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	DeviceUIDAnnotation     = "fleet.datumapis.com/registration-device-uid"
	DeviceVersionAnnotation = "fleet.datumapis.com/registration-device-version"
	OperationAnnotation     = "fleet.datumapis.com/registration-operation"
)

type Project struct {
	Identity controller.ProjectIdentity
	Client   client.Client
}

type Handler struct {
	Store       Store
	VerifyFence func(context.Context) error
	FenceDigest func(context.Context) (string, error)
	Resolve     func(context.Context, string) (Project, error)
	Authorize   func(context.Context, Project, authenticationv1.UserInfo, string, string, string) error
}

func aliases(device *inventory.Device) []string {
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
	if handler.Store.RequireActive {
		if err := handler.Ready(ctx); err != nil {
			return admission.Denied(err.Error())
		}
	}
	if request.Operation == admissionv1.Delete && request.Name == "" && ((request.Resource.Group == "" && request.Resource.Resource == "namespaces") || (request.Resource.Group == "resourcemanager.miloapis.com" && request.Resource.Resource == "projects")) {
		return admission.Denied("lifetime deletion requires an exact resource name; collection deletion cannot prove exclusion of retained lifetimes")
	}
	if request.Resource.Group == "" && request.Resource.Resource == "namespaces" && request.Name != "default" {
		return admission.Allowed("outside the retained inventory namespace")
	}
	if request.Resource.Group == "resourcemanager.miloapis.com" && request.Resource.Resource == "projects" && request.Operation == admissionv1.Delete {
		if slices.Contains(handler.Store.Projects, request.Name) {
			return admission.Denied("provider project lifetime is retained by the registration authority")
		}
		return admission.Allowed("outside the provider registration scope")
	}
	if request.DryRun != nil && *request.DryRun {
		return admission.Denied("registration admission retains reservations and does not support dry-run")
	}
	name, err := projectName(request.UserInfo)
	if err != nil {
		return admission.Denied(err.Error())
	}
	if !slices.Contains(handler.Store.Projects, name) {
		if (request.Resource.Group == "fleet.datumapis.com" && request.Resource.Resource == "devices") || (request.Resource.Group == "" && request.Resource.Resource == "namespaces") {
			return admission.Allowed("outside the configured provider identity scope")
		}
		return admission.Denied("project is outside the configured registration authority")
	}
	if request.UID == "" {
		return admission.Denied("admission operation identity is required")
	}
	for attempt := 0; attempt < 6; attempt++ {
		err = handler.evaluate(ctx, name, request)
		if !apierrors.IsConflict(err) {
			break
		}
	}
	if err != nil {
		if apierrors.IsConflict(err) {
			slog.ErrorContext(ctx, "registration ledger CAS retries exhausted", "attempts", 6, "project", name, "operation", request.UID)
		}
		return admission.Denied(err.Error())
	}
	return admission.Allowed("inert inventory registration only; no availability or preparation authority")
}

func (handler *Handler) evaluate(ctx context.Context, name string, request admission.Request) error {
	if handler.VerifyFence != nil {
		if err := handler.VerifyFence(ctx); err != nil {
			return err
		}
	}
	secret, ledger, err := handler.Store.Read(ctx)
	if err != nil {
		return fmt.Errorf("registration unavailable: %w", err)
	}
	if _, exists := secret.Data["routing.json"]; exists {
		route, err := decodeRouting(secret)
		if err != nil {
			return err
		}
		if route.Pending != nil {
			return errors.New("registration expansion is fenced")
		}
	}
	project, err := handler.Resolve(ctx, name)
	if err != nil {
		return err
	}
	if !project.Identity.Valid() || project.Identity.RouteName != name || project.Identity.AuthorityID != ledger.Authority {
		return errors.New("project lifetime unavailable")
	}
	if request.Resource.Group == "" && request.Resource.Resource == "namespaces" {
		if request.Name == "default" && request.Operation == admissionv1.Delete {
			return errors.New("the inventory namespace cannot be deleted within the registration authority")
		}
		return nil
	}
	if request.Resource.Group == "fleet.datumapis.com" && request.Resource.Resource == "devices" {
		return handler.device(ctx, project, request, secret, ledger)
	}
	if request.Resource.Group != "metal.datumapis.com" || request.Resource.Resource != "machinecapacities" {
		return errors.New("unsupported registration admission resource")
	}
	if request.Operation != admissionv1.Create || request.SubResource != "" {
		return errors.New("capacity mutation and deletion are disabled; retained and legacy capacity require operator reconciliation")
	}
	if request.Namespace != "default" {
		return errors.New("registration requires the default inventory namespace")
	}
	var capacity metal.MachineCapacity
	if err := json.Unmarshal(request.Object.Raw, &capacity); err != nil {
		return err
	}
	if capacity.UID == "" || capacity.Name == "" || capacity.Name != request.Name || capacity.Namespace != "default" || capacity.GenerateName != "" || !capacity.DeletionTimestamp.IsZero() {
		return errors.New("an explicit capacity name and API-assigned incarnation are required")
	}
	if capacity.Spec.AllocationPolicy != "Disabled" || capacity.Spec.ReclaimPolicy != "Retain" || capacity.Spec.CleaningPolicyRef.Name != "" || !reflect.DeepEqual(capacity.Status, metal.MachineCapacityStatus{}) || len(capacity.OwnerReferences) != 0 || len(capacity.Finalizers) != 0 {
		return errors.New("only Disabled/Retain registration with an explicitly unconfigured cleaning policy and no status or ownership is supported")
	}
	annotations := capacity.Annotations
	if len(annotations) != 3 || annotations[DeviceUIDAnnotation] == "" || annotations[DeviceVersionAnnotation] == "" || annotations[OperationAnnotation] == "" {
		return errors.New("exact Device UID/resourceVersion and a stable registration operation are required")
	}
	if len(annotations[OperationAnnotation]) > 128 {
		return errors.New("registration operation is too long")
	}
	if err := handler.Authorize(ctx, project, request.UserInfo, "metal.datumapis.com", "machinecapacities", ""); err != nil {
		return err
	}
	if err := handler.Authorize(ctx, project, request.UserInfo, "fleet.datumapis.com", "devices", capacity.Spec.DeviceRef.Name); err != nil {
		return err
	}
	namespace := &corev1.Namespace{}
	if err := project.Client.Get(ctx, client.ObjectKey{Name: "default"}, namespace); err != nil {
		return err
	}
	if namespace.UID == "" || !namespace.DeletionTimestamp.IsZero() {
		return errors.New("inventory namespace lifetime unavailable")
	}
	device := &inventory.Device{}
	if err := project.Client.Get(ctx, client.ObjectKey{Namespace: "default", Name: capacity.Spec.DeviceRef.Name}, device); err != nil {
		return errors.New("selected Device is unavailable")
	}
	if device.UID != types.UID(annotations[DeviceUIDAnnotation]) || device.ResourceVersion != annotations[DeviceVersionAnnotation] || !device.DeletionTimestamp.IsZero() {
		return errors.New("selected Device incarnation or version changed; do not silently refresh the selection")
	}
	if !eligible(device) {
		return errors.New("registration requires non-placeholder serial, canonical system UUID, and complete provider aliases; incomplete identity is ambiguous")
	}
	entry := Entry{Operation: request.UID, Actor: request.UserInfo, Project: name, ProjectUID: project.Identity.UID, NamespaceUID: namespace.UID, Name: capacity.Name, CapacityUID: capacity.UID, DeviceName: device.Name, DeviceUID: device.UID, DeviceVersion: device.ResourceVersion, Aliases: aliases(device), Input: append(json.RawMessage(nil), request.Object.Raw...), Outcome: "Unknown"}
	entry.AssetID = entry.Aliases[1]
	for _, retained := range ledger.Entries {
		if retained.Operation == entry.Operation && reflect.DeepEqual(retained, entry) {
			return nil
		}
		if overlaps(retained.Aliases, entry.Aliases) || (retained.Project == name && (retained.Name == entry.Name || retained.DeviceUID == entry.DeviceUID)) {
			return ErrConflict
		}
		var prior metal.MachineCapacity
		if json.Unmarshal(retained.Input, &prior) != nil {
			return errors.New("retained registration is corrupt")
		}
		if retained.Project == name && prior.Annotations[OperationAnnotation] == annotations[OperationAnnotation] {
			return ErrConflict
		}
	}
	for _, fence := range ledger.DeviceFences {
		if fence.Project == name && fence.UID == device.UID {
			if (fence.ProjectUID != "" && fence.ProjectUID != entry.ProjectUID) || (fence.NamespaceUID != "" && fence.NamespaceUID != entry.NamespaceUID) || (fence.Name != "" && (fence.Name != device.Name || fence.Namespace != device.Namespace)) {
				return ErrConflict
			}
			if fence.Deleted || (fence.Version != "" && fence.Version == device.ResourceVersion) {
				return ErrConflict
			}
			continue
		}
		if overlaps(fence.Aliases, entry.Aliases) {
			return ErrConflict
		}
	}
	if err := handler.scan(ctx, entry); err != nil {
		return err
	}
	current, err := handler.Resolve(ctx, name)
	if err != nil || current.Identity != project.Identity {
		return errors.New("project lifetime changed")
	}
	if err := project.Client.Get(ctx, client.ObjectKeyFromObject(namespace), namespace); err != nil || namespace.UID != entry.NamespaceUID || !namespace.DeletionTimestamp.IsZero() {
		return errors.New("namespace lifetime changed")
	}
	if err := project.Client.Get(ctx, client.ObjectKeyFromObject(device), device); err != nil || device.UID != entry.DeviceUID || device.ResourceVersion != entry.DeviceVersion || !device.DeletionTimestamp.IsZero() {
		return errors.New("selected Device changed")
	}
	if err := handler.Authorize(ctx, project, request.UserInfo, "metal.datumapis.com", "machinecapacities", ""); err != nil {
		return err
	}
	if err := handler.Authorize(ctx, project, request.UserInfo, "fleet.datumapis.com", "devices", device.Name); err != nil {
		return err
	}
	current, err = handler.Resolve(ctx, name)
	if err != nil || current.Identity != project.Identity {
		return errors.New("project lifetime changed during authorization")
	}
	if err := project.Client.Get(ctx, client.ObjectKeyFromObject(namespace), namespace); err != nil || namespace.UID != entry.NamespaceUID || !namespace.DeletionTimestamp.IsZero() {
		return errors.New("namespace lifetime changed during authorization")
	}
	if err := project.Client.Get(ctx, client.ObjectKeyFromObject(device), device); err != nil || device.UID != entry.DeviceUID || device.ResourceVersion != entry.DeviceVersion || !device.DeletionTimestamp.IsZero() {
		return errors.New("selected Device changed during authorization")
	}
	ledger.Entries = append(ledger.Entries, entry)
	return handler.Store.Write(ctx, secret, ledger)
}

func (handler *Handler) scan(ctx context.Context, entry Entry) error {
	for _, name := range handler.Store.Projects {
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
		for _, device := range devices.Items {
			if name == entry.Project && device.UID == entry.DeviceUID {
				continue
			}
			if overlaps(aliases(&device), entry.Aliases) {
				return errors.New("another Device shares recorded hardware identity; resolve aliases before registration")
			}
		}
		for _, capacity := range capacities.Items {
			if name == entry.Project && capacity.Namespace == "default" && capacity.Name == entry.Name {
				return ErrConflict
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
				return errors.New("legacy capacity has unresolved physical identity; registration cannot establish non-conflict")
			}
			if overlaps(aliases(matched), entry.Aliases) {
				return ErrConflict
			}
		}
	}
	return nil
}

func (handler *Handler) device(ctx context.Context, project Project, request admission.Request, secret *corev1.Secret, ledger Ledger) error {
	var device, old inventory.Device
	if request.Operation != admissionv1.Create {
		if err := json.Unmarshal(request.OldObject.Raw, &old); err != nil {
			return err
		}
	}
	if request.Operation != admissionv1.Delete {
		if err := json.Unmarshal(request.Object.Raw, &device); err != nil {
			return err
		}
	} else {
		device = old
	}
	for _, entry := range ledger.Entries {
		bound := entry.Project == project.Identity.RouteName && entry.DeviceUID == device.UID
		if entry.Project == project.Identity.RouteName && device.Namespace == "default" && entry.DeviceName == device.Name && !bound {
			return ErrConflict
		}
		if bound && (entry.ProjectUID != project.Identity.UID || request.Operation == admissionv1.Delete) {
			return ErrConflict
		}
		if overlaps(entry.Aliases, aliases(&device)) && !bound {
			return ErrConflict
		}
		if bound && !reflect.DeepEqual(device.Spec, old.Spec) {
			return errors.New("registered Device specification is frozen")
		}
	}
	if request.Operation == admissionv1.Update {
		if device.UID != old.UID || !reflect.DeepEqual(device.Spec.Identity, old.Spec.Identity) || !reflect.DeepEqual(aliases(&device), aliases(&old)) {
			return errors.New("Device incarnation and identity are immutable")
		}
		if old.ResourceVersion == "" {
			return errors.New("Device update baseline is unavailable")
		}
	}
	if request.Operation != admissionv1.Create && request.Operation != admissionv1.Delete && request.Operation != admissionv1.Update {
		return errors.New("unsupported Device operation")
	}
	if device.UID == "" {
		return errors.New("Device incarnation unavailable")
	}
	if request.Operation == admissionv1.Update && identicalObjects(request.OldObject.Raw, request.Object.Raw) {
		return nil
	}
	var namespace corev1.Namespace
	if err := project.Client.Get(ctx, client.ObjectKey{Name: device.Namespace}, &namespace); err != nil {
		return err
	}
	if namespace.UID == "" || !namespace.DeletionTimestamp.IsZero() {
		return errors.New("Device namespace lifetime unavailable")
	}
	fence := DeviceFence{Project: project.Identity.RouteName, ProjectUID: project.Identity.UID, Namespace: device.Namespace, NamespaceUID: namespace.UID, Name: device.Name, UID: device.UID, Deleted: request.Operation == admissionv1.Delete}
	if request.Operation == admissionv1.Update {
		current := &inventory.Device{}
		if err := project.Client.Get(ctx, client.ObjectKeyFromObject(&old), current); err != nil {
			return err
		}
		if current.UID != old.UID || current.ResourceVersion != old.ResourceVersion || device.ResourceVersion != old.ResourceVersion || !current.DeletionTimestamp.IsZero() {
			return errors.New("Device update baseline is no longer current")
		}
		fence.Version = old.ResourceVersion
	}
	if request.Operation == admissionv1.Create {
		fence.Aliases = aliases(&device)
		for _, retained := range ledger.DeviceFences {
			if overlaps(retained.Aliases, fence.Aliases) && !reflect.DeepEqual(retained, fence) {
				return ErrConflict
			}
		}
	}
	coalesced := make([]DeviceFence, 0, len(ledger.DeviceFences)+1)
	for _, retained := range ledger.DeviceFences {
		if retained.Project != fence.Project || retained.UID != fence.UID {
			coalesced = append(coalesced, retained)
			continue
		}
		if (retained.Deleted && !fence.Deleted) || (request.Operation == admissionv1.Create && !reflect.DeepEqual(retained, fence)) {
			return ErrConflict
		}
		if (retained.ProjectUID != "" && retained.ProjectUID != fence.ProjectUID) || (retained.NamespaceUID != "" && retained.NamespaceUID != fence.NamespaceUID) || (retained.Name != "" && (retained.Name != fence.Name || retained.Namespace != fence.Namespace)) {
			return ErrConflict
		}
		for _, alias := range retained.Aliases {
			if !slices.Contains(fence.Aliases, alias) {
				fence.Aliases = append(fence.Aliases, alias)
			}
		}
	}
	slices.Sort(fence.Aliases)
	coalesced = append(coalesced, fence)
	if reflect.DeepEqual(ledger.DeviceFences, coalesced) {
		return nil
	}
	ledger.DeviceFences = coalesced
	return handler.Store.Write(ctx, secret, ledger)
}

func identicalObjects(old, current []byte) bool {
	decode := func(raw []byte) (map[string]any, error) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var object map[string]any
		if err := decoder.Decode(&object); err != nil {
			return nil, err
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return nil, errors.New("expected exactly one JSON object")
		}
		return object, nil
	}
	before, beforeErr := decode(old)
	after, afterErr := decode(current)
	return beforeErr == nil && afterErr == nil && before != nil && after != nil && reflect.DeepEqual(before, after)
}
