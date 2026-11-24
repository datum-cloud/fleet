package registration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestPublishedBundlePinsRegistrationImage(t *testing.T) {
	kustomize, err := exec.LookPath("kustomize")
	if err != nil {
		t.Skip("kustomize required")
	}
	root := filepath.Join("..", "..")
	data, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "publish.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct{ With struct{ Images string } }
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	var images []struct{ Path, Name, Tag string }
	if err := yaml.UnmarshalStrict([]byte(workflow.Jobs["publish-kustomize-bundle"].With.Images), &images); err != nil {
		t.Fatal(err)
	}
	temporary := t.TempDir()
	if err := os.CopyFS(filepath.Join(temporary, "config"), os.DirFS(filepath.Join(root, "config"))); err != nil {
		t.Fatal(err)
	}
	const pinnedTag = "sha-0123456789abcdef"
	registrationTransform := false
	for _, image := range images {
		if image.Path == "config/base/registration" {
			registrationTransform = image.Name == "ghcr.io/datum-cloud/fleet" && image.Tag == "${{ needs.publish-container-image.outputs.tag }}"
		}
		command := exec.Command(kustomize, "edit", "set", "image", image.Name+"="+image.Name+":"+pinnedTag)
		command.Dir = filepath.Join(temporary, image.Path)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("bundle transform: %v\n%s", err, output)
		}
	}
	if !registrationTransform {
		t.Fatal("registration is not pinned to the published server build")
	}
	for _, overlay := range []string{"management-plane", "synthetic-staging"} {
		output, err := exec.Command(kustomize, "build", filepath.Join(temporary, "config", "overlays", overlay)).CombinedOutput()
		if err != nil {
			t.Fatalf("render %s: %v\n%s", overlay, err, output)
		}
		found := false
		for _, document := range strings.Split(string(output), "\n---\n") {
			var deployment struct {
				Kind string
				Spec struct {
					Template struct {
						Spec struct {
							Containers []struct {
								Command []string
								Image   string
							}
						}
					}
				}
			}
			if err := yaml.Unmarshal([]byte(document), &deployment); err != nil {
				t.Fatal(err)
			}
			if deployment.Kind != "Deployment" {
				continue
			}
			for _, container := range deployment.Spec.Template.Spec.Containers {
				if len(container.Command) > 0 && container.Command[0] == "/fleet-registration" {
					found = true
					if container.Image != "ghcr.io/datum-cloud/fleet:"+pinnedTag {
						t.Fatalf("%s registration image is %s", overlay, container.Image)
					}
				}
			}
		}
		if found != (overlay == "management-plane") {
			t.Fatalf("%s registration deployment presence violates isolation: %v", overlay, found)
		}
	}
}
