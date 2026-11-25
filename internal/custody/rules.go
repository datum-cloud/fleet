package custody

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
)

// CreatorClass is the requester class a row admits.
type CreatorClass string

const (
	// CreatorMetalProvider is the Fleet provider for Metal.
	CreatorMetalProvider CreatorClass = "MetalProvider"
	// CreatorReleaseController is the release controller.
	CreatorReleaseController CreatorClass = "ReleaseController"
	// CreatorStaff is every creator that is not a configured service
	// principal. IAM decides which staff may create which kind; the API
	// server has already authorized the create.
	CreatorStaff CreatorClass = "Staff"
)

// AuthorityType is which authority field an operation sets.
type AuthorityType string

const (
	AuthorityAllocation AuthorityType = "Allocation"
	AuthorityService    AuthorityType = "ServiceRequest"
)

// HoldBehavior is how a row treats a recovery hold.
type HoldBehavior string

const (
	// HoldSettles admits the operation under a hold only if its kind settles
	// the held kind. This is the default.
	HoldSettles HoldBehavior = ""
	// HoldRequired admits the operation only while a hold exists.
	HoldRequired HoldBehavior = "Required"
	// HoldIgnored admits the operation whether or not a hold exists.
	HoldIgnored HoldBehavior = "Ignored"
	// HoldSettlesRequired admits the operation only under a hold it
	// settles, such as a staff power operation from holder Service under a
	// power hold.
	HoldSettlesRequired HoldBehavior = "SettlesRequired"
)

// Rule is one row of the operations table in #174.
type Rule struct {
	Kind    string
	Variant string
	Creator CreatorClass
	// Authority is the authority type the operation must set.
	Authority AuthorityType
	// Holders are the holders the row admits from. Empty admits any holder.
	Holders []ops.HolderType
	// Custody is the custody state the row admits from. Empty is Verified.
	Custody ops.CustodyState
	Hold    HoldBehavior
	// Queue rows wait in Pending while another operation runs. Staff rows
	// never queue: admission refuses what cannot run now.
	Queue bool
}

func (r Rule) custodyState() ops.CustodyState {
	if r.Custody == "" {
		return ops.CustodyVerified
	}
	return r.Custody
}

func (r Rule) admitsHolder(holder ops.HolderType) bool {
	return len(r.Holders) == 0 || slices.Contains(r.Holders, holder)
}

// Principal is one configured service identity. An empty UID matches any UID
// for the username.
type Principal struct {
	Username string `json:"username"`
	UID      string `json:"uid,omitempty"`
}

// Principals maps service creator classes to their identities. Staff is never
// configured: it is every other creator.
type Principals map[CreatorClass][]Principal

// Validate rejects a configuration that names Staff, an empty username, or
// one identity in two classes.
func (p Principals) Validate() error {
	seen := map[string]CreatorClass{}
	for class, principals := range p {
		if class == CreatorStaff || class == "" {
			return errors.New("staff is every unconfigured creator and cannot be configured")
		}
		for _, principal := range principals {
			if principal.Username == "" {
				return fmt.Errorf("%s: a principal needs a username", class)
			}
			if other, ok := seen[principal.Username]; ok && other != class {
				return fmt.Errorf("%s is configured for both %s and %s", principal.Username, other, class)
			}
			seen[principal.Username] = class
		}
	}
	return nil
}

// Classify returns the class of an authenticated creator.
func (p Principals) Classify(creator ops.Creator) CreatorClass {
	for class, principals := range p {
		for _, principal := range principals {
			if principal.Username == creator.Username && (principal.UID == "" || principal.UID == creator.UID) {
				return class
			}
		}
	}
	return CreatorStaff
}

func authorityType(authority ops.Authority) (AuthorityType, bool) {
	switch {
	case authority.AllocationRef != nil && authority.ServiceRequest == nil:
		return AuthorityAllocation, true
	case authority.ServiceRequest != nil && authority.AllocationRef == nil:
		return AuthorityService, true
	}
	return "", false
}

// matchRows returns the rows for the operation's kind, variant, creator class
// and authority type.
func (r *Registry) matchRows(kind *Kind, operation ops.Operation, class CreatorClass) []Rule {
	authority, ok := authorityType(operation.Envelope().Authority)
	if !ok {
		return nil
	}
	variant := ""
	if kind.Variant != nil {
		variant = kind.Variant(operation)
	}
	var out []Rule
	for _, rule := range r.rules {
		if rule.Kind == kind.GVK.Kind && (rule.Variant == "" || rule.Variant == variant) && rule.Creator == class && rule.Authority == authority {
			out = append(out, rule)
		}
	}
	return out
}

func bytesReader(data []byte) io.Reader { return bytes.NewReader(data) }
