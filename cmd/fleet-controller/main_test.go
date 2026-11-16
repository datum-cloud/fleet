package main

import (
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"go.datum.net/fleet/internal/controller"
)

func TestLifecycleModeIsExplicitAndFailClosed(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kube := fake.NewClientBuilder().WithScheme(scheme).Build()
	authority, ready, err := lifecycleWiring("production", kube, "private")
	if err != nil || authority != nil || !errors.Is(ready(nil), controller.ErrUnsupported) {
		t.Fatalf("production did not fail closed: %v %v", authority, err)
	}
	if _, _, err := lifecycleWiring("typo", kube, "private"); err == nil {
		t.Fatal("unrecognized mode was accepted")
	}
	authority, ready, err = lifecycleWiring("synthetic", kube, "private")
	if err != nil || ready(nil) != nil {
		t.Fatalf("synthetic wiring: %v", err)
	}
	concrete, ok := authority.(*controller.LifecycleSessionAuthority)
	if !ok || concrete.Coordinator == nil || concrete.Resolver == nil {
		t.Fatalf("missing real custody or resolver: %T", authority)
	}
	if _, _, err := lifecycleWiring("synthetic", kube, ""); err == nil {
		t.Fatal("accepted missing private namespace")
	}
}
