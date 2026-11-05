package nso

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	networkingv1 "go.datum.net/network-services-operator/api/v1alpha"

	fleetnetwork "go.datum.net/fleet/internal/network"
)

const (
	claimUIDAnnotation  = "fleet.datumapis.com/claim-uid"
	bindingAnnotation   = "fleet.datumapis.com/binding-id"
	planAnnotation      = "fleet.datumapis.com/network-plan"
	interfaceAnnotation = "fleet.datumapis.com/interface-name"
	guardAnnotation     = "fleet.datumapis.com/object-guard"
)

type GuardRequest struct {
	Namespace  string
	Name       string
	ClaimUID   types.UID
	BindingID  string
	PlanDigest string
}

// ObjectGuard is durable, service-owned proof that Fleet reserved an NSO name.
// CreateOrRecover must serialize the guarded create/recovery and UID binding
// against Close; a pre-create check followed by an unguarded create is unsafe.
// Metadata on the NSO object is not proof by itself.
type ObjectGuard interface {
	Reserve(context.Context, GuardRequest) (string, error)
	CreateOrRecover(context.Context, GuardRequest, string, func() (types.UID, error)) (types.UID, error)
	Close(context.Context, GuardRequest, string) error
	Verify(context.Context, GuardRequest, string, types.UID) error
	Proof(context.Context, GuardRequest, string) (GuardProof, error)
}

type GuardProof struct{ CreatedUID types.UID }

type AuthorityVerifier interface {
	Verify(context.Context, fleetnetwork.InterfacePlan) error
}

type Adapter struct {
	client     client.Client
	authorizer fleetnetwork.UseAuthorizer
	guard      ObjectGuard
	verifier   AuthorityVerifier
	location   fleetnetwork.LocationAuthority
	now        func() time.Time
}

func New(kube client.Client, authorizer fleetnetwork.UseAuthorizer, guard ObjectGuard, verifier AuthorityVerifier, location fleetnetwork.LocationAuthority) (*Adapter, error) {
	if kube == nil || authorizer == nil || guard == nil || verifier == nil || !location.Valid() {
		return nil, fleetnetwork.ErrAuthorizationUnavailable
	}
	return &Adapter{client: kube, authorizer: authorizer, guard: guard, verifier: verifier, location: location, now: time.Now}, nil
}

func (a *Adapter) Preflight(ctx context.Context, request fleetnetwork.PreflightRequest) (fleetnetwork.Plan, error) {
	if request.ClaimUID == "" || request.NamespaceUID == "" || request.PrincipalUID == "" || request.ClaimNamespace == "" || request.Location != a.location || len(request.Interfaces) == 0 {
		return fleetnetwork.Plan{}, fleetnetwork.ErrAuthorizationUnavailable
	}
	canonical := append([]fleetnetwork.InterfaceRequest(nil), request.Interfaces...)
	sort.Slice(canonical, func(i, j int) bool { return canonical[i].Name < canonical[j].Name })
	digest, err := fleetnetwork.DigestRequests(canonical)
	if err != nil {
		return fleetnetwork.Plan{}, err
	}
	plan := fleetnetwork.Plan{RequestDigest: digest, Location: request.Location}
	seen := map[string]bool{}
	for _, requested := range canonical {
		if requested.Name == "" || seen[requested.Name] || requested.NetworkName == "" {
			return fleetnetwork.Plan{}, fleetnetwork.ErrObjectConflict
		}
		seen[requested.Name] = true
		namespace := requested.NetworkNS
		if namespace == "" {
			namespace = request.ClaimNamespace
		}
		if namespace != request.ClaimNamespace {
			return fleetnetwork.Plan{}, fleetnetwork.ErrAuthorizationDenied
		}
		reclaim, ok := fleetnetwork.NormalizeReclaimPolicy(requested.ReclaimPolicy)
		if !ok {
			return fleetnetwork.Plan{}, fleetnetwork.ErrObjectConflict
		}
		var network networkingv1.Network
		if err := a.client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: requested.NetworkName}, &network); err != nil {
			return fleetnetwork.Plan{}, err
		}
		if network.UID == "" || network.DeletionTimestamp != nil {
			return fleetnetwork.Plan{}, fleetnetwork.ErrEvidenceStale
		}
		ready := apiMeta.FindStatusCondition(network.Status.Conditions, networkingv1.NetworkReady)
		if ready == nil || ready.Status != metav1.ConditionTrue || ready.ObservedGeneration != network.Generation {
			return fleetnetwork.Plan{}, fleetnetwork.ErrEvidenceStale
		}
		for _, family := range requested.IPFamilies {
			if !containsFamily(network.Spec.IPFamilies, networkingv1.IPFamily(family)) {
				return fleetnetwork.Plan{}, fmt.Errorf("%w: network does not carry %s", fleetnetwork.ErrObjectConflict, family)
			}
		}
		decision, err := a.authorizer.AuthorizeNetworkUse(ctx, fleetnetwork.UseRequest{PrincipalUID: request.PrincipalUID, ClaimUID: request.ClaimUID, Namespace: namespace, NamespaceUID: request.NamespaceUID, NetworkName: network.Name, NetworkUID: network.UID, AddressClasses: append([]string(nil), requested.AddressClasses...), Location: request.Location})
		if err != nil {
			return fleetnetwork.Plan{}, err
		}
		if !decision.Valid() {
			return fleetnetwork.Plan{}, fleetnetwork.ErrAuthorizationUnavailable
		}
		classUIDs := map[string]types.UID{}
		for _, class := range requested.AddressClasses {
			if decision.AddressClassUIDs[class] == "" {
				return fleetnetwork.Plan{}, fleetnetwork.ErrAuthorizationUnavailable
			}
			classUIDs[class] = decision.AddressClassUIDs[class]
		}
		plan.Interfaces = append(plan.Interfaces, fleetnetwork.InterfacePlan{Name: requested.Name, NetworkNamespace: namespace, NetworkName: network.Name, NetworkUID: network.UID, NetworkGeneration: network.Generation, IPFamilies: append([]string(nil), requested.IPFamilies...), AddressClasses: append([]string(nil), requested.AddressClasses...), AddressClassUIDs: classUIDs, ReclaimPolicy: reclaim, ClaimName: claimName(request.ClaimUID, requested.Name), Decision: decision})
	}
	if !plan.Valid() {
		return fleetnetwork.Plan{}, fleetnetwork.ErrObjectConflict
	}
	return plan, nil
}

func (a *Adapter) Ensure(ctx context.Context, request fleetnetwork.EnsureRequest) (fleetnetwork.Observation, error) {
	if request.ClaimUID == "" || request.BindingID == "" || !request.Plan.Valid() || request.Plan.Location != a.location {
		return fleetnetwork.Observation{}, fleetnetwork.ErrObjectConflict
	}
	observation := fleetnetwork.Observation{ObservedAt: a.now()}
	previous, valid := observationsByName(request.Previous)
	if !valid {
		return observation, fleetnetwork.ErrObjectConflict
	}
	states, valid := lifecycleByName(request.Previous.Lifecycle)
	if !valid {
		return observation, fleetnetwork.ErrObjectConflict
	}
	if len(states) == 0 {
		for _, planned := range request.Plan.Interfaces {
			guardRequest := GuardRequest{Namespace: planned.NetworkNamespace, Name: planned.ClaimName, ClaimUID: request.ClaimUID, BindingID: request.BindingID, PlanDigest: request.Plan.RequestDigest}
			token, err := a.guard.Reserve(ctx, guardRequest)
			if err != nil || token == "" {
				return fleetnetwork.Observation{}, fleetnetwork.ErrObjectConflict
			}
			observation.Lifecycle = append(observation.Lifecycle, fleetnetwork.InterfaceLifecycleState{Name: planned.Name, GuardToken: token, CreateAdmitted: true})
		}
		return observation, fleetnetwork.ErrPending
	}
	if len(states) != len(request.Plan.Interfaces) {
		return observation, fleetnetwork.ErrObjectConflict
	}
	for _, planned := range request.Plan.Interfaces {
		observation.Lifecycle = append(observation.Lifecycle, states[planned.Name])
	}
	for i, planned := range request.Plan.Interfaces {
		state, ok := states[planned.Name]
		if !ok || !state.CreateAdmitted || state.GuardToken == "" || state.DeleteAdmitted {
			return observation, fleetnetwork.ErrObjectConflict
		}
		var network networkingv1.Network
		if err := a.client.Get(ctx, client.ObjectKey{Namespace: planned.NetworkNamespace, Name: planned.NetworkName}, &network); err != nil {
			return observation, err
		}
		if network.UID != planned.NetworkUID || network.DeletionTimestamp != nil {
			return observation, fleetnetwork.ErrEvidenceStale
		}
		if err := a.verifier.Verify(ctx, planned); err != nil {
			return observation, err
		}
		guardRequest := GuardRequest{Namespace: planned.NetworkNamespace, Name: planned.ClaimName, ClaimUID: request.ClaimUID, BindingID: request.BindingID, PlanDigest: request.Plan.RequestDigest}
		desired := desiredClaim(request, planned, state.GuardToken)
		var actual networkingv1.NetworkInterfaceClaim
		err := a.client.Get(ctx, client.ObjectKeyFromObject(desired), &actual)
		if apierrors.IsNotFound(err) {
			// A mutable Network must still match the retained preflight evidence
			// immediately before Fleet creates a claim. Once the exact guarded
			// claim exists, that claim is the stable allocation identity; later
			// Network spec generations must not strand the allocation.
			ready := apiMeta.FindStatusCondition(network.Status.Conditions, networkingv1.NetworkReady)
			if network.Generation != planned.NetworkGeneration || ready == nil || ready.Status != metav1.ConditionTrue || ready.ObservedGeneration != network.Generation {
				return observation, fleetnetwork.ErrEvidenceStale
			}
			createdUID, guardErr := a.guard.CreateOrRecover(ctx, guardRequest, state.GuardToken, func() (types.UID, error) {
				if createErr := a.client.Create(ctx, desired); createErr != nil {
					var recovered networkingv1.NetworkInterfaceClaim
					if getErr := a.client.Get(ctx, client.ObjectKeyFromObject(desired), &recovered); getErr != nil {
						return "", createErr
					}
					if !claimMatches(&recovered, desired) || recovered.UID == "" {
						return "", fleetnetwork.ErrObjectConflict
					}
					return recovered.UID, nil
				}
				return desired.UID, nil
			})
			if guardErr != nil || createdUID == "" {
				return observation, fleetnetwork.ErrObjectConflict
			}
			if getErr := a.client.Get(ctx, client.ObjectKeyFromObject(desired), &actual); getErr != nil {
				return observation, getErr
			}
			if actual.UID != createdUID {
				return observation, fleetnetwork.ErrObjectConflict
			}
		} else if err != nil {
			return observation, err
		} else {
			createdUID, guardErr := a.guard.CreateOrRecover(ctx, guardRequest, state.GuardToken, func() (types.UID, error) {
				if !claimMatches(&actual, desired) || actual.UID == "" {
					return "", fleetnetwork.ErrObjectConflict
				}
				return actual.UID, nil
			})
			if guardErr != nil || createdUID != actual.UID {
				return observation, fleetnetwork.ErrObjectConflict
			}
		}
		if !claimMatches(&actual, desired) || actual.UID == "" || a.guard.Verify(ctx, guardRequest, actual.Annotations[guardAnnotation], actual.UID) != nil {
			return observation, fleetnetwork.ErrObjectConflict
		}
		if state.CreatedUID != "" && state.CreatedUID != actual.UID {
			return observation, fleetnetwork.ErrObjectConflict
		}
		state.CreatedUID = actual.UID
		observation.Lifecycle[i] = state
		if prior, ok := previous[planned.Name]; ok && prior.ClaimUID != "" && actual.UID != prior.ClaimUID {
			return observation, fleetnetwork.ErrObjectConflict
		}
		observed := fleetnetwork.InterfaceObservation{Name: planned.Name, ClaimUID: actual.UID, ClaimGeneration: actual.Generation}
		for _, conditionType := range []string{networkingv1.NetworkInterfaceClaimAllocated, networkingv1.NetworkInterfaceClaimPrepared, networkingv1.NetworkInterfaceClaimProgrammed} {
			condition := apiMeta.FindStatusCondition(actual.Status.Conditions, conditionType)
			if condition != nil {
				observed.Conditions = append(observed.Conditions, fleetnetwork.Condition{Type: conditionType, Status: condition.Status, Reason: condition.Reason, ObservedGeneration: condition.ObservedGeneration})
			}
		}
		if actual.Status.NetworkInterfaceRef != nil {
			var iface networkingv1.NetworkInterface
			if err := a.client.Get(ctx, client.ObjectKey{Namespace: actual.Namespace, Name: actual.Status.NetworkInterfaceRef.Name}, &iface); err != nil {
				return observation, err
			}
			if iface.Spec.Network.Name != planned.NetworkName || iface.Spec.ClaimRef == nil || iface.Spec.ClaimRef.Name != actual.Name {
				return observation, fleetnetwork.ErrObjectConflict
			}
			if prior, ok := previous[planned.Name]; ok && prior.NetworkInterfaceUID != "" && iface.UID != prior.NetworkInterfaceUID {
				return observation, fleetnetwork.ErrObjectConflict
			}
			observed.NetworkInterfaceName, observed.NetworkInterfaceUID, observed.MTU = iface.Name, iface.UID, iface.Spec.MTU
		}
		for _, address := range actual.Status.Addresses {
			observed.Addresses = append(observed.Addresses, fleetnetwork.Address{Address: address.Address, Family: string(address.Family), Gateway: address.Gateway, Primary: address.Primary})
		}
		for _, address := range actual.Status.ExternalAddresses {
			observed.Addresses = append(observed.Addresses, fleetnetwork.Address{Address: address.Address, Family: string(address.Family), Class: address.Class})
		}
		observation.Interfaces = append(observation.Interfaces, observed)
	}
	revision, err := digest(observation.Interfaces)
	if err != nil {
		return observation, err
	}
	observation.Revision = revision
	if !observation.Programmed(request.Plan) {
		return observation, fleetnetwork.ErrPending
	}
	return observation, nil
}

func (a *Adapter) Release(ctx context.Context, request fleetnetwork.ReleaseRequest) (fleetnetwork.ReleaseObservation, error) {
	if request.ClaimUID == "" || request.BindingID == "" || !request.Plan.Valid() || request.Plan.Location != a.location {
		return fleetnetwork.ReleaseObservation{}, fleetnetwork.ErrObjectConflict
	}
	observed, valid := observationsByName(request.Observation)
	if !valid {
		return fleetnetwork.ReleaseObservation{}, fleetnetwork.ErrObjectConflict
	}
	states, valid := lifecycleByName(request.Lifecycle)
	if !valid {
		return fleetnetwork.ReleaseObservation{}, fleetnetwork.ErrObjectConflict
	}
	if len(states) == 0 {
		states, valid = lifecycleByName(request.Observation.Lifecycle)
		if !valid {
			return fleetnetwork.ReleaseObservation{}, fleetnetwork.ErrObjectConflict
		}
	}
	if len(states) != 0 && len(states) != len(request.Plan.Interfaces) {
		return fleetnetwork.ReleaseObservation{}, fleetnetwork.ErrObjectConflict
	}
	admission := fleetnetwork.ReleaseObservation{}
	needsAdmission := len(states) == 0
	for _, planned := range request.Plan.Interfaces {
		state, ok := states[planned.Name]
		if !ok {
			guardRequest := GuardRequest{Namespace: planned.NetworkNamespace, Name: planned.ClaimName, ClaimUID: request.ClaimUID, BindingID: request.BindingID, PlanDigest: request.Plan.RequestDigest}
			token, err := a.guard.Reserve(ctx, guardRequest)
			if err != nil || token == "" {
				return fleetnetwork.ReleaseObservation{}, fleetnetwork.ErrObjectConflict
			}
			state = fleetnetwork.InterfaceLifecycleState{Name: planned.Name, GuardToken: token, CreateAdmitted: true}
		}
		if !state.CreateAdmitted || state.GuardToken == "" {
			return fleetnetwork.ReleaseObservation{}, fleetnetwork.ErrObjectConflict
		}
		guardRequest := GuardRequest{Namespace: planned.NetworkNamespace, Name: planned.ClaimName, ClaimUID: request.ClaimUID, BindingID: request.BindingID, PlanDigest: request.Plan.RequestDigest}
		var existing networkingv1.NetworkInterfaceClaim
		if err := a.client.Get(ctx, client.ObjectKey{Namespace: planned.NetworkNamespace, Name: planned.ClaimName}, &existing); err == nil {
			desired := desiredClaim(fleetnetwork.EnsureRequest{ClaimUID: request.ClaimUID, BindingID: request.BindingID, Plan: request.Plan}, planned, state.GuardToken)
			if existing.UID == "" || !claimMatches(&existing, desired) || state.CreatedUID != "" && state.CreatedUID != existing.UID {
				return fleetnetwork.ReleaseObservation{}, fleetnetwork.ErrObjectConflict
			}
			if state.CreatedUID == "" {
				if a.guard.Verify(ctx, guardRequest, state.GuardToken, existing.UID) != nil {
					createdUID, guardErr := a.guard.CreateOrRecover(ctx, guardRequest, state.GuardToken, func() (types.UID, error) { return existing.UID, nil })
					if guardErr != nil || createdUID != existing.UID {
						return fleetnetwork.ReleaseObservation{}, fleetnetwork.ErrObjectConflict
					}
				}
			} else if a.guard.Verify(ctx, guardRequest, state.GuardToken, existing.UID) != nil {
				return fleetnetwork.ReleaseObservation{}, fleetnetwork.ErrObjectConflict
			}
			state.CreatedUID = existing.UID
		} else if !apierrors.IsNotFound(err) {
			return fleetnetwork.ReleaseObservation{}, err
		}
		if !state.DeleteAdmitted {
			state.DeleteAdmitted = true
			needsAdmission = true
		}
		if err := a.guard.Close(ctx, guardRequest, state.GuardToken); err != nil {
			return fleetnetwork.ReleaseObservation{}, err
		}
		admission.Lifecycle = append(admission.Lifecycle, state)
	}
	if needsAdmission {
		return admission, fleetnetwork.ErrPending
	}

	result := fleetnetwork.ReleaseObservation{}
	for _, planned := range request.Plan.Interfaces {
		result.Lifecycle = append(result.Lifecycle, states[planned.Name])
	}
	for i, planned := range request.Plan.Interfaces {
		state := result.Lifecycle[i]
		if state.DeleteCompleted {
			continue
		}
		identity := observed[planned.Name]
		var claim networkingv1.NetworkInterfaceClaim
		err := a.client.Get(ctx, client.ObjectKey{Namespace: planned.NetworkNamespace, Name: planned.ClaimName}, &claim)
		if err == nil {
			guardRequest := GuardRequest{Namespace: planned.NetworkNamespace, Name: planned.ClaimName, ClaimUID: request.ClaimUID, BindingID: request.BindingID, PlanDigest: request.Plan.RequestDigest}
			desired := desiredClaim(fleetnetwork.EnsureRequest{ClaimUID: request.ClaimUID, BindingID: request.BindingID, Plan: request.Plan}, planned, state.GuardToken)
			if claim.UID == "" || !claimMatches(&claim, desired) || a.guard.Verify(ctx, guardRequest, state.GuardToken, claim.UID) != nil || state.CreatedUID != "" && state.CreatedUID != claim.UID || identity.ClaimUID != "" && identity.ClaimUID != claim.UID {
				return result, fleetnetwork.ErrObjectConflict
			}
			state.CreatedUID = claim.UID
			if planned.ReclaimPolicy == "Retain" {
				if claim.Status.NetworkInterfaceRef != nil {
					var iface networkingv1.NetworkInterface
					if err := a.client.Get(ctx, client.ObjectKey{Namespace: claim.Namespace, Name: claim.Status.NetworkInterfaceRef.Name}, &iface); err != nil {
						result.Lifecycle[i] = state
						return result, err
					}
					if iface.UID == "" || iface.Spec.ClaimRef == nil || iface.Spec.ClaimRef.Name != claim.Name || iface.Spec.Network.Name != planned.NetworkName {
						return result, fleetnetwork.ErrObjectConflict
					}
					state.RetainedInterfaceName, state.RetainedInterfaceUID = iface.Name, iface.UID
				}
			}
			if claim.DeletionTimestamp == nil {
				if err := a.client.Delete(ctx, &claim); err != nil && !apierrors.IsNotFound(err) {
					result.Lifecycle[i] = state
					return result, err
				}
			}
		} else if !apierrors.IsNotFound(err) {
			result.Lifecycle[i] = state
			return result, err
		}
		if apierrors.IsNotFound(err) && planned.ReclaimPolicy == "Retain" {
			if state.RetainedInterfaceUID == "" && identity.NetworkInterfaceUID != "" {
				state.RetainedInterfaceName, state.RetainedInterfaceUID = identity.NetworkInterfaceName, identity.NetworkInterfaceUID
			}
			if state.RetainedInterfaceUID == "" && !state.NoBindingVerified {
				guardRequest := GuardRequest{Namespace: planned.NetworkNamespace, Name: planned.ClaimName, ClaimUID: request.ClaimUID, BindingID: request.BindingID, PlanDigest: request.Plan.RequestDigest}
				proof, proofErr := a.guard.Proof(ctx, guardRequest, state.GuardToken)
				if proofErr != nil {
					return result, proofErr
				}
				if proof.CreatedUID == "" && state.CreatedUID == "" {
					state.NoBindingVerified = true
				} else {
					if state.CreatedUID == "" {
						state.CreatedUID = proof.CreatedUID
					}
					result.Lifecycle[i] = state
					return result, fleetnetwork.ErrPending
				}
			}
			if state.RetainedInterfaceUID != "" {
				if state.RetainedInterfaceName == "" {
					return result, fleetnetwork.ErrObjectConflict
				}
				var iface networkingv1.NetworkInterface
				if err := a.client.Get(ctx, client.ObjectKey{Namespace: planned.NetworkNamespace, Name: state.RetainedInterfaceName}, &iface); err != nil {
					return result, err
				}
				if iface.UID != state.RetainedInterfaceUID || iface.Spec.ClaimRef != nil || iface.Status.Phase != networkingv1.NetworkInterfacePhaseAvailable {
					result.Lifecycle[i] = state
					return result, fleetnetwork.ErrPending
				}
			}
		}
		if apierrors.IsNotFound(err) {
			state.DeleteCompleted = true
		}
		result.Lifecycle[i] = state
	}
	for _, state := range result.Lifecycle {
		if !state.DeleteCompleted {
			return result, fleetnetwork.ErrPending
		}
	}
	if len(request.Observation.Interfaces) == 0 {
		result.Verified = true
		result.Revision, _ = digest(result.Lifecycle)
		return result, nil
	}
	revision, err := digest(request.Observation.Interfaces)
	if err != nil {
		return result, err
	}
	result.Verified, result.Revision = true, revision
	return result, nil
}

func desiredClaim(request fleetnetwork.EnsureRequest, planned fleetnetwork.InterfacePlan, guardToken string) *networkingv1.NetworkInterfaceClaim {
	claim := &networkingv1.NetworkInterfaceClaim{ObjectMeta: metav1.ObjectMeta{Name: planned.ClaimName, Namespace: planned.NetworkNamespace, Annotations: map[string]string{claimUIDAnnotation: string(request.ClaimUID), bindingAnnotation: request.BindingID, planAnnotation: request.Plan.RequestDigest, interfaceAnnotation: planned.Name, guardAnnotation: guardToken}}, Spec: networkingv1.NetworkInterfaceClaimSpec{Network: networkingv1.LocalNetworkRef{Name: planned.NetworkName}, ReclaimPolicy: networkingv1.NetworkInterfaceReclaimPolicy(planned.ReclaimPolicy)}}
	for _, family := range planned.IPFamilies {
		claim.Spec.IPFamilies = append(claim.Spec.IPFamilies, networkingv1.IPFamily(family))
	}
	for _, class := range planned.AddressClasses {
		claim.Spec.Addresses = append(claim.Spec.Addresses, networkingv1.NetworkInterfaceAddressRequest{Class: class})
	}
	return claim
}

func claimMatches(actual, desired *networkingv1.NetworkInterfaceClaim) bool {
	return actual.Annotations[claimUIDAnnotation] == desired.Annotations[claimUIDAnnotation] && actual.Annotations[bindingAnnotation] == desired.Annotations[bindingAnnotation] && actual.Annotations[planAnnotation] == desired.Annotations[planAnnotation] && actual.Annotations[interfaceAnnotation] == desired.Annotations[interfaceAnnotation] && actual.Annotations[guardAnnotation] == desired.Annotations[guardAnnotation] && normalizedSpec(actual.Spec) == normalizedSpec(desired.Spec)
}

func normalizedSpec(spec networkingv1.NetworkInterfaceClaimSpec) string {
	if len(spec.IPFamilies) == 0 {
		spec.IPFamilies = []networkingv1.IPFamily{networkingv1.IPv6Protocol}
	}
	if spec.ReclaimPolicy == "" {
		spec.ReclaimPolicy = networkingv1.NetworkInterfaceReclaimPolicyDelete
	}
	if spec.InterfaceName == "" {
		spec.InterfaceName = "eth0"
	}
	if spec.AttachmentMode == "" {
		spec.AttachmentMode = networkingv1.NetworkInterfaceAttachmentModeNetns
	}
	b, _ := json.Marshal(spec)
	return string(b)
}

func observationsByName(observation fleetnetwork.Observation) (map[string]fleetnetwork.InterfaceObservation, bool) {
	result := map[string]fleetnetwork.InterfaceObservation{}
	for _, iface := range observation.Interfaces {
		if iface.Name == "" || result[iface.Name].Name != "" {
			return nil, false
		}
		result[iface.Name] = iface
	}
	return result, true
}

func lifecycleByName(states []fleetnetwork.InterfaceLifecycleState) (map[string]fleetnetwork.InterfaceLifecycleState, bool) {
	result := map[string]fleetnetwork.InterfaceLifecycleState{}
	for _, state := range states {
		if state.Name == "" || result[state.Name].Name != "" {
			return nil, false
		}
		result[state.Name] = state
	}
	return result, true
}

func claimName(claimUID types.UID, interfaceName string) string {
	sum := sha256.Sum256([]byte(string(claimUID) + "/" + interfaceName))
	return "fleet-" + hex.EncodeToString(sum[:])[:40]
}

func digest(value any) (string, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func containsFamily(values []networkingv1.IPFamily, wanted networkingv1.IPFamily) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

var _ fleetnetwork.Lifecycle = (*Adapter)(nil)
