package main

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestSchemeRecognizesSecrets(t *testing.T) {
	kinds, _, err := scheme.ObjectKinds(&corev1.Secret{})
	if err != nil {
		t.Fatalf("scheme does not recognize Secret: %v", err)
	}
	if len(kinds) != 1 || kinds[0].Kind != "Secret" || kinds[0].Version != "v1" {
		t.Fatalf("unexpected kinds for Secret: %v", kinds)
	}
}
