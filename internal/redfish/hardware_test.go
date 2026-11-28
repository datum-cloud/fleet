package redfish

import "testing"

func TestArchitecture(t *testing.T) {
	for _, c := range []struct{ processor, set, want string }{
		{"x86", "x86-64", "amd64"},
		{"x86", "", "amd64"},
		{"ARM", "ARM-A64", "arm64"},
		{"ARM", "ARM-A32", "ARM-A32"},
		{"ARM", "", "ARM"},
		{"", "", ""},
	} {
		if got := architecture(c.processor, c.set); got != c.want {
			t.Errorf("architecture(%q, %q) = %q, want %q", c.processor, c.set, got, c.want)
		}
	}
}
