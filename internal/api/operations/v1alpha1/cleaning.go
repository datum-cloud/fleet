package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// CleaningPolicy says how Fleet cleans each drive of a server before another
// customer can use it, what each step must prove, and what assurance the
// policy claims, as proposed in datum-cloud/fleet#183. Provider operators
// own it. Its spec is immutable: a changed policy is a new policy, and every
// allocation keeps the copy it pinned.
type CleaningPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CleaningPolicySpec `json:"spec"`
}

// Assurance is how thoroughly a method removes data, in the NIST SP 800-88
// and IEEE 2883 sense. Fleet makes no certification claim.
type Assurance string

const (
	AssuranceClear Assurance = "Clear"
	AssurancePurge Assurance = "Purge"
)

// Reaches reports whether a meets the required assurance.
func (a Assurance) Reaches(required Assurance) bool {
	return a == AssurancePurge || (a == AssuranceClear && required == AssuranceClear)
}

type DriveProtocol string

const (
	ProtocolNVMe DriveProtocol = "NVMe"
	ProtocolSATA DriveProtocol = "SATA"
	ProtocolSAS  DriveProtocol = "SAS"
)

type MediaType string

const (
	MediaSSD MediaType = "SSD"
	MediaHDD MediaType = "HDD"
)

// CleaningMethod is one way to erase a drive. Its assurance is fixed.
type CleaningMethod string

const (
	MethodCryptographicErase CleaningMethod = "CryptographicErase"
	MethodBlockErase         CleaningMethod = "BlockErase"
	MethodSanitizeOverwrite  CleaningMethod = "SanitizeOverwrite"
	MethodFormat             CleaningMethod = "Format"
	MethodSecureErase        CleaningMethod = "SecureErase"
	MethodOverwrite          CleaningMethod = "Overwrite"
)

type VerifyMode string

const (
	// VerifyFull reads every block back and compares it with the pattern.
	VerifyFull VerifyMode = "Full"
	// VerifyCanary writes random markers to sampled ranges before the erase
	// and requires that none remain after it.
	VerifyCanary VerifyMode = "Canary"
)

type CleaningPolicySpec struct {
	// Assurance is what every drive must reach.
	Assurance Assurance `json:"assurance"`
	// Rules are matched in order. The first rule that matches a drive applies.
	Rules []CleaningRule `json:"rules"`
	// Qualified lists the drive models and firmware that passed hardware
	// qualification, and the methods each passed. A Purge method runs on a
	// drive only when its model, firmware and that method are listed.
	Qualified []QualifiedModel `json:"qualified,omitempty"`
}

type CleaningRule struct {
	Name  string     `json:"name"`
	Match DriveMatch `json:"match"`
	// Methods are tried in order. Fleet plans only the methods qualified for
	// the drive.
	Methods []CleaningMethod `json:"methods"`
	Verify  VerifySpec       `json:"verify"`
	// TimePerTiB bounds how long each method may take per TiB of capacity.
	// The wipe's deadline is derived from it.
	TimePerTiB metav1.Duration `json:"timePerTiB"`
}

// DriveMatch selects drives by protocol and media type.
type DriveMatch struct {
	Protocol  DriveProtocol `json:"protocol"`
	MediaType MediaType     `json:"mediaType"`
}

// QualifiedModel is one drive model's hardware qualification.
type QualifiedModel struct {
	Model    string           `json:"model"`
	Firmware []string         `json:"firmware"`
	Methods  []CleaningMethod `json:"methods"`
	// AlwaysEncrypts says the model encrypts all user data and cannot turn
	// encryption off. CryptographicErase needs it.
	AlwaysEncrypts bool `json:"alwaysEncrypts,omitempty"`
}

type VerifySpec struct {
	Mode VerifyMode `json:"mode"`
	// Samples is the number of canary ranges. Canary only.
	Samples int32 `json:"samples,omitempty"`
}

// CleaningPolicyReference names one CleaningPolicy incarnation and the digest
// of its spec.
type CleaningPolicyReference struct {
	Name   string `json:"name"`
	UID    string `json:"uid"`
	Digest string `json:"digest"`
}

// PinnedCleaningPolicy is the copy of a CleaningPolicy an allocation keeps,
// so its release does not depend on the policy still existing.
type PinnedCleaningPolicy struct {
	CleaningPolicyReference `json:",inline"`
	Spec                    CleaningPolicySpec `json:"spec"`
}

type CleaningPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CleaningPolicy `json:"items"`
}

func (in *CleaningPolicy) DeepCopyObject() runtime.Object     { return deepCopy(in) }
func (in *CleaningPolicyList) DeepCopyObject() runtime.Object { return deepCopy(in) }
