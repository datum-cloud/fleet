package cleaning

import (
	"slices"
	"strings"
	"testing"
	"time"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var hour = metav1.Duration{Duration: time.Hour}

func purgePolicy() ops.CleaningPolicySpec {
	return ops.CleaningPolicySpec{
		Assurance: ops.AssurancePurge,
		Rules: []ops.CleaningRule{{
			Name:       "nvme-qualified",
			Match:      ops.DriveMatch{Protocol: ops.ProtocolNVMe, MediaType: ops.MediaSSD},
			Methods:    []ops.CleaningMethod{ops.MethodCryptographicErase, ops.MethodBlockErase},
			Verify:     ops.VerifySpec{Mode: ops.VerifyCanary, Samples: 64},
			TimePerTiB: metav1.Duration{Duration: 10 * time.Minute},
		}},
		Qualified: []ops.QualifiedModel{{Model: "EXAMPLE-NVME-3840", Firmware: []string{"EX1.2", "EX1.3"}, Methods: []ops.CleaningMethod{ops.MethodCryptographicErase, ops.MethodBlockErase}, AlwaysEncrypts: true}},
	}
}

func clearPolicy() ops.CleaningPolicySpec {
	return ops.CleaningPolicySpec{
		Assurance: ops.AssuranceClear,
		Rules: []ops.CleaningRule{
			{Name: "nvme", Match: ops.DriveMatch{Protocol: ops.ProtocolNVMe, MediaType: ops.MediaSSD}, Methods: []ops.CleaningMethod{ops.MethodFormat, ops.MethodOverwrite}, Verify: ops.VerifySpec{Mode: ops.VerifyFull}, TimePerTiB: hour},
			{Name: "any-sata", Match: ops.DriveMatch{Protocol: ops.ProtocolSATA, MediaType: ops.MediaHDD}, Methods: []ops.CleaningMethod{ops.MethodOverwrite}, Verify: ops.VerifySpec{Mode: ops.VerifyFull}, TimePerTiB: hour},
		},
	}
}

func TestValidateAcceptsPolicies(t *testing.T) {
	for name, spec := range map[string]ops.CleaningPolicySpec{"purge": purgePolicy(), "clear": clearPolicy()} {
		if err := Validate(spec); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]struct {
		mutate func(*ops.CleaningPolicySpec)
		want   string
	}{
		"fallback below assurance": {func(s *ops.CleaningPolicySpec) {
			s.Rules[0].Methods = append(s.Rules[0].Methods, ops.MethodOverwrite)
		}, "below the policy's Purge"},
		"crypto erase without always-on encryption": {func(s *ops.CleaningPolicySpec) { s.Qualified[0].AlwaysEncrypts = false }, "always encrypts"},
		"no time bound": {func(s *ops.CleaningPolicySpec) { s.Rules[0].TimePerTiB = metav1.Duration{} }, "timePerTiB"},
		"method for another protocol": {func(s *ops.CleaningPolicySpec) {
			s.Assurance = ops.AssuranceClear
			s.Rules[0].Methods = []ops.CleaningMethod{ops.MethodSecureErase}
		}, "does not apply to NVMe"},
		"overwrite without full verify": {func(s *ops.CleaningPolicySpec) {
			s.Assurance = ops.AssuranceClear
			s.Rules[0].Methods = []ops.CleaningMethod{ops.MethodOverwrite}
		}, "cannot use Canary"},
		"duplicate method": {func(s *ops.CleaningPolicySpec) {
			s.Rules[0].Methods = []ops.CleaningMethod{ops.MethodBlockErase, ops.MethodBlockErase}
		}, "listed twice"},
		"unknown method":    {func(s *ops.CleaningPolicySpec) { s.Rules[0].Methods = []ops.CleaningMethod{"Shred"} }, "unknown method"},
		"too few samples":   {func(s *ops.CleaningPolicySpec) { s.Rules[0].Verify.Samples = 1 }, "samples"},
		"no rules":          {func(s *ops.CleaningPolicySpec) { s.Rules = nil }, "1 to 16 rules"},
		"bad assurance":     {func(s *ops.CleaningPolicySpec) { s.Assurance = "Destroy" }, "not Clear or Purge"},
		"duplicate rule":    {func(s *ops.CleaningPolicySpec) { s.Rules = append(s.Rules, s.Rules[0]) }, "unique"},
		"model no firmware": {func(s *ops.CleaningPolicySpec) { s.Qualified[0].Firmware = nil }, "firmware"},
	}
	for name, tc := range cases {
		spec := purgePolicy()
		tc.mutate(&spec)
		err := Validate(spec)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error containing %q", name, err, tc.want)
		}
	}
}

func TestMatchAndQualifiedMethods(t *testing.T) {
	spec := purgePolicy()
	rule := Match(spec, Drive{ops.ProtocolNVMe, ops.MediaSSD, "EXAMPLE-NVME-3840", "EX1.3"})
	if rule == nil || rule.Name != "nvme-qualified" {
		t.Fatalf("matching rule: got %v", rule)
	}
	if rule := Match(spec, Drive{ops.ProtocolSATA, ops.MediaSSD, "EXAMPLE-NVME-3840", "EX1.3"}); rule != nil {
		t.Fatalf("another protocol matched %s", rule.Name)
	}
	cases := []struct {
		name  string
		drive Drive
		edit  func(*ops.CleaningPolicySpec)
		want  []ops.CleaningMethod
	}{
		{"qualified", Drive{ops.ProtocolNVMe, ops.MediaSSD, "EXAMPLE-NVME-3840", "EX1.3"}, nil, []ops.CleaningMethod{ops.MethodCryptographicErase, ops.MethodBlockErase}},
		{"unqualified firmware", Drive{ops.ProtocolNVMe, ops.MediaSSD, "EXAMPLE-NVME-3840", "EX0.9"}, nil, nil},
		{"unqualified model", Drive{ops.ProtocolNVMe, ops.MediaSSD, "OTHER", "EX1.3"}, nil, nil},
		{"method not qualified", Drive{ops.ProtocolNVMe, ops.MediaSSD, "EXAMPLE-NVME-3840", "EX1.3"},
			func(s *ops.CleaningPolicySpec) { s.Qualified[0].Methods = []ops.CleaningMethod{ops.MethodBlockErase} }, []ops.CleaningMethod{ops.MethodBlockErase}},
		{"crypto erase without always-on encryption", Drive{ops.ProtocolNVMe, ops.MediaSSD, "EXAMPLE-NVME-3840", "EX1.3"},
			func(s *ops.CleaningPolicySpec) { s.Qualified[0].AlwaysEncrypts = false }, []ops.CleaningMethod{ops.MethodBlockErase}},
	}
	for _, tc := range cases {
		spec := purgePolicy()
		if tc.edit != nil {
			tc.edit(&spec)
		}
		if got := Methods(spec, spec.Rules[0], tc.drive); !slices.Equal(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
	clear := clearPolicy()
	if got := Methods(clear, clear.Rules[0], Drive{ops.ProtocolNVMe, ops.MediaSSD, "any", "any"}); len(got) != 2 {
		t.Errorf("Clear methods need no qualification: got %v", got)
	}
}

func TestDigestChangesWithSpec(t *testing.T) {
	a, _ := Digest(purgePolicy())
	changed := purgePolicy()
	changed.Rules[0].Verify.Samples = 65
	b, _ := Digest(changed)
	if a == b || !strings.HasPrefix(a, "sha256:") || len(a) != 71 {
		t.Fatalf("digests %s and %s", a, b)
	}
}
