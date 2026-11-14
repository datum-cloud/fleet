package manager

import (
	"context"
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	resourcemanagerv1alpha1 "go.miloapis.com/milo/pkg/apis/resourcemanager/v1alpha1"
)

func TestResolveProjectIdentityUsesMiloUID(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := resourcemanagerv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&resourcemanagerv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "datum-cloud", UID: types.UID("project-lifetime-1")},
	}).Build()

	got, err := ResolveProjectIdentity(context.Background(), reader, "staging-milo", "datum-cloud")
	if err != nil {
		t.Fatal(err)
	}
	if got.AuthorityID != "staging-milo" || got.RouteName != "datum-cloud" || got.UID != "project-lifetime-1" {
		t.Fatalf("unexpected identity: %#v", got)
	}
}

func TestResolveProjectIdentityRejectsUnavailableLifetime(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := resourcemanagerv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	now := metav1.NewTime(time.Now())
	tests := map[string]*resourcemanagerv1alpha1.Project{
		"missing uid": {ObjectMeta: metav1.ObjectMeta{Name: "datum-cloud"}},
		"deleting":    {ObjectMeta: metav1.ObjectMeta{Name: "datum-cloud", UID: "project-lifetime-1", DeletionTimestamp: &now, Finalizers: []string{"test"}}},
	}
	for name, project := range tests {
		project := project
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(project).Build()
			if _, err := ResolveProjectIdentity(context.Background(), reader, "staging-milo", "datum-cloud"); !errors.Is(err, ErrProjectIdentityUnavailable) {
				t.Fatalf("expected ErrProjectIdentityUnavailable, got %v", err)
			}
		})
	}
}

func TestResolveProjectIdentityRejectsConfigurationAndLookupFailures(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := resourcemanagerv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).Build()
	tests := map[string]struct {
		authorityID string
		projectName string
	}{
		"missing authority": {projectName: "datum-cloud"},
		"missing name":      {authorityID: "staging-milo"},
		"unknown project":   {authorityID: "staging-milo", projectName: "datum-cloud"},
	}
	for name, test := range tests {
		test := test
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := ResolveProjectIdentity(context.Background(), reader, test.authorityID, test.projectName); !errors.Is(err, ErrProjectIdentityUnavailable) {
				t.Fatalf("expected ErrProjectIdentityUnavailable, got %v", err)
			}
		})
	}
}
