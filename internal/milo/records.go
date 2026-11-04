package milo

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"go.datum.net/fleet/internal/controller"
)

const (
	authoritySchemaVersion = 1
	authorityDataKey       = "record.json"
	authorityType          = corev1.SecretType("fleet.datumapis.com/private-milo-authority")
)

// RecordStore persists private authority state in a service-controlled
// namespace. It must not be backed by a consumer-visible project namespace.
type RecordStore struct {
	client    client.Client
	namespace string
}

func NewRecordStore(kube client.Client, namespace string) (*RecordStore, error) {
	if kube == nil || namespace == "" {
		return nil, ErrAuthorityInvalid
	}
	return &RecordStore{client: kube, namespace: namespace}, nil
}

func authorityRecordName(authorityID string, projectUID, claimUID types.UID) string {
	return "fleet-milo-" + shortDigest(authorityID+"/"+string(projectUID)+"/"+string(claimUID))
}

func (s *RecordStore) Create(ctx context.Context, record AuthorityRecord) (controller.AuthorityRef, error) {
	if err := validateAuthorityRecord(&record); err != nil {
		return controller.AuthorityRef{}, err
	}
	name := authorityRecordName(record.SourceRequest.ConsumerProject.AuthorityID, record.SourceRequest.ConsumerProject.UID, record.ClaimUID)
	secret := corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.namespace, Labels: map[string]string{"fleet.datumapis.com/private-record": "milo-authority", "fleet.datumapis.com/claim-uid": string(record.ClaimUID)}}, Type: authorityType}
	if err := encodeAuthority(&secret, &record); err != nil {
		return controller.AuthorityRef{}, err
	}
	if err := s.client.Create(ctx, &secret); apierrors.IsAlreadyExists(err) {
		existing, _, getErr := s.getByName(ctx, name)
		if getErr != nil {
			return controller.AuthorityRef{}, getErr
		}
		if !sameAuthorityContract(existing, &record) {
			return controller.AuthorityRef{}, fmt.Errorf("%w: authority record name is occupied by another lifetime", ErrAuthorityInvalid)
		}
		return authorityRef(name, existing), nil
	} else if err != nil {
		return controller.AuthorityRef{}, err
	}
	return authorityRef(name, &record), nil
}

func (s *RecordStore) Get(ctx context.Context, ref controller.AuthorityRef) (*AuthorityRecord, error) {
	if !ref.Valid() || ref.Provider != "milo" {
		return nil, ErrAuthorityInvalid
	}
	record, _, err := s.getByName(ctx, ref.RecordName)
	if err != nil {
		return nil, err
	}
	if record.RecordID != ref.RecordID || record.ContractDigest != ref.ContractDigest {
		return nil, ErrAuthorityInvalid
	}
	return record, nil
}

func (s *RecordStore) GetForClaim(ctx context.Context, consumer controller.ProjectIdentity, claimUID types.UID) (*AuthorityRecord, controller.AuthorityRef, error) {
	if !consumer.Valid() {
		return nil, controller.AuthorityRef{}, ErrAuthorityInvalid
	}
	name := authorityRecordName(consumer.AuthorityID, consumer.UID, claimUID)
	record, _, err := s.getByName(ctx, name)
	if err != nil {
		return nil, controller.AuthorityRef{}, err
	}
	return record, authorityRef(name, record), nil
}

func (s *RecordStore) Update(ctx context.Context, ref controller.AuthorityRef, mutate func(*AuthorityRecord) error) (*AuthorityRecord, error) {
	var result *AuthorityRecord
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		record, secret, err := s.getByName(ctx, ref.RecordName)
		if err != nil {
			return err
		}
		if record.RecordID != ref.RecordID || record.ContractDigest != ref.ContractDigest {
			return ErrAuthorityInvalid
		}
		if err := mutate(record); err != nil {
			return err
		}
		if err := validateAuthorityRecord(record); err != nil {
			return err
		}
		if err := encodeAuthority(secret, record); err != nil {
			return err
		}
		if err := s.client.Update(ctx, secret); err != nil {
			return err
		}
		result = record
		return nil
	})
	return result, err
}

func (s *RecordStore) getByName(ctx context.Context, name string) (*AuthorityRecord, *corev1.Secret, error) {
	var secret corev1.Secret
	if err := s.client.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: name}, &secret); err != nil {
		return nil, nil, err
	}
	var record AuthorityRecord
	if secret.Type != authorityType || secret.Labels["fleet.datumapis.com/private-record"] != "milo-authority" || json.Unmarshal(secret.Data[authorityDataKey], &record) != nil || validateAuthorityRecord(&record) != nil || secret.Name != authorityRecordName(record.SourceRequest.ConsumerProject.AuthorityID, record.SourceRequest.ConsumerProject.UID, record.ClaimUID) || secret.Labels["fleet.datumapis.com/claim-uid"] != string(record.ClaimUID) {
		return nil, nil, ErrAuthorityInvalid
	}
	return &record, &secret, nil
}

func encodeAuthority(secret *corev1.Secret, record *AuthorityRecord) error {
	b, err := json.Marshal(record)
	if err != nil {
		return err
	}
	secret.Type = authorityType
	secret.Data = map[string][]byte{authorityDataKey: b}
	return nil
}

func authorityRef(name string, record *AuthorityRecord) controller.AuthorityRef {
	return controller.AuthorityRef{Provider: "milo", RecordName: name, RecordID: record.RecordID, ContractDigest: record.ContractDigest}
}

func sameAuthorityContract(a, b *AuthorityRecord) bool {
	left, _ := json.Marshal(a.AcceptedRequirements)
	right, _ := json.Marshal(b.AcceptedRequirements)
	return a.RecordID == b.RecordID && a.ClaimNamespace == b.ClaimNamespace && a.ClaimName == b.ClaimName && a.ClaimUID == b.ClaimUID && a.SourceRequest == b.SourceRequest && a.SourceReceipt == b.SourceReceipt && a.ContractDigest == b.ContractDigest && string(left) == string(right)
}

func validateAuthorityRecord(r *AuthorityRecord) error {
	if r.SchemaVersion != authoritySchemaVersion || r.RecordID == "" || r.ClaimNamespace == "" || r.ClaimName == "" || r.ClaimUID == "" || r.SourceRequest.Validate() != nil || validateReceipt(r.SourceRequest, r.SourceReceipt) != nil || r.ContractDigest == "" {
		return ErrAuthorityInvalid
	}
	contract := struct {
		Request SourceUseRequest `json:"request"`
		Receipt SourceUseReceipt `json:"receipt"`
	}{r.SourceRequest, r.SourceReceipt}
	want, err := digest(contract)
	if err != nil || want != r.ContractDigest {
		return ErrAuthorityInvalid
	}
	requirements, err := digest(r.AcceptedRequirements)
	if err != nil || requirements != r.SourceRequest.RequirementsDigest {
		return ErrAuthorityInvalid
	}
	switch r.State {
	case StatePrepared:
		if r.Entitlement != nil || r.QuotaIntent != nil || r.QuotaHold != nil || r.FenceReceipt != nil || r.Settlement != nil || r.AssetID != "" || r.BindingID != "" {
			return ErrAuthorityInvalid
		}
	case StateQuotaIntent:
		if r.QuotaIntent == nil || r.Entitlement == nil || r.FenceReceipt != nil || r.Settlement != nil || r.AssetID != "" || r.BindingID != "" {
			return ErrAuthorityInvalid
		}
	case StateQuotaGranted:
		if r.QuotaIntent == nil || r.QuotaHold == nil || r.Entitlement == nil || r.Settlement != nil || r.AssetID != "" || r.BindingID != "" {
			return ErrAuthorityInvalid
		}
	case StateBound:
		if r.QuotaIntent == nil || r.QuotaHold == nil || r.Entitlement == nil || r.FenceReceipt == nil || r.Settlement != nil || r.AssetID == "" || r.BindingID == "" {
			return ErrAuthorityInvalid
		}
	case StateCleanupVerified:
		if r.QuotaIntent == nil || r.QuotaHold == nil || r.Entitlement == nil || r.Settlement != nil {
			return ErrAuthorityInvalid
		}
	case StateQuotaReleasePending:
		if r.QuotaIntent == nil || r.QuotaHold == nil || r.Entitlement == nil {
			return ErrAuthorityInvalid
		}
	case StateReleased:
		activeQuota := r.QuotaIntent != nil || r.QuotaHold != nil || r.Entitlement != nil || r.Settlement != nil
		if activeQuota && (r.QuotaIntent == nil || r.QuotaHold == nil || r.Entitlement == nil || r.Settlement == nil) {
			return ErrAuthorityInvalid
		}
		if !activeQuota && (r.FenceReceipt != nil || r.AssetID != "" || r.BindingID != "") {
			return ErrAuthorityInvalid
		}
	case StateQuarantined:
	default:
		return ErrAuthorityInvalid
	}
	if (r.AssetID == "") != (r.BindingID == "") {
		return ErrAuthorityInvalid
	}
	if r.QuotaHold != nil && (!r.QuotaHold.Ref().Valid() || r.QuotaIntent == nil || r.QuotaHold.Intent != *r.QuotaIntent) {
		return ErrAuthorityInvalid
	}
	if r.QuotaIntent != nil && !r.QuotaIntent.Valid() || r.QuotaHold != nil && !r.QuotaHold.Valid() || r.Entitlement != nil && !r.Entitlement.Valid() {
		return ErrAuthorityInvalid
	}
	if r.FenceReceipt != nil {
		if r.QuotaHold == nil || r.Entitlement == nil {
			return ErrAuthorityInvalid
		}
		ref := authorityRef(authorityRecordName(r.SourceRequest.ConsumerProject.AuthorityID, r.SourceRequest.ConsumerProject.UID, r.ClaimUID), r)
		commitDigest, digestErr := digest(AdmissionCommit{Authority: ref, SourceUse: r.SourceRequest, Source: r.SourceReceipt, Entitlement: *r.Entitlement, Hold: *r.QuotaHold})
		if digestErr != nil || r.FenceReceipt.ContractDigest != r.ContractDigest || r.FenceReceipt.CommitDigest != commitDigest || r.FenceReceipt.FenceToken != r.SourceReceipt.FenceToken || r.FenceReceipt.CommitID == "" {
			return ErrAuthorityInvalid
		}
	}
	if r.State == StateReleased && r.QuotaHold != nil && r.Settlement == nil {
		return ErrAuthorityInvalid
	}
	if r.Settlement != nil && (r.QuotaHold == nil || !validSettlement(controller.AuthorityRef{RecordID: r.RecordID}, *r.QuotaHold, *r.Settlement)) {
		return ErrAuthorityInvalid
	}
	if r.NoHold != nil && (r.NoHold.AuthorityRecordID != r.RecordID || r.NoHold.IntentDigest == "" || r.NoHold.AcknowledgmentID == "") {
		return ErrAuthorityInvalid
	}
	if (r.LastSettledHold == nil) != (r.LastSettlement == nil) || r.LastSettlement != nil && !validSettlement(controller.AuthorityRef{RecordID: r.RecordID}, *r.LastSettledHold, *r.LastSettlement) {
		return ErrAuthorityInvalid
	}
	return nil
}
