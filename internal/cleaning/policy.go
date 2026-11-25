// Package cleaning validates CleaningPolicies and matches them to drives, as
// proposed in datum-cloud/fleet#183. It is pure: it reads no API and sends
// nothing to a server.
package cleaning

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
)

// Bounds keep a policy, and every allocation that copies it, small.
const (
	MaxRules       = 16
	MaxMethods     = 5
	MaxModels      = 32
	MaxFirmware    = 16
	MinSamples     = 16
	MaxSamples     = 1024
	DefaultSamples = 64
)

// method is what Fleet knows about one cleaning method: the assurance it can
// reach, the protocols it applies to, and the checks it may use.
type method struct {
	assurance ops.Assurance
	protocols []ops.DriveProtocol
	verify    []ops.VerifyMode
}

var methods = map[ops.CleaningMethod]method{
	ops.MethodCryptographicErase: {ops.AssurancePurge, []ops.DriveProtocol{ops.ProtocolNVMe, ops.ProtocolSATA, ops.ProtocolSAS}, []ops.VerifyMode{ops.VerifyCanary}},
	ops.MethodBlockErase:         {ops.AssurancePurge, []ops.DriveProtocol{ops.ProtocolNVMe, ops.ProtocolSATA, ops.ProtocolSAS}, []ops.VerifyMode{ops.VerifyCanary, ops.VerifyFull}},
	ops.MethodSanitizeOverwrite:  {ops.AssurancePurge, []ops.DriveProtocol{ops.ProtocolNVMe, ops.ProtocolSATA, ops.ProtocolSAS}, []ops.VerifyMode{ops.VerifyCanary, ops.VerifyFull}},
	ops.MethodFormat:             {ops.AssuranceClear, []ops.DriveProtocol{ops.ProtocolNVMe, ops.ProtocolSAS}, []ops.VerifyMode{ops.VerifyCanary, ops.VerifyFull}},
	ops.MethodSecureErase:        {ops.AssuranceClear, []ops.DriveProtocol{ops.ProtocolSATA}, []ops.VerifyMode{ops.VerifyCanary, ops.VerifyFull}},
	ops.MethodOverwrite:          {ops.AssuranceClear, []ops.DriveProtocol{ops.ProtocolNVMe, ops.ProtocolSATA, ops.ProtocolSAS}, []ops.VerifyMode{ops.VerifyFull}},
}

// AssuranceOf returns the assurance a method can reach, or "" for an unknown
// method.
func AssuranceOf(m ops.CleaningMethod) ops.Assurance { return methods[m].assurance }

// Digest is the policy digest allocations pin: the SHA-256 of the canonical
// spec, computed the way custody digests operation inputs.
func Digest(spec ops.CleaningPolicySpec) (string, error) {
	data, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// Validate reports every reason the spec is not a usable policy. A rule may
// list only methods that reach the policy's assurance, so a fallback never
// lowers it.
func Validate(spec ops.CleaningPolicySpec) error {
	var errs []error
	if spec.Assurance != ops.AssuranceClear && spec.Assurance != ops.AssurancePurge {
		errs = append(errs, fmt.Errorf("assurance %q is not Clear or Purge", spec.Assurance))
	}
	if len(spec.Rules) == 0 || len(spec.Rules) > MaxRules {
		errs = append(errs, fmt.Errorf("a policy has 1 to %d rules", MaxRules))
	}
	names := map[string]bool{}
	for i, rule := range spec.Rules {
		at := fmt.Sprintf("rule %d (%s)", i, rule.Name)
		if rule.Name == "" || len(rule.Name) > 63 {
			errs = append(errs, fmt.Errorf("%s: a rule name is 1 to 63 characters", at))
		}
		if names[rule.Name] {
			errs = append(errs, fmt.Errorf("%s: rule names are unique", at))
		}
		names[rule.Name] = true
		errs = append(errs, validateMatch(at, rule.Match)...)
		if rule.TimePerTiB.Duration <= 0 {
			errs = append(errs, fmt.Errorf("%s: timePerTiB is positive", at))
		}
		if len(rule.Methods) == 0 || len(rule.Methods) > MaxMethods {
			errs = append(errs, fmt.Errorf("%s: a rule lists 1 to %d methods", at, MaxMethods))
		}
		seen := map[ops.CleaningMethod]bool{}
		for _, m := range rule.Methods {
			known, ok := methods[m]
			switch {
			case !ok:
				errs = append(errs, fmt.Errorf("%s: unknown method %q", at, m))
				continue
			case seen[m]:
				errs = append(errs, fmt.Errorf("%s: method %s is listed twice", at, m))
			case !slices.Contains(known.protocols, rule.Match.Protocol):
				errs = append(errs, fmt.Errorf("%s: method %s does not apply to %s", at, m, rule.Match.Protocol))
			case !known.assurance.Reaches(spec.Assurance):
				errs = append(errs, fmt.Errorf("%s: method %s reaches only %s, below the policy's %s", at, m, known.assurance, spec.Assurance))
			case !slices.Contains(known.verify, rule.Verify.Mode):
				errs = append(errs, fmt.Errorf("%s: method %s cannot use %s verification", at, m, rule.Verify.Mode))
			}
			seen[m] = true
		}
		switch rule.Verify.Mode {
		case ops.VerifyFull:
			if rule.Verify.Samples != 0 {
				errs = append(errs, fmt.Errorf("%s: samples apply only to Canary verification", at))
			}
		case ops.VerifyCanary:
			if rule.Verify.Samples < MinSamples || rule.Verify.Samples > MaxSamples {
				errs = append(errs, fmt.Errorf("%s: Canary verification takes %d to %d samples", at, MinSamples, MaxSamples))
			}
		default:
			errs = append(errs, fmt.Errorf("%s: verification mode %q is not Full or Canary", at, rule.Verify.Mode))
		}
	}
	errs = append(errs, validateQualified(spec.Qualified)...)
	return errors.Join(errs...)
}

func validateQualified(qualified []ops.QualifiedModel) []error {
	var errs []error
	if len(qualified) > MaxModels {
		errs = append(errs, fmt.Errorf("a policy qualifies at most %d models", MaxModels))
	}
	for i, model := range qualified {
		at := fmt.Sprintf("qualified %d (%s)", i, model.Model)
		if model.Model == "" || len(model.Firmware) == 0 || len(model.Firmware) > MaxFirmware {
			errs = append(errs, fmt.Errorf("%s: a qualification names the model and 1 to %d firmware versions", at, MaxFirmware))
		}
		if len(model.Methods) == 0 || len(model.Methods) > len(methods) {
			errs = append(errs, fmt.Errorf("%s: a qualification lists the methods that passed", at))
		}
		for _, m := range model.Methods {
			if _, ok := methods[m]; !ok {
				errs = append(errs, fmt.Errorf("%s: unknown method %q", at, m))
			}
			if m == ops.MethodCryptographicErase && !model.AlwaysEncrypts {
				errs = append(errs, fmt.Errorf("%s: CryptographicErase needs a model that always encrypts", at))
			}
		}
	}
	return errs
}

func validateMatch(at string, match ops.DriveMatch) []error {
	var errs []error
	if !slices.Contains([]ops.DriveProtocol{ops.ProtocolNVMe, ops.ProtocolSATA, ops.ProtocolSAS}, match.Protocol) {
		errs = append(errs, fmt.Errorf("%s: protocol %q is not NVMe, SATA or SAS", at, match.Protocol))
	}
	if match.MediaType != ops.MediaSSD && match.MediaType != ops.MediaHDD {
		errs = append(errs, fmt.Errorf("%s: media type %q is not SSD or HDD", at, match.MediaType))
	}
	return errs
}

// Drive is what Fleet knows about one drive when it matches a policy.
type Drive struct {
	Protocol  ops.DriveProtocol
	MediaType ops.MediaType
	Model     string
	Firmware  string
}

// Match returns the first rule that matches the drive, or nil when none
// does.
func Match(spec ops.CleaningPolicySpec, drive Drive) *ops.CleaningRule {
	for i, rule := range spec.Rules {
		if rule.Match.Protocol == drive.Protocol && rule.Match.MediaType == drive.MediaType {
			return &spec.Rules[i]
		}
	}
	return nil
}

// Methods returns the rule's methods Fleet may plan for the drive, in order:
// every Clear method, and each Purge method the policy qualifies for the
// drive's model and firmware.
func Methods(spec ops.CleaningPolicySpec, rule ops.CleaningRule, drive Drive) []ops.CleaningMethod {
	var planned []ops.CleaningMethod
	for _, m := range rule.Methods {
		if AssuranceOf(m) != ops.AssurancePurge || qualified(spec, drive, m) {
			planned = append(planned, m)
		}
	}
	return planned
}

func qualified(spec ops.CleaningPolicySpec, drive Drive, m ops.CleaningMethod) bool {
	for _, model := range spec.Qualified {
		if model.Model == drive.Model && slices.Contains(model.Firmware, drive.Firmware) && slices.Contains(model.Methods, m) &&
			(m != ops.MethodCryptographicErase || model.AlwaysEncrypts) {
			return true
		}
	}
	return false
}
