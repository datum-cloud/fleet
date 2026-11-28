//go:build ignore

// Renders every Kustomize entry point below a config root and fails when a
// Secret that is not a BMC credential Secret lands in a BMC credential
// namespace. Kubernetes RBAC cannot narrow BMC discovery's Secret Role to the
// labelled credential Secrets, so discovery reads every Secret in its
// namespace; this check keeps that namespace holding credentials only.
package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

const (
	// Discovery's ServiceAccount and Secret Role, from config/base/bmc-discovery.
	discoveryAccount = "fleet-bmc-discovery"
	discoveryRole    = "fleet-bmc-discovery-credentials"
	// discoveryKubeconfig is the Milo kubeconfig Secret discovery mounts.
	discoveryKubeconfig = "fleet-bmc-discovery-milo-kubeconfig"
	// namespaceLabel marks a Namespace that holds BMC credential Secrets
	// under discovery's contract but does not run discovery itself.
	namespaceLabel = "fleet.datumapis.com/bmc-credential-namespace"

	// The credential Secret contract in internal/bmcdiscovery.
	credentialLabel     = "fleet.datumapis.com/bmc-credential"
	projectAnnotation   = "fleet.datumapis.com/device-project"
	namespaceAnnotation = "fleet.datumapis.com/device-namespace"
	nameAnnotation      = "fleet.datumapis.com/device-name"
)

func main() {
	if len(os.Args) != 2 {
		fatalf("usage: %s CONFIG_ROOT", os.Args[0])
	}
	root, err := filepath.Abs(os.Args[1])
	check(err)
	entries, err := discover(root)
	check(err)
	if len(entries) == 0 {
		fatalf("no Kustomize entry points found below %s", root)
	}
	// A namespace marked in one root is a credential namespace in every root,
	// since Flux may apply the Namespace and the objects in it from different
	// Kustomizations. The unset namespace is the deploying repository's only
	// within a render that leaves discovery's namespace unset.
	type render struct {
		relative string
		objects  []*unstructured.Unstructured
		local    namespaces
	}
	var renders []render
	global := namespaces{credential: map[string]bool{}, discovery: map[string]bool{}}
	var covered []string
	for _, entry := range entries {
		dir := filepath.Dir(entry)
		relative, err := filepath.Rel(root, dir)
		check(err)
		objects, err := build(dir)
		if err != nil {
			fatalf("build %s: %v", relative, err)
		}
		local := credentialNamespaces(objects)
		if len(local.credential) != 0 {
			covered = append(covered, filepath.ToSlash(relative))
		}
		for namespace := range local.credential {
			if namespace != "" {
				global.credential[namespace] = true
			}
		}
		for namespace := range local.discovery {
			if namespace != "" {
				global.discovery[namespace] = true
			}
		}
		renders = append(renders, render{relative: relative, objects: objects, local: local})
	}
	var failures []string
	for _, r := range renders {
		for _, object := range r.objects {
			namespace := object.GetNamespace()
			if !isCoreKind(object, "Secret") || !(global.credential[namespace] || namespace == "" && r.local.credential[""]) {
				continue
			}
			// Discovery mounts its own Milo kubeconfig, so the Role exposes
			// nothing new by letting it read that Secret beside discovery.
			if object.GetName() == discoveryKubeconfig && (global.discovery[namespace] || namespace == "" && r.local.discovery[""]) {
				continue
			}
			if reason := notCredential(object); reason != "" {
				failures = append(failures, fmt.Sprintf("%s: Secret %q in BMC credential namespace %s %s", filepath.ToSlash(r.relative), object.GetName(), describe(namespace), reason))
			}
		}
	}
	if len(failures) != 0 {
		sort.Strings(failures)
		for _, failure := range failures {
			fmt.Fprintln(os.Stderr, failure)
		}
		os.Exit(1)
	}
	if len(covered) == 0 {
		fatalf("no Kustomize entry point below %s renders a BMC credential namespace", root)
	}
	fmt.Printf("%d of %d Kustomize entry points render a BMC credential namespace, each holding only credential Secrets: %s\n", len(covered), len(entries), strings.Join(covered, ", "))
}

// namespaces are the credential namespaces of a render, and those of them
// that run discovery.
type namespaces struct {
	credential, discovery map[string]bool
}

// credentialNamespaces is every namespace in one render that discovery reads
// Secrets from, or that is marked as holding credential Secrets. The empty
// namespace is the deploying repository's namespace for a render that leaves
// discovery's namespace unset.
func credentialNamespaces(objects []*unstructured.Unstructured) namespaces {
	found := namespaces{credential: map[string]bool{}, discovery: map[string]bool{}}
	for _, object := range objects {
		switch {
		case isCoreKind(object, "ServiceAccount") && object.GetName() == discoveryAccount,
			object.GetAPIVersion() == "rbac.authorization.k8s.io/v1" && object.GetKind() == "Role" && object.GetName() == discoveryRole:
			found.credential[object.GetNamespace()] = true
			found.discovery[object.GetNamespace()] = true
		case isCoreKind(object, "Secret") && object.GetLabels()[credentialLabel] == "true":
			found.credential[object.GetNamespace()] = true
		case isCoreKind(object, "Namespace") && object.GetLabels()[namespaceLabel] == "true":
			found.credential[object.GetName()] = true
		}
	}
	return found
}

// notCredential says why a Secret does not meet the credential contract, or
// returns "" when it does.
func notCredential(secret *unstructured.Unstructured) string {
	if secret.GetLabels()[credentialLabel] != "true" {
		return fmt.Sprintf("is not a credential Secret: it lacks label %s: \"true\"", credentialLabel)
	}
	for _, annotation := range []string{projectAnnotation, namespaceAnnotation, nameAnnotation} {
		if secret.GetAnnotations()[annotation] == "" {
			return fmt.Sprintf("names no Device: it lacks annotation %s", annotation)
		}
	}
	return ""
}

func describe(namespace string) string {
	if namespace == "" {
		return "(set by the deploying repository)"
	}
	return namespace
}

func isCoreKind(object *unstructured.Unstructured, kind string) bool {
	return object.GetAPIVersion() == "v1" && object.GetKind() == kind
}

func build(root string) ([]*unstructured.Unstructured, error) {
	output, err := exec.Command("kustomize", "build", root).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("kustomize build: %w: %s", err, bytes.TrimSpace(output))
	}
	reader := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(output)))
	var objects []*unstructured.Unstructured
	for {
		doc, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}
		var object map[string]any
		if err := yaml.Unmarshal(doc, &object); err != nil {
			return nil, err
		}
		objects = append(objects, &unstructured.Unstructured{Object: object})
	}
	return objects, nil
}

func discover(root string) ([]string, error) {
	var entries []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name := entry.Name(); !entry.IsDir() && (name == "kustomization.yaml" || name == "kustomization.yml" || name == "Kustomization") {
			entries = append(entries, path)
		}
		return nil
	})
	sort.Strings(entries)
	return entries, err
}

func check(err error) {
	if err != nil {
		fatalf("%v", err)
	}
}

func fatalf(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...); os.Exit(1) }
