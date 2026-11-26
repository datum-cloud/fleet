// Package custody implements Device custody and coordinator admission, as
// proposed in datum-cloud/fleet#174 at d4d17f9.
//
// The Coordinator is the only writer of DeviceCustody and of operation status.
// Every custody change is one Update carrying the resourceVersion it read; on
// a conflict it reads again and decides again. Operation kinds plug in through
// a Registry entry (Kind) and rows of the admission Rules table, so a new kind
// changes neither the core nor how existing rows are evaluated.
//
// Increment A is inert: nothing wires this package into a deployment root.
package custody

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"time"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Naming is how an operation kind's objects are named.
type Naming int

const (
	// KindNaming, on a row, means the kind's own naming rule applies. A kind
	// must name a rule of its own.
	KindNaming Naming = iota
	// DeterministicName kinds are named from their authority and request, so
	// a retried create collides instead of making a second operation.
	DeterministicName
	// GeneratedName kinds are staff requests. Each one is new, so each must
	// be created with metadata.generateName.
	GeneratedName
)

// Kind is the registry entry for one operation kind. The core reads only
// these fields; it never switches on a kind name.
type Kind struct {
	GVK schema.GroupVersionKind
	// New returns an empty object of the kind, and NewList an empty list.
	New     func() ops.Operation
	NewList func() client.ObjectList
	// Items returns the operations in a list made by NewList.
	Items func(client.ObjectList) []ops.Operation
	// Digest returns the digest of the operation's exact inputs.
	Digest func(ops.Operation) (string, error)
	Naming Naming
	// Name returns the only name a DeterministicName operation may have.
	Name func(ops.Operation) string
	// Deadline is how long the site may work on an admitted operation.
	Deadline time.Duration
	// Effectful kinds can reach the server. A non-effectful kind, such as
	// DeviceRecovery, that passes its deadline ends Failed: nothing it does
	// can leave the server in an unknown state.
	Effectful bool
	// SkipsDiscovery, when it returns true, exempts the operation from
	// admission checks 5 (current discovery) and 6 (a unique BMC key).
	// Only a kind that cannot reach the server may set it. DeviceRecovery
	// sets it for Retire and Resolve, which must work when a BMC is
	// unreachable. The Device UID is always checked.
	SkipsDiscovery func(ops.Operation) bool
	// Supersedes means a newer pending operation of this kind for the same
	// authority cancels an older pending one.
	Supersedes bool
	// Variant, when set, selects among this kind's rows, such as a recovery
	// decision. An empty row Variant matches every operation of the kind.
	Variant func(ops.Operation) string
	// BootInterface names the Device interface whose MAC the snapshot
	// captures, if the kind boots the server.
	BootInterface func(ops.Operation) string
	// Check runs kind-specific admission checks after the shared
	// preconditions pass and before the custody update. A non-nil refusal
	// ends the operation Failed with that reason, and nothing is sent.
	Check func(context.Context, *Admission) *Refusal
	// Settles lists the held kinds a success of this kind settles. It may be
	// empty: a kind whose holds only a DeviceRecovery settles, such as power,
	// settles no hold itself.
	Settles []SettleRule
	// Retryable means a DeviceRecovery with Retry applies to this kind's
	// holds: it lets the held operation's requester create one settling
	// operation.
	Retryable bool
	// Reasons are the kind's own refusal reasons. A refusal with a reason
	// that is neither shared nor declared here ends the operation Failed with
	// UndeclaredReason.
	Reasons []Reason
	// SnapshotExtra adds bounded fields to the Device snapshot at admission.
	// It sees the same Device the checks saw.
	SnapshotExtra func(*Admission) map[string]string
	// Evidence validates the kind's typed, bounded result evidence against
	// the operation it is for, such as requiring reset proof for a restart,
	// and returns it in canonical form. Nil means the kind's results carry
	// none.
	Evidence func(ops.Operation, []byte) ([]byte, error)
	// EvidenceRequired means a Succeeded result without evidence proves
	// nothing and ends OutcomeUnknown.
	EvidenceRequired bool
	// RecoverySettle validates DeviceRecovery Settle evidence for a hold of
	// this kind. A kind without it cannot be settled by a DeviceRecovery.
	RecoverySettle func(context.Context, *RecoverySettleContext) *Refusal
	// Apply runs when an admitted operation of a non-effectful kind is
	// confirmed by its site fence. It returns the custody to write.
	Apply func(context.Context, *ApplyContext) (*ops.DeviceCustodySpec, *Refusal)
	// Retires reports whether a succeeded operation of this kind retired the
	// Device. Rebuild and retention read it.
	Retires func(ops.Operation) bool
	// Clears returns the hold a succeeded operation of this kind cleared, if
	// any. Rebuild reads it to tell a settled unknown outcome from one that
	// is still held.
	Clears func(ops.Operation) *ops.HeldOperationReference
}

func (k *Kind) checksDiscovery(operation ops.Operation) bool {
	return k.SkipsDiscovery == nil || !k.SkipsDiscovery(operation)
}

// ReasonSource is where a refusal reason is raised.
type ReasonSource string

const (
	RaisedByCoordinator ReasonSource = "Coordinator"
	RaisedByWorker      ReasonSource = "Worker"
	RaisedByBoth        ReasonSource = "Both"
)

// Reason is one refusal reason a kind declares.
type Reason struct {
	Name     string
	RaisedBy ReasonSource
}

// SettleRule says a success of the owning kind settles a hold of Held. When
// RequiresRetry is set, a DeviceRecovery with Retry must first authorize it.
type SettleRule struct {
	Held          string
	RequiresRetry bool
}

// Admission is what a kind-specific check sees.
type Admission struct {
	Operation ops.Operation
	Custody   *ops.DeviceCustody
	Device    *inventory.Device
	Row       Rule
	Registry  *Registry
	// Reader reads the provider project. A check must not write.
	Reader client.Reader
	// Now is the coordinator's injected clock, read once per decision.
	Now time.Time
	// DiscoveryInterval is how often BMC discovery reads each BMC.
	DiscoveryInterval time.Duration
}

// RecoverySettleContext is what a held kind's settle hook sees. Hold carries
// when the held operation ended (Since), its deadline and its admission
// snapshot. A hold that rebuild created has Since set to the rebuild time,
// and has no deadline or snapshot if the operation's status never recorded
// them, so a hook must check both for nil. Device is read when the hook runs,
// at admission of the recovery and again when it is applied.
type RecoverySettleContext struct {
	Hold     ops.RecoveryHold
	Recovery *ops.DeviceRecovery
	Device   *inventory.Device
	Custody  *ops.DeviceCustody
	Now      time.Time
}

// ApplyContext is what a non-effectful kind's Apply sees.
type ApplyContext struct {
	Operation ops.Operation
	Custody   *ops.DeviceCustody
	Device    *inventory.Device
	Registry  *Registry
	// Reader reads the provider project. Apply must not write.
	Reader client.Reader
	// ConfirmedFence is the lowest fence the serving sites report, and
	// HighestFence the highest. Both are at least the operation's fence.
	ConfirmedFence int64
	HighestFence   int64
	Now            time.Time
}

// Refusal is a named reason an operation was not admitted or applied.
type Refusal struct {
	Reason  string
	Message string
	// Retry means the check could not decide, for example because a service
	// it reads is unreachable. The operation stays Pending and is checked
	// again; it is never admitted on an undecided check.
	Retry bool
}

func (r *Refusal) Error() string { return r.Reason + ": " + r.Message }

func refuse(reason, format string, args ...any) *Refusal {
	return &Refusal{Reason: reason, Message: fmt.Sprintf(format, args...)}
}

// Shared refusal and cancellation reasons. Kinds add their own; the set is
// open.
const (
	// The shared admission reasons of #174, named as it names them, with
	// the rule each refuses. NameMismatch and the outcome reasons below are
	// this implementation's additions.
	ReasonCustodyUnverified      = "CustodyUnverified"      // rule 1
	ReasonCreatorNotPermitted    = "CreatorNotPermitted"    // rule 2
	ReasonHolderMismatch         = "HolderMismatch"         // rule 2 or 3
	ReasonOperationInProgress    = "OperationInProgress"    // rule 4
	ReasonDeviceIdentityMismatch = "DeviceIdentityMismatch" // rule 5
	ReasonSharedPublicKey        = "SharedPublicKey"
	ReasonRecoveryHold           = "RecoveryHold"
	ReasonNameMismatch           = "NameMismatch"
	ReasonExpired                = "Expired"
	ReasonSuperseded             = "Superseded"
	ReasonWithdrawn              = "Withdrawn"
	ReasonDeadlineExceeded       = "DeadlineExceeded"
	ReasonSiteNotConfirmed       = "SiteNotConfirmed"
	ReasonSiteUnknown            = "SiteCouldNotProve"
	ReasonInvalidEvidence        = "InvalidEvidence"
	ReasonUndeclared             = "UndeclaredReason"
	ReasonSnapshotInvalid        = "SnapshotInvalid"
)

// Reasons the site framework raises for any kind.
const (
	ReasonUnsupportedKind   = "UnsupportedKind"
	ReasonInputMismatch     = "InputMismatch"
	ReasonCredentialMissing = "CredentialMissing"
	ReasonPinNotApproved    = "PinNotApproved"
	ReasonPublicKeyMismatch = "PublicKeyMismatch"
	ReasonNotEnrolled       = "NotEnrolled"
	ReasonBMCUnavailable    = "BMCUnavailable"
	ReasonWorkerRunning     = "OlderWorkerRunning"
	ReasonStale             = "Stale"
	ReasonStepUnknown       = "StepOutcomeUnknown"
)

// sharedReasons are the reasons every kind may raise.
var sharedReasons = []string{ReasonUnsupportedKind, ReasonInputMismatch, ReasonCredentialMissing, ReasonPinNotApproved, ReasonPublicKeyMismatch,
	ReasonNotEnrolled, ReasonBMCUnavailable, ReasonWorkerRunning, ReasonStale, ReasonStepUnknown, ReasonCustodyUnverified, ReasonCreatorNotPermitted, ReasonHolderMismatch, ReasonOperationInProgress, ReasonDeviceIdentityMismatch,
	ReasonSharedPublicKey, ReasonRecoveryHold, ReasonNameMismatch, ReasonExpired, ReasonSuperseded, ReasonWithdrawn, ReasonDeadlineExceeded,
	ReasonSiteNotConfirmed, ReasonSiteUnknown, ReasonInvalidEvidence, ReasonUndeclared, ReasonSnapshotInvalid}

var reasonPattern = regexp.MustCompile(`^[A-Z][A-Za-z0-9]{0,62}$`)

// Registry holds the operation kinds, the admission rules, and the checks
// for each kind of boot address claim.
type Registry struct {
	kinds      map[string]*Kind
	order      []string
	rules      []Rule
	bootClaims map[schema.GroupKind]BootAddressCheck
}

func NewRegistry() *Registry {
	return &Registry{kinds: map[string]*Kind{}, bootClaims: map[schema.GroupKind]BootAddressCheck{}}
}

// BootAddressCheck checks at admission that an operation's boot address
// claim is the operation's own and holds exactly the input's address. kind
// is the operation's kind. It must not write. It returns an error wrapping
// bootaddress.ErrUnreachable when the claim's source cannot be read, so the
// operation waits, and any other error when the claim does not hold the
// address, so the operation fails.
type BootAddressCheck func(ctx context.Context, reader client.Reader, operation ops.Operation, kind string, input ops.BootNetworkInput) error

// RegisterBootAddressClaim adds the check for one kind of boot address
// claim. Registering a claim kind twice is an error.
func (r *Registry) RegisterBootAddressClaim(apiGroup, kind string, check BootAddressCheck) error {
	key := schema.GroupKind{Group: apiGroup, Kind: kind}
	if apiGroup == "" || kind == "" || check == nil {
		return errors.New("a boot address claim check needs an API group, a kind and a check")
	}
	if _, ok := r.bootClaims[key]; ok {
		return fmt.Errorf("boot address claim %s/%s is already registered", apiGroup, kind)
	}
	r.bootClaims[key] = check
	return nil
}

// BootAddressClaim returns the check for a boot address claim kind, or nil.
func (r *Registry) BootAddressClaim(apiGroup, kind string) BootAddressCheck {
	return r.bootClaims[schema.GroupKind{Group: apiGroup, Kind: kind}]
}

// Register adds a kind. Registering a kind twice is an error.
func (r *Registry) Register(kind Kind) error {
	name := kind.GVK.Kind
	if name == "" || kind.New == nil || kind.NewList == nil || kind.Items == nil || kind.Digest == nil || kind.Deadline <= 0 {
		return errors.New("a kind needs a GVK, constructors, a digest and a deadline")
	}
	if kind.Naming != DeterministicName && kind.Naming != GeneratedName {
		return fmt.Errorf("%s: a kind names a deterministic or a generated naming rule", name)
	}
	if kind.Naming == DeterministicName && kind.Name == nil {
		return fmt.Errorf("%s: a deterministic name rule is required", name)
	}
	if kind.EvidenceRequired && kind.Evidence == nil {
		return fmt.Errorf("%s: required evidence needs a validator", name)
	}
	if !kind.Effectful && kind.Apply == nil {
		return fmt.Errorf("%s: a non-effectful kind needs Apply", name)
	}
	if kind.Effectful && kind.SkipsDiscovery != nil {
		return fmt.Errorf("%s: a kind that can reach the server always checks discovery", name)
	}
	if _, ok := r.kinds[name]; ok {
		return fmt.Errorf("%s is already registered", name)
	}
	seen := map[string]bool{}
	for _, reason := range kind.Reasons {
		switch {
		case !reasonPattern.MatchString(reason.Name):
			return fmt.Errorf("%s: reason %q is not a CamelCase name", name, reason.Name)
		case slices.Contains(sharedReasons, reason.Name):
			return fmt.Errorf("%s: reason %s is shared and needs no declaration", name, reason.Name)
		case seen[reason.Name]:
			return fmt.Errorf("%s: reason %s is declared twice", name, reason.Name)
		case reason.RaisedBy != RaisedByCoordinator && reason.RaisedBy != RaisedByWorker && reason.RaisedBy != RaisedByBoth:
			return fmt.Errorf("%s: reason %s needs a source", name, reason.Name)
		}
		seen[reason.Name] = true
	}
	r.kinds[name] = &kind
	r.order = append(r.order, name)
	sort.Strings(r.order)
	return nil
}

// AddRule appends an admission row. Rows are evaluated independently, so
// adding one never changes how another is evaluated.
func (r *Registry) AddRule(rule Rule) error {
	if _, ok := r.kinds[rule.Kind]; !ok {
		return fmt.Errorf("rule for unregistered kind %s", rule.Kind)
	}
	if rule.Creator == "" || (rule.Authority != AuthorityAllocation && rule.Authority != AuthorityService) {
		return errors.New("a rule needs a creator class and an authority type")
	}
	if rule.Queue && rule.Creator == CreatorStaff {
		return errors.New("staff operations never queue")
	}
	if rule.Naming == DeterministicName && rule.Name == nil {
		return errors.New("a row with a deterministic name rule needs its Name function")
	}
	r.rules = append(r.rules, rule)
	return nil
}

func (r *Registry) Kind(name string) *Kind { return r.kinds[name] }

// Declared reports whether a reason is shared or declared by the kind for
// the given source.
func (r *Registry) Declared(kind, reason string, source ReasonSource) bool {
	if slices.Contains(sharedReasons, reason) {
		return true
	}
	entry := r.kinds[kind]
	if entry == nil {
		return false
	}
	return slices.ContainsFunc(entry.Reasons, func(declared Reason) bool {
		return declared.Name == reason && (declared.RaisedBy == source || declared.RaisedBy == RaisedByBoth)
	})
}

// declaredRefusal returns the refusal unchanged when the kind, or the held
// kind whose settle hook it came through, declared its reason, and
// UndeclaredReason otherwise. Every recorded reason is one a kind registered.
func (r *Registry) declaredRefusal(kind, held string, refusal *Refusal) *Refusal {
	if refusal == nil || r.Declared(kind, refusal.Reason, RaisedByCoordinator) || (held != "" && r.Declared(held, refusal.Reason, RaisedByCoordinator)) {
		return refusal
	}
	return refuse(ReasonUndeclared, "%s refused with %s, which it does not declare", kind, refusal.Reason)
}

// settleHookKind is the held kind whose settle hook may refuse through this
// operation: only a non-effectful kind admitted to decide a hold, such as a
// DeviceRecovery, calls one. Any other kind's refusals must be its own.
func settleHookKind(kind *Kind, row Rule, custody *ops.DeviceCustody) string {
	if hold := custody.Spec.RecoveryHold; hold != nil && !kind.Effectful && row.Hold == HoldRequired {
		return hold.Kind
	}
	return ""
}

// Kinds returns every registered kind, sorted by name.
func (r *Registry) Kinds() []*Kind {
	out := make([]*Kind, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.kinds[name])
	}
	return out
}

func (r *Registry) Rules() []Rule { return append([]Rule(nil), r.rules...) }

// SpecDigest is the default input digest: sha256 over the canonical JSON of
// the object's spec. encoding/json orders struct fields and sorts map keys, so
// the encoding is stable.
func SpecDigest(spec any) (string, error) {
	data, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// StrictEvidence returns an Evidence function that decodes into T, rejecting
// unknown fields and anything over maxBytes, then runs validate with the
// operation the evidence is for.
func StrictEvidence[T any](maxBytes int, validate func(ops.Operation, *T) error) func(ops.Operation, []byte) ([]byte, error) {
	return func(operation ops.Operation, raw []byte) ([]byte, error) {
		if len(raw) > maxBytes {
			return nil, fmt.Errorf("evidence is %d bytes; the limit is %d", len(raw), maxBytes)
		}
		value := new(T)
		decoder := json.NewDecoder(bytesReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(value); err != nil {
			return nil, fmt.Errorf("evidence does not match the kind's schema: %w", err)
		}
		if decoder.More() {
			return nil, errors.New("evidence has trailing data")
		}
		if validate != nil {
			if err := validate(operation, value); err != nil {
				return nil, err
			}
		}
		return json.Marshal(value)
	}
}
