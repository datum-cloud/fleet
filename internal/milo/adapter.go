package milo

import (
	"context"
	"errors"
	"fmt"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	fleetnetwork "go.datum.net/fleet/internal/network"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
)

type Adapter struct {
	Records                *RecordStore
	Projects               ProjectResolver
	Sources                SourceAuthorizer
	Entitlements           EntitlementReader
	Fence                  AdmissionFence
	Custodian              QuotaCustodian
	Lifecycle              *controller.Coordinator
	afterLifecycleAllocate func()
	afterAbortedHoldClear  func()
	afterAuthorityCreate   func() error
	afterCustodyEnsure     func() error
}

type PrepareRequest struct {
	ConsumerRoute       string
	ProviderRoute       string
	NamespaceUID        types.UID
	Claim               *fleetv1.MachineClaim
	LocalClass          *fleetv1.MachineClass
	SourceClass         *fleetv1.MachineClass
	LocalProfile        *fleetv1.MachineProfile
	SourceProfile       *fleetv1.MachineProfile
	PublicationRevision string
	Location            fleetnetwork.LocationAuthority
}

type PreparedClaim struct {
	Authority        controller.AuthorityRef
	ConsumerProject  controller.ProjectIdentity
	ProviderProject  controller.ProjectIdentity
	NamespaceUID     types.UID
	PrincipalUID     string
	DecisionID       string
	LocalClassName   string
	LocalClassUID    types.UID
	SourceClassUID   types.UID
	LocalProfileName string
	LocalProfileUID  types.UID
	SourceProfileUID types.UID
	Location         fleetnetwork.LocationAuthority
}

func (a *Adapter) PrepareClaim(ctx context.Context, request PrepareRequest) (PreparedClaim, error) {
	if a == nil || a.Records == nil || a.Projects == nil || a.Lifecycle == nil || a.Sources == nil {
		return PreparedClaim{}, ErrAuthorityUnavailable
	}
	if request.Claim == nil || request.LocalClass == nil || request.SourceClass == nil || request.LocalProfile == nil || request.NamespaceUID == "" || request.Claim.UID == "" || request.ConsumerRoute == "" || request.ProviderRoute == "" || request.PublicationRevision == "" || !request.Location.Valid() || request.Claim.Spec.LocationRef.Name != request.Location.Name {
		return PreparedClaim{}, ErrAuthorityInvalid
	}
	if request.LocalClass.Name != request.Claim.Spec.MachineClassRef.Name || request.LocalProfile.Name != request.Claim.Spec.MachineProfileRef.Name {
		return PreparedClaim{}, ErrAuthorityInvalid
	}
	if request.LocalClass.Spec.Source == nil {
		if request.ConsumerRoute != request.ProviderRoute || request.LocalClass.UID != request.SourceClass.UID || request.LocalClass.Name != request.SourceClass.Name {
			return PreparedClaim{}, ErrAuthorityInvalid
		}
	} else if request.LocalClass.Spec.Source.Project != request.ProviderRoute || request.LocalClass.Spec.Source.Name != request.SourceClass.Name {
		return PreparedClaim{}, ErrAuthorityInvalid
	}
	consumer, _, err := a.Projects.Resolve(ctx, request.ConsumerRoute)
	if err != nil {
		return PreparedClaim{}, err
	}
	provider, _, err := a.Projects.Resolve(ctx, request.ProviderRoute)
	if err != nil {
		return PreparedClaim{}, err
	}
	if !consumer.Valid() || !provider.Valid() || consumer.RouteName != request.ConsumerRoute || provider.RouteName != request.ProviderRoute {
		return PreparedClaim{}, ErrProjectLifetimeMismatch
	}
	if err := a.Projects.Verify(ctx, consumer); err != nil {
		return PreparedClaim{}, ErrProjectLifetimeMismatch
	}
	if err := a.Projects.Verify(ctx, provider); err != nil {
		return PreparedClaim{}, ErrProjectLifetimeMismatch
	}
	requirements, err := requirementsDigest(request.LocalProfile)
	if err != nil {
		return PreparedClaim{}, err
	}
	classDigest, err := digest(request.SourceClass.Spec)
	if err != nil {
		return PreparedClaim{}, err
	}
	kind := ProfileCustom
	var sourceProfileUID types.UID
	if request.SourceProfile != nil {
		kind, sourceProfileUID = ProfilePublished, request.SourceProfile.UID
		sourceDigest, err := requirementsDigest(request.SourceProfile)
		if err != nil || sourceDigest != requirements {
			return PreparedClaim{}, ErrAuthorityInvalid
		}
	}
	source := SourceUseRequest{ConsumerProject: consumer, ProviderProject: provider, NamespaceUID: request.NamespaceUID, ClaimUID: request.Claim.UID, LocalClassUID: request.LocalClass.UID, SourceClassUID: request.SourceClass.UID, SourceClassRevision: request.SourceClass.Status.EvaluatedRevision, SourceClassDigest: classDigest, LocalProfileUID: request.LocalProfile.UID, SourceProfileUID: sourceProfileUID, ProfileKind: kind, RequirementsDigest: requirements, PublicationRevision: request.PublicationRevision, Location: request.Location}
	if err := source.Validate(); err != nil {
		return PreparedClaim{}, err
	}
	receipt, err := a.Sources.AuthorizeSource(ctx, source)
	if err != nil {
		return PreparedClaim{}, fmt.Errorf("%w: %v", ErrAuthorityUnavailable, err)
	}
	if err := validateReceipt(source, receipt); err != nil {
		return PreparedClaim{}, err
	}
	contract := struct {
		Request SourceUseRequest `json:"request"`
		Receipt SourceUseReceipt `json:"receipt"`
	}{source, receipt}
	contractDigest, err := digest(contract)
	if err != nil {
		return PreparedClaim{}, err
	}
	record := AuthorityRecord{SchemaVersion: authoritySchemaVersion, RecordID: shortDigest(contractDigest + "/" + string(request.Claim.UID)), State: StatePrepared, ClaimNamespace: request.Claim.Namespace, ClaimName: request.Claim.Name, ClaimUID: request.Claim.UID, SourceRequest: source, SourceReceipt: receipt, ContractDigest: contractDigest, AcceptedRequirements: request.LocalProfile.Spec}
	ref, err := a.Records.Create(ctx, record)
	if err != nil {
		return PreparedClaim{}, err
	}
	if a.afterAuthorityCreate != nil {
		if err := a.afterAuthorityCreate(); err != nil {
			return PreparedClaim{}, err
		}
	}
	admission := controller.AdmissionAuthority{PrincipalUID: receipt.PrincipalUID, DecisionID: receipt.DecisionID}
	if err := a.Lifecycle.EnsureClaimCustody(ctx, request.Claim, request.NamespaceUID, admission, consumer); err != nil {
		return PreparedClaim{}, err
	}
	if a.afterCustodyEnsure != nil {
		if err := a.afterCustodyEnsure(); err != nil {
			return PreparedClaim{}, err
		}
	}
	if err := a.Lifecycle.RetainClaimAuthority(ctx, request.Claim.UID, consumer, provider, ref); err != nil {
		return PreparedClaim{}, err
	}
	return PreparedClaim{Authority: ref, ConsumerProject: consumer, ProviderProject: provider, NamespaceUID: request.NamespaceUID, PrincipalUID: receipt.PrincipalUID, DecisionID: receipt.DecisionID, LocalClassName: request.LocalClass.Name, LocalClassUID: request.LocalClass.UID, SourceClassUID: request.SourceClass.UID, LocalProfileName: request.LocalProfile.Name, LocalProfileUID: request.LocalProfile.UID, SourceProfileUID: sourceProfileUID, Location: request.Location}, nil
}

// PrepareOrRecoverClaim returns the same durable prepared authority after a
// manager restart or finalizer event. Recovery validates current resolved
// inputs against the immutable authority record and never re-authorizes with a
// potentially different caller receipt.
func (a *Adapter) PrepareOrRecoverClaim(ctx context.Context, request PrepareRequest) (PreparedClaim, error) {
	if a == nil || a.Records == nil || a.Projects == nil || a.Lifecycle == nil || request.Claim == nil || request.Claim.UID == "" || request.ConsumerRoute == "" {
		return PreparedClaim{}, ErrAuthorityInvalid
	}
	consumer, _, err := a.Projects.Resolve(ctx, request.ConsumerRoute)
	if err != nil {
		return PreparedClaim{}, err
	}
	if !consumer.Valid() || consumer.RouteName != request.ConsumerRoute || a.Projects.Verify(ctx, consumer) != nil {
		return PreparedClaim{}, ErrProjectLifetimeMismatch
	}
	record, ref, err := a.Records.GetForClaim(ctx, consumer, request.Claim.UID)
	if apierrors.IsNotFound(err) {
		hasCustody, custodyErr := a.Lifecycle.HasClaimCustody(ctx, consumer, request.Claim.UID)
		if custodyErr != nil {
			return PreparedClaim{}, custodyErr
		}
		if hasCustody {
			return PreparedClaim{}, controller.ErrRecordInvalid
		}
		return a.PrepareClaim(ctx, request)
	}
	if err != nil {
		return PreparedClaim{}, err
	}
	provider, _, err := a.Projects.Resolve(ctx, request.ProviderRoute)
	if err != nil || !provider.Valid() || provider.RouteName != request.ProviderRoute || a.Projects.Verify(ctx, provider) != nil {
		return PreparedClaim{}, ErrProjectLifetimeMismatch
	}
	if err := validateRecoveredPrepare(record, request, consumer, provider); err != nil {
		return PreparedClaim{}, err
	}
	admission := controller.AdmissionAuthority{PrincipalUID: record.SourceReceipt.PrincipalUID, DecisionID: record.SourceReceipt.DecisionID}
	if err := a.Lifecycle.EnsureClaimCustody(ctx, request.Claim, request.NamespaceUID, admission, consumer); err != nil {
		return PreparedClaim{}, err
	}
	if err := a.Lifecycle.RetainClaimAuthority(ctx, request.Claim.UID, consumer, provider, ref); err != nil {
		return PreparedClaim{}, err
	}
	return PreparedClaim{
		Authority: ref, ConsumerProject: consumer, ProviderProject: provider,
		NamespaceUID: request.NamespaceUID, PrincipalUID: record.SourceReceipt.PrincipalUID, DecisionID: record.SourceReceipt.DecisionID,
		LocalClassName: request.LocalClass.Name, LocalClassUID: request.LocalClass.UID, SourceClassUID: request.SourceClass.UID,
		LocalProfileName: request.LocalProfile.Name, LocalProfileUID: request.LocalProfile.UID, SourceProfileUID: record.SourceRequest.SourceProfileUID,
		Location: request.Location,
	}, nil
}

func validateRecoveredPrepare(record *AuthorityRecord, request PrepareRequest, consumer, provider controller.ProjectIdentity) error {
	if record == nil || request.Claim == nil || request.LocalClass == nil || request.SourceClass == nil || request.LocalProfile == nil || request.NamespaceUID == "" || request.PublicationRevision == "" || !request.Location.Valid() {
		return ErrAuthorityInvalid
	}
	if record.ClaimNamespace != request.Claim.Namespace || record.ClaimName != request.Claim.Name || record.ClaimUID != request.Claim.UID || request.LocalClass.Name != request.Claim.Spec.MachineClassRef.Name || request.LocalProfile.Name != request.Claim.Spec.MachineProfileRef.Name {
		return ErrAuthorityInvalid
	}
	if request.LocalClass.Spec.Source == nil {
		if consumer != provider || request.LocalClass.UID != request.SourceClass.UID || request.LocalClass.Name != request.SourceClass.Name {
			return ErrAuthorityInvalid
		}
	} else if request.LocalClass.Spec.Source.Project != request.ProviderRoute || request.LocalClass.Spec.Source.Name != request.SourceClass.Name {
		return ErrAuthorityInvalid
	}
	requirements, err := requirementsDigest(request.LocalProfile)
	if err != nil {
		return err
	}
	classDigest, err := digest(request.SourceClass.Spec)
	if err != nil {
		return err
	}
	kind := ProfileCustom
	var sourceProfileUID types.UID
	if request.SourceProfile != nil {
		kind, sourceProfileUID = ProfilePublished, request.SourceProfile.UID
		sourceRequirements, sourceErr := requirementsDigest(request.SourceProfile)
		if sourceErr != nil || sourceRequirements != requirements {
			return ErrAuthorityInvalid
		}
	}
	want := SourceUseRequest{ConsumerProject: consumer, ProviderProject: provider, NamespaceUID: request.NamespaceUID, ClaimUID: request.Claim.UID, LocalClassUID: request.LocalClass.UID, SourceClassUID: request.SourceClass.UID, SourceClassRevision: request.SourceClass.Status.EvaluatedRevision, SourceClassDigest: classDigest, LocalProfileUID: request.LocalProfile.UID, SourceProfileUID: sourceProfileUID, ProfileKind: kind, RequirementsDigest: requirements, PublicationRevision: request.PublicationRevision, Location: request.Location}
	if want.Validate() != nil || record.SourceRequest != want {
		return ErrAuthorityInvalid
	}
	return nil
}

// ReleaseClaim recovers release authority using only the exact consumer project
// and claim lifetime. Public claim/class/profile objects may already be gone.
func (a *Adapter) ReleaseClaim(ctx context.Context, consumer controller.ProjectIdentity, claimUID types.UID) (controller.ReleaseResult, error) {
	if a == nil || a.Records == nil || a.Lifecycle == nil || !consumer.Valid() || claimUID == "" {
		return controller.ReleaseResult{}, ErrAuthorityInvalid
	}
	record, ref, err := a.Records.GetForClaim(ctx, consumer, claimUID)
	if apierrors.IsNotFound(err) {
		hasCustody, custodyErr := a.Lifecycle.HasClaimCustody(ctx, consumer, claimUID)
		if custodyErr != nil {
			return controller.ReleaseResult{}, custodyErr
		}
		if hasCustody {
			return controller.ReleaseResult{}, controller.ErrRecordInvalid
		}
		return controller.ReleaseResult{}, ErrClaimAuthorityNotFound
	}
	if err != nil {
		return controller.ReleaseResult{}, err
	}
	return a.Release(ctx, PreparedClaim{Authority: ref, ConsumerProject: record.SourceRequest.ConsumerProject}, claimUID)
}

func (a *Adapter) Allocate(ctx context.Context, prepared PreparedClaim, request controller.AllocationRequest) (*controller.AssetRecord, error) {
	if a == nil || a.Records == nil || a.Projects == nil || a.Lifecycle == nil {
		return nil, ErrAuthorityInvalid
	}
	if a.Fence == nil {
		return nil, ErrAdmissionFenceUnavailable
	}
	if a.Custodian == nil {
		return nil, ErrQuotaCustodyUnavailable
	}
	if a.Entitlements == nil {
		return nil, ErrQuotaUnknown
	}
	record, err := a.Records.Get(ctx, prepared.Authority)
	if err != nil {
		return nil, err
	}
	if err := validatePreparedAllocation(record, prepared, request); err != nil {
		return nil, ErrAuthorityInvalid
	}
	if request.Claim.DeletionTimestamp != nil || !containsFinalizer(request.Claim.Finalizers, controller.MachineClaimFinalizer) {
		return nil, ErrAuthorityInvalid
	}
	switch record.State {
	case StateReleased, StateCleanupVerified, StateQuarantined:
		return nil, ErrAuthorityInvalid
	case StateQuotaReleasePending:
		if record.QuotaHold == nil || record.AssetID != "" {
			return nil, ErrAuthorityInvalid
		}
		if record.Settlement == nil {
			receipt, settleErr := a.Custodian.Settle(ctx, record.SourceRequest.ConsumerProject, prepared.Authority, *record.QuotaHold)
			if settleErr != nil || !validSettlement(prepared.Authority, *record.QuotaHold, receipt) {
				return nil, ErrQuotaReleaseUnknown
			}
			if _, err := a.Records.Update(ctx, prepared.Authority, func(current *AuthorityRecord) error {
				if current.State != StateQuotaReleasePending || current.Settlement != nil || current.QuotaHold == nil || *current.QuotaHold != *record.QuotaHold || current.AssetID != "" {
					return ErrAuthorityInvalid
				}
				current.Settlement = &receipt
				return nil
			}); err != nil {
				return nil, err
			}
			record.Settlement = &receipt
		}
		if err := a.finishAbortedSettlement(ctx, prepared.Authority, *record.QuotaHold, *record.Settlement); err != nil {
			return nil, err
		}
		return nil, ErrQuotaPending
	case StatePrepared, StateQuotaIntent, StateQuotaGranted, StateBound:
	default:
		return nil, ErrAuthorityInvalid
	}
	consumer, consumerClient, err := a.Projects.Resolve(ctx, prepared.ConsumerProject.RouteName)
	if err != nil || consumer != prepared.ConsumerProject || a.Projects.Verify(ctx, consumer) != nil {
		return nil, ErrProjectLifetimeMismatch
	}
	provider, _, err := a.Projects.Resolve(ctx, prepared.ProviderProject.RouteName)
	if err != nil || provider != prepared.ProviderProject || a.Projects.Verify(ctx, provider) != nil {
		return nil, ErrProjectLifetimeMismatch
	}
	request.NamespaceUID = prepared.NamespaceUID
	request.Admission = controller.AdmissionAuthority{PrincipalUID: prepared.PrincipalUID, DecisionID: prepared.DecisionID}
	request.LocalClassName, request.LocalClassUID = prepared.LocalClassName, prepared.LocalClassUID
	request.LocalProfileName, request.LocalProfileUID = prepared.LocalProfileName, prepared.LocalProfileUID
	request.ConsumerProject, request.ProviderProject, request.Authority = consumer, provider, prepared.Authority
	request.Location = prepared.Location
	if len(request.Claim.Spec.NetworkInterfaces) != 0 {
		return nil, fmt.Errorf("%w: Milo adapter does not yet compose network admission into its fence", controller.ErrUnsupported)
	}
	if record.QuotaHold != nil && record.FenceReceipt != nil && (record.State == StateQuotaGranted || record.State == StateBound) {
		request.ExternalHold = record.QuotaHold.Ref()
		asset, allocateErr := a.Lifecycle.Allocate(ctx, request)
		if allocateErr != nil {
			return nil, allocateErr
		}
		if _, err := a.Records.Update(ctx, prepared.Authority, func(current *AuthorityRecord) error {
			if current.State != StateQuotaGranted && current.State != StateBound {
				return ErrAuthorityInvalid
			}
			if current.Settlement != nil {
				return ErrAuthorityInvalid
			}
			current.State, current.AssetID, current.BindingID = StateBound, asset.AssetID, asset.BindingID
			return nil
		}); err != nil {
			return nil, err
		}
		return asset, nil
	}
	if err := a.Lifecycle.CheckCapacity(request); err != nil {
		return nil, err
	}
	entitlement, err := a.Entitlements.Current(ctx, consumerClient)
	if err != nil {
		return nil, err
	}
	intent := QuotaIntentFor(consumer, request.Claim.Namespace, request.Claim.Name, request.Claim.UID)
	if record.QuotaIntent == nil {
		record, err = a.Records.Update(ctx, prepared.Authority, func(current *AuthorityRecord) error {
			if current.State != StatePrepared {
				return ErrAuthorityInvalid
			}
			current.State, current.QuotaIntent, current.Entitlement, current.Settlement = StateQuotaIntent, &intent, &entitlement, nil
			return nil
		})
		if err != nil {
			return nil, err
		}
	} else if *record.QuotaIntent != intent {
		return nil, ErrAuthorityInvalid
	}
	hold, granted, acquireErr := a.Custodian.Acquire(ctx, consumer, prepared.Authority, intent)
	if hold.UID != "" {
		record, err = a.Records.Update(ctx, prepared.Authority, func(current *AuthorityRecord) error {
			if current.State != StateQuotaIntent && current.State != StateQuotaGranted {
				return ErrAuthorityInvalid
			}
			if current.QuotaIntent == nil || *current.QuotaIntent != intent {
				return ErrAuthorityInvalid
			}
			if current.QuotaHold != nil && *current.QuotaHold != hold {
				return ErrAuthorityInvalid
			}
			current.QuotaHold, current.Entitlement = &hold, &entitlement
			if granted {
				current.State = StateQuotaGranted
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if acquireErr != nil {
		return nil, acquireErr
	}
	if !granted || hold.UID == "" {
		return nil, ErrQuotaPending
	}
	request.ExternalHold = hold.Ref()
	if err := a.Lifecycle.RetainAllocationAuthority(ctx, request.Claim.UID, consumer, provider, prepared.Authority, hold.Ref()); err != nil {
		return nil, err
	}
	commit := AdmissionCommit{Authority: prepared.Authority, SourceUse: record.SourceRequest, Source: record.SourceReceipt, Entitlement: entitlement, Hold: hold}
	commitDigest, err := digest(commit)
	if err != nil {
		return nil, err
	}
	fenceReceipt, err := a.Fence.Commit(ctx, commit)
	if err != nil {
		return nil, a.settleAfterFailedAdmission(ctx, consumer, prepared.Authority, hold, err)
	}
	if fenceReceipt.ContractDigest != prepared.Authority.ContractDigest || fenceReceipt.CommitDigest != commitDigest || fenceReceipt.FenceToken != record.SourceReceipt.FenceToken || fenceReceipt.CommitID == "" {
		return nil, a.settleAfterFailedAdmission(ctx, consumer, prepared.Authority, hold, ErrAuthorityInvalid)
	}
	if _, err := a.Records.Update(ctx, prepared.Authority, func(current *AuthorityRecord) error {
		if current.State != StateQuotaGranted || current.Settlement != nil {
			return ErrAuthorityInvalid
		}
		current.FenceReceipt = &fenceReceipt
		return nil
	}); err != nil {
		return nil, err
	}
	asset, err := a.Lifecycle.Allocate(ctx, request)
	if err != nil {
		if errors.Is(err, controller.ErrInsufficientCapacity) {
			return nil, a.settleAfterFailedAdmission(ctx, consumer, prepared.Authority, hold, err)
		}
		return nil, err
	}
	if a.afterLifecycleAllocate != nil {
		a.afterLifecycleAllocate()
	}
	if _, err := a.Records.Update(ctx, prepared.Authority, func(current *AuthorityRecord) error {
		if current.State != StateQuotaGranted || current.Settlement != nil || current.FenceReceipt == nil {
			return ErrAuthorityInvalid
		}
		current.State, current.AssetID, current.BindingID = StateBound, asset.AssetID, asset.BindingID
		return nil
	}); err != nil {
		return nil, err
	}
	return asset, nil
}

func containsFinalizer(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func (a *Adapter) settleAfterFailedAdmission(ctx context.Context, consumer controller.ProjectIdentity, authority controller.AuthorityRef, hold QuotaHold, cause error) error {
	if _, err := a.Records.Update(ctx, authority, func(current *AuthorityRecord) error {
		if current.State != StateQuotaGranted {
			return ErrAuthorityInvalid
		}
		current.State = StateQuotaReleasePending
		return nil
	}); err != nil {
		return err
	}
	receipt, err := a.Custodian.Settle(ctx, consumer, authority, hold)
	if err != nil {
		return errors.Join(cause, ErrQuotaReleaseUnknown, err)
	}
	if !validSettlement(authority, hold, receipt) {
		return errors.Join(cause, ErrQuotaReleaseUnknown)
	}
	if _, err := a.Records.Update(ctx, authority, func(current *AuthorityRecord) error {
		if current.State != StateQuotaReleasePending || current.QuotaHold == nil || *current.QuotaHold != hold || current.Settlement != nil || current.AssetID != "" {
			return ErrAuthorityInvalid
		}
		current.Settlement = &receipt
		return nil
	}); err != nil {
		return err
	}
	if err := a.finishAbortedSettlement(ctx, authority, hold, receipt); err != nil {
		return err
	}
	return cause
}

func (a *Adapter) finishAbortedSettlement(ctx context.Context, authority controller.AuthorityRef, hold QuotaHold, receipt SettlementReceipt) error {
	if !validSettlement(authority, hold, receipt) {
		return ErrQuotaReleaseUnknown
	}
	if err := a.Lifecycle.ClearSettledExternalHold(ctx, hold.Intent.ClaimUID, authority, hold.Ref(), hold.Intent.ConsumerProject); err != nil {
		return err
	}
	if a.afterAbortedHoldClear != nil {
		a.afterAbortedHoldClear()
	}
	_, err := a.Records.Update(ctx, authority, func(current *AuthorityRecord) error {
		if current.State != StateQuotaReleasePending || current.QuotaHold == nil || *current.QuotaHold != hold || current.Settlement == nil || *current.Settlement != receipt || current.AssetID != "" {
			return ErrAuthorityInvalid
		}
		current.State, current.LastSettledHold, current.LastSettlement = StatePrepared, &hold, &receipt
		current.Entitlement, current.QuotaIntent, current.QuotaHold, current.FenceReceipt = nil, nil, nil, nil
		current.Settlement = nil
		return nil
	})
	return err
}

func validatePreparedAllocation(record *AuthorityRecord, prepared PreparedClaim, request controller.AllocationRequest) error {
	if record == nil || request.Claim == nil || request.Class == nil || request.Profile == nil || prepared.Authority.ContractDigest != record.ContractDigest || record.SourceRequest.ConsumerProject != prepared.ConsumerProject || record.SourceRequest.ProviderProject != prepared.ProviderProject || record.SourceRequest.NamespaceUID != prepared.NamespaceUID || record.SourceReceipt.PrincipalUID != prepared.PrincipalUID || record.SourceReceipt.DecisionID != prepared.DecisionID || request.Claim.Namespace != record.ClaimNamespace || request.Claim.Name != record.ClaimName || request.Claim.UID != record.ClaimUID || prepared.LocalClassUID != record.SourceRequest.LocalClassUID || prepared.SourceClassUID != record.SourceRequest.SourceClassUID || prepared.LocalProfileUID != record.SourceRequest.LocalProfileUID || prepared.SourceProfileUID != record.SourceRequest.SourceProfileUID || prepared.Location != record.SourceRequest.Location || request.Location != record.SourceRequest.Location || request.Claim.Spec.LocationRef.Name != record.SourceRequest.Location.Name || request.Class.UID != record.SourceRequest.SourceClassUID || request.Class.Status.EvaluatedRevision != record.SourceRequest.SourceClassRevision {
		return ErrAuthorityInvalid
	}
	wantProfileUID := record.SourceRequest.LocalProfileUID
	if record.SourceRequest.ProfileKind == ProfilePublished {
		wantProfileUID = record.SourceRequest.SourceProfileUID
	}
	requirements, err := requirementsDigest(request.Profile)
	classDigest, classErr := digest(request.Class.Spec)
	acceptedDigest, acceptedErr := digest(record.AcceptedRequirements)
	if err != nil || classErr != nil || acceptedErr != nil || request.Profile.UID != wantProfileUID || requirements != record.SourceRequest.RequirementsDigest || acceptedDigest != record.SourceRequest.RequirementsDigest || classDigest != record.SourceRequest.SourceClassDigest {
		return ErrAuthorityInvalid
	}
	return nil
}

func (a *Adapter) Release(ctx context.Context, prepared PreparedClaim, claimUID types.UID) (controller.ReleaseResult, error) {
	if a == nil || a.Custodian == nil {
		return controller.ReleaseResult{}, ErrQuotaCustodyUnavailable
	}
	record, ref, err := a.Records.GetForClaim(ctx, prepared.ConsumerProject, claimUID)
	if err != nil {
		return controller.ReleaseResult{}, err
	}
	if ref != prepared.Authority || record.SourceRequest.ConsumerProject != prepared.ConsumerProject {
		return controller.ReleaseResult{}, ErrAuthorityInvalid
	}
	if record.State == StateQuarantined {
		return controller.ReleaseResult{}, ErrAuthorityInvalid
	}
	consumer := record.SourceRequest.ConsumerProject
	if record.State == StateQuotaIntent && record.QuotaHold == nil {
		resolution, recoverErr := a.Custodian.Recover(ctx, consumer, ref, *record.QuotaIntent)
		if recoverErr != nil {
			return controller.ReleaseResult{State: controller.AssetCleanupVerified, ExternalSettlementRequired: true}, errors.Join(ErrQuotaUnknown, recoverErr)
		}
		if resolution.Hold == nil {
			intentDigest, _ := digest(*record.QuotaIntent)
			if resolution.NoHold == nil || resolution.NoHold.AuthorityRecordID != ref.RecordID || resolution.NoHold.IntentDigest != intentDigest || resolution.NoHold.AcknowledgmentID == "" {
				return controller.ReleaseResult{State: controller.AssetCleanupVerified, ExternalSettlementRequired: true}, ErrQuotaUnknown
			}
			if _, err := a.Records.Update(ctx, ref, func(current *AuthorityRecord) error {
				if current.State != StateQuotaIntent || current.QuotaHold != nil {
					return ErrAuthorityInvalid
				}
				current.State, current.NoHold, current.QuotaIntent, current.Entitlement = StatePrepared, resolution.NoHold, nil, nil
				return nil
			}); err != nil {
				return controller.ReleaseResult{}, err
			}
			record, _, err = a.Records.GetForClaim(ctx, prepared.ConsumerProject, claimUID)
			if err != nil {
				return controller.ReleaseResult{}, err
			}
		} else {
			hold := *resolution.Hold
			if hold.Intent != *record.QuotaIntent || !hold.Ref().Valid() {
				return controller.ReleaseResult{}, ErrAuthorityInvalid
			}
			if _, err := a.Records.Update(ctx, ref, func(current *AuthorityRecord) error {
				if current.State != StateQuotaIntent || current.QuotaHold != nil {
					return ErrAuthorityInvalid
				}
				current.State, current.QuotaHold = StateQuotaGranted, &hold
				return nil
			}); err != nil {
				return controller.ReleaseResult{}, err
			}
			record, _, err = a.Records.GetForClaim(ctx, prepared.ConsumerProject, claimUID)
			if err != nil {
				return controller.ReleaseResult{}, err
			}
		}
	}
	// A hold may have been durably retained immediately before the allocating
	// process crashed. Attach its exact lifetime to core custody before asking
	// core whether release is finalizer-safe, independent of grant phase.
	if record.QuotaHold != nil {
		if err := a.Lifecycle.RetainAllocationAuthority(ctx, claimUID, record.SourceRequest.ConsumerProject, record.SourceRequest.ProviderProject, ref, record.QuotaHold.Ref()); err != nil {
			return controller.ReleaseResult{}, err
		}
	}
	result, err := a.Lifecycle.ReleaseScoped(ctx, consumer, claimUID)
	if err != nil || !result.ExternalSettlementRequired {
		if err == nil && result.SafeToRemoveFinalizer && record.State == StateQuotaReleasePending && record.Settlement != nil {
			_, updateErr := a.Records.Update(ctx, ref, func(current *AuthorityRecord) error {
				if current.State != StateQuotaReleasePending || current.Settlement == nil {
					return ErrAuthorityInvalid
				}
				current.State = StateReleased
				return nil
			})
			return result, updateErr
		}
		if err == nil && result.SafeToRemoveFinalizer && record.State == StatePrepared {
			_, updateErr := a.Records.Update(ctx, ref, func(current *AuthorityRecord) error {
				if current.State != StatePrepared || current.QuotaIntent != nil || current.QuotaHold != nil {
					return ErrAuthorityInvalid
				}
				current.State = StateReleased
				return nil
			})
			return result, updateErr
		}
		return result, err
	}
	if record.QuotaHold == nil {
		return result, ErrAuthorityInvalid
	}
	if record.State == StateQuotaReleasePending && record.Settlement != nil {
		completed, err := a.Lifecycle.CompleteExternalRelease(ctx, claimUID, ref, record.QuotaHold.Ref(), consumer)
		if err != nil {
			return result, err
		}
		if _, err := a.Records.Update(ctx, ref, func(current *AuthorityRecord) error {
			if current.State != StateQuotaReleasePending || current.Settlement == nil {
				return ErrAuthorityInvalid
			}
			current.State = StateReleased
			return nil
		}); err != nil {
			return result, err
		}
		return completed, nil
	}
	if _, err := a.Records.Update(ctx, ref, func(current *AuthorityRecord) error {
		if current.State != StateBound && current.State != StateQuotaIntent && current.State != StateQuotaGranted && current.State != StateCleanupVerified {
			return ErrAuthorityInvalid
		}
		current.State = StateCleanupVerified
		return nil
	}); err != nil {
		return result, err
	}
	receipt, err := a.Custodian.Settle(ctx, consumer, ref, *record.QuotaHold)
	if err != nil {
		return result, ErrQuotaReleaseUnknown
	}
	if !validSettlement(ref, *record.QuotaHold, receipt) {
		return result, ErrQuotaReleaseUnknown
	}
	if _, err := a.Records.Update(ctx, ref, func(current *AuthorityRecord) error {
		if current.State != StateCleanupVerified {
			return ErrAuthorityInvalid
		}
		current.State, current.Settlement = StateQuotaReleasePending, &receipt
		return nil
	}); err != nil {
		return result, err
	}
	completed, err := a.Lifecycle.CompleteExternalRelease(ctx, claimUID, ref, record.QuotaHold.Ref(), consumer)
	if err != nil {
		return result, err
	}
	if _, err := a.Records.Update(ctx, ref, func(current *AuthorityRecord) error {
		if current.State != StateQuotaReleasePending || current.Settlement == nil {
			return ErrAuthorityInvalid
		}
		current.State = StateReleased
		return nil
	}); err != nil {
		return result, err
	}
	return completed, nil
}

func validSettlement(authority controller.AuthorityRef, hold QuotaHold, receipt SettlementReceipt) bool {
	want, err := digest(hold)
	return err == nil && receipt.AuthorityRecordID == authority.RecordID && receipt.Hold == hold.Ref() && receipt.HoldDigest == want && receipt.AcknowledgmentID != ""
}
