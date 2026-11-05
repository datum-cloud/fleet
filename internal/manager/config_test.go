package manager

import (
	"strings"
	"testing"

	"k8s.io/client-go/rest"
)

func TestProjectRESTConfigScopesCopyToExactRoute(t *testing.T) {
	root := &rest.Config{Host: "https://milo.example.test/"}
	project, err := ProjectRESTConfig(root, "datum-cloud")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://milo.example.test/apis/resourcemanager.miloapis.com/v1alpha1/projects/datum-cloud/control-plane"
	if project.Host != want {
		t.Fatalf("Host = %q, want %q", project.Host, want)
	}
	if root.Host != "https://milo.example.test/" || project == root {
		t.Fatal("root config was mutated instead of copied")
	}
}

func TestProjectRESTConfigRejectsUnscopedOrAmbiguousRoute(t *testing.T) {
	for _, route := range []string{"", "project/other", "project?other", "project#other"} {
		_, err := ProjectRESTConfig(&rest.Config{Host: "https://milo.example.test"}, route)
		if err == nil || !strings.Contains(err.Error(), ErrEventWiringUnavailable.Error()) {
			t.Fatalf("route %q err = %v, want event wiring error", route, err)
		}
	}
	if _, err := ProjectRESTConfig(nil, "datum-cloud"); err == nil {
		t.Fatal("nil root config was accepted")
	}
}
