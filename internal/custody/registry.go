// Package custody implements Device custody and coordinator admission, as
// proposed in datum-cloud/fleet#174 at 3907eec.
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
	// DeterministicName kinds are named from their authority and request, so
	// a retried create collides instead of making a second operation.
	DeterministicName Naming = iota
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
	// Settles lists the held kinds a success of this kind settles.
	Settles []SettleRule
	// Evidence validates the kind's typed, bounded result evidence and
	// returns it in canonical form. Nil means the kind's results carry none.
	Evidence func([]byte) ([]byte, error)
	// RecoverySettle validates DeviceRecovery Settle evidence for a hold of
	// this kind. A kind without it cannot be settled by a DeviceRecovery.
	RecoverySettle func(context.Context, *RecoverySettleContext) *Refusal
	// Apply runs when an admitted operation of a non-effectful kind is
	// confirmed by its site fence. It returns the custody to write.
	Apply func(context.Context, *ApplyContext) (*ops.DeviceCustodySpec, *Refusal)
	// Retires reports whether a succeeded operation of this kind retired the
	// Device. Rebuild reads it.
	Retires func(ops.Operation) bool
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
	Now       time.Time
}

// RecoverySettleContext is what a held kind's settle hook sees.
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
}

func (r *Refusal) Error() string { return r.Reason + ": " + r.Message }

func refuse(reason, format string, args ...any) *Refusal {
	return &Refusal{Reason: reason, Message: fmt.Sprintf(format, args...)}
}

// Shared refusal and cancellation reasons. Kinds add their own; the set is
// open.
const (
	ReasonUnverified         = "Unverified"
	ReasonNoMatchingRule     = "NoMatchingRule"
	ReasonHolderMismatch     = "HolderMismatch"
	ReasonOperationActive    = "OperationActive"
	ReasonIdentityMismatch   = "IdentityMismatch"
	ReasonIdentityNotCurrent = "IdentityNotCurrent"
	ReasonSharedPublicKey    = "SharedPublicKey"
	ReasonRecoveryHold       = "RecoveryHold"
	ReasonNameMismatch       = "NameMismatch"
	ReasonExpired            = "Expired"
	ReasonSuperseded         = "Superseded"
	ReasonWithdrawn          = "Withdrawn"
	ReasonDeadlineExceeded   = "DeadlineExceeded"
	ReasonSiteNotConfirmed   = "SiteNotConfirmed"
	ReasonSiteUnknown        = "SiteCouldNotProve"
	ReasonInvalidEvidence    = "InvalidEvidence"
)

// Registry holds the operation kinds and the admission rules.
type Registry struct {
	kinds map[string]*Kind
	order []string
	rules []Rule
}

func NewRegistry() *Registry { return &Registry{kinds: map[string]*Kind{}} }

// Register adds a kind. Registering a kind twice is an error.
func (r *Registry) Register(kind Kind) error {
	name := kind.GVK.Kind
	if name == "" || kind.New == nil || kind.NewList == nil || kind.Items == nil || kind.Digest == nil || kind.Deadline <= 0 {
		return errors.New("a kind needs a GVK, constructors, a digest and a deadline")
	}
	if kind.Naming == DeterministicName && kind.Name == nil {
		return fmt.Errorf("%s: a deterministic name rule is required", name)
	}
	if !kind.Effectful && kind.Apply == nil {
		return fmt.Errorf("%s: a non-effectful kind needs Apply", name)
	}
	if _, ok := r.kinds[name]; ok {
		return fmt.Errorf("%s is already registered", name)
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
	r.rules = append(r.rules, rule)
	return nil
}

func (r *Registry) Kind(name string) *Kind { return r.kinds[name] }

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
// unknown fields and anything over maxBytes, then runs validate.
func StrictEvidence[T any](maxBytes int, validate func(*T) error) func([]byte) ([]byte, error) {
	return func(raw []byte) ([]byte, error) {
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
			if err := validate(value); err != nil {
				return nil, err
			}
		}
		return json.Marshal(value)
	}
}
