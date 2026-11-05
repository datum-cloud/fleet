//go:build ignore

package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

const (
	marker              = "fleet.datumapis.com/standalone-only"
	localProjectStandIn = "fleet.datumapis.com/local-project-stand-in"
	sharedNamespace     = "fleet.datumapis.com/shared-with-standalone"
)

type kustomization struct {
	Resources                   []string       `json:"resources"`
	Components                  []string       `json:"components"`
	Bases                       []string       `json:"bases"`
	Generators                  []string       `json:"generators"`
	HelmCharts                  []any          `json:"helmCharts"`
	HelmChartInflationGenerator map[string]any `json:"helmChartInflationGenerator"`
}

type validator struct {
	configRoot, standaloneRoot, simulatorRoot string
	standaloneSecrets                         map[string]struct{}
	standaloneRBAC                            map[[32]byte]struct{}
	standaloneNamespaces                      map[string]bool
}

func main() {
	if len(os.Args) < 2 || len(os.Args) > 3 {
		fatalf("usage: %s CONFIG_ROOT [STANDALONE_ROOT]", os.Args[0])
	}
	configRoot, err := canonicalDir(os.Args[1])
	check(err)
	standalone := ""
	if len(os.Args) == 3 {
		standalone = os.Args[2]
	}
	if standalone == "" {
		standalone = filepath.Join(configRoot, "standalone")
	}
	standaloneRoot, err := canonicalDir(standalone)
	check(err)
	simulatorRoot := filepath.Join(configRoot, "base", "simulator")
	if _, err := os.Stat(simulatorRoot); err == nil {
		simulatorRoot, err = canonicalDir(simulatorRoot)
		check(err)
	}

	v := &validator{configRoot: configRoot, standaloneRoot: standaloneRoot, simulatorRoot: simulatorRoot}
	standaloneObjects, err := build(standaloneRoot)
	check(err)
	if len(standaloneObjects) == 0 {
		fatalf("standalone render is empty: %s", standaloneRoot)
	}
	for _, object := range standaloneObjects {
		value, found, err := unstructured.NestedString(object.Object, "metadata", "annotations", marker)
		if err != nil || !found || value != "true" {
			fatalf("standalone-rendered %s %q must have %s: \"true\"", object.GetKind(), object.GetName(), marker)
		}
	}
	v.indexStandalone(standaloneObjects)

	entries, err := discover(configRoot)
	check(err)
	if len(entries) == 0 {
		fatalf("no Kustomize entry points found below %s", configRoot)
	}
	checked := 0
	for _, entry := range entries {
		root := filepath.Dir(entry)
		if v.definitionRoot(root) {
			continue
		}
		checked++
		if err := v.checkGraph(root); err != nil {
			fatalf("%s: %v", root, err)
		}
		objects, err := build(root)
		if err != nil {
			fatalf("build %s: %v", root, err)
		}
		for _, object := range objects {
			if err := v.checkObject(object); err != nil {
				fatalf("render %s: %v", root, err)
			}
		}
	}
	if checked == 0 {
		fatalf("no non-standalone Kustomize entry point was validated")
	}
}

func (v *validator) definitionRoot(path string) bool {
	return path == v.standaloneRoot || path == v.simulatorRoot
}

func (v *validator) checkGraph(root string) error {
	seen := map[string]bool{}
	var visit func(string) error
	visit = func(path string) error {
		resolved, err := canonical(path)
		if err != nil {
			return err
		}
		if within(resolved, v.standaloneRoot) || within(resolved, v.simulatorRoot) {
			return fmt.Errorf("source graph reaches standalone-only path %s", resolved)
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return nil
		}
		kfile, err := findKustomization(resolved)
		if err != nil {
			return err
		}
		if seen[kfile] {
			return nil
		}
		seen[kfile] = true
		data, err := os.ReadFile(kfile)
		if err != nil {
			return err
		}
		var k kustomization
		if err := yaml.Unmarshal(data, &k); err != nil {
			return fmt.Errorf("parse %s: %w", kfile, err)
		}
		if len(k.HelmCharts) != 0 || len(k.HelmChartInflationGenerator) != 0 {
			return fmt.Errorf("Helm resource generation is not auditable in %s", kfile)
		}
		children := append([]string{}, k.Resources...)
		children = append(children, k.Components...)
		children = append(children, k.Bases...)
		children = append(children, k.Generators...)
		for _, child := range children {
			childPath, err := v.localReference(resolved, child)
			if err != nil {
				return err
			}
			if err := visit(childPath); err != nil {
				return err
			}
		}
		return nil
	}
	return visit(root)
}

func (v *validator) localReference(parent, reference string) (string, error) {
	if reference == "" || filepath.IsAbs(reference) {
		return "", fmt.Errorf("resource-producing reference is not a plain local relative path: %q", reference)
	}
	if strings.Contains(reference, "//") || strings.ContainsAny(reference, ":?#@%&=\\") {
		return "", fmt.Errorf("resource-producing reference is not a plain local relative path: %q", reference)
	}
	lower := strings.ToLower(reference)
	if strings.Contains(lower, ".git/") || strings.HasSuffix(lower, ".git") ||
		strings.Contains(lower, "/_git/") || strings.HasPrefix(lower, "_git/") ||
		strings.HasPrefix(lower, "github.com/") || strings.HasPrefix(lower, "gitlab.com/") ||
		strings.HasPrefix(lower, "bitbucket.org/") {
		return "", fmt.Errorf("resource-producing reference is not a plain local relative path: %q", reference)
	}
	firstSegment, _, _ := strings.Cut(lower, "/")
	if firstSegment != "." && firstSegment != ".." && strings.Contains(firstSegment, ".") && strings.Count(lower, "/") >= 2 {
		return "", fmt.Errorf("resource-producing reference is not a plain local relative path: %q", reference)
	}
	for _, char := range reference {
		if unicode.IsLetter(char) || unicode.IsNumber(char) || strings.ContainsRune("/._- ", char) {
			continue
		}
		return "", fmt.Errorf("resource-producing reference is not a plain local relative path: %q", reference)
	}

	// Check the cleaned lexical path before touching the filesystem. This keeps
	// traversal and local shadows of repo syntax outside the audited tree.
	lexical := filepath.Clean(filepath.Join(parent, filepath.FromSlash(reference)))
	if !within(lexical, v.configRoot) {
		return "", fmt.Errorf("resource-producing reference escapes audited config tree: %q", reference)
	}
	resolved, err := canonical(lexical)
	if err != nil {
		return "", err
	}
	if !within(resolved, v.configRoot) {
		return "", fmt.Errorf("resource-producing reference resolves outside audited config tree: %q", reference)
	}
	return resolved, nil
}

func (v *validator) indexStandalone(objects []*unstructured.Unstructured) {
	v.standaloneSecrets = map[string]struct{}{}
	v.standaloneRBAC = map[[32]byte]struct{}{}
	v.standaloneNamespaces = map[string]bool{}
	for _, object := range objects {
		if isCoreNamespace(object) {
			v.standaloneNamespaces[object.GetName()] = object.GetAnnotations()[sharedNamespace] == "true"
		}
		if isCoreSecret(object) {
			for _, value := range secretValues(object) {
				v.standaloneSecrets[value] = struct{}{}
			}
		}
		if isRBAC(object) {
			v.standaloneRBAC[rbacSignature(object)] = struct{}{}
		}
	}
}

func (v *validator) checkObject(object *unstructured.Unstructured) error {
	if annotations, found, _ := unstructured.NestedMap(object.Object, "metadata", "annotations"); found {
		if value, marked := annotations[marker]; marked {
			return fmt.Errorf("standalone marker %q on %s %q", value, object.GetKind(), object.GetName())
		}
	}
	if isCoreNamespace(object) {
		if object.GetLabels()[localProjectStandIn] == "true" {
			return fmt.Errorf("Namespace %q carries standalone local-project stand-in identity", object.GetName())
		}
		if shared, found := v.standaloneNamespaces[object.GetName()]; found &&
			(!shared || object.GetAnnotations()[sharedNamespace] != "true") {
			return fmt.Errorf("Namespace %q duplicates a standalone-only namespace name", object.GetName())
		}
	}
	if isCoreSecret(object) {
		for _, value := range secretValues(object) {
			for standaloneValue := range v.standaloneSecrets {
				if strings.Contains(value, standaloneValue) {
					return fmt.Errorf("Secret %q contains standalone-only secret material", object.GetName())
				}
			}
		}
	}
	if isRBAC(object) {
		if _, found := v.standaloneRBAC[rbacSignature(object)]; found {
			return fmt.Errorf("%s %q duplicates standalone-only RBAC semantics", object.GetKind(), object.GetName())
		}
	}
	return nil
}

func isCoreNamespace(object *unstructured.Unstructured) bool {
	return object.GetAPIVersion() == "v1" && object.GetKind() == "Namespace"
}

func isCoreSecret(object *unstructured.Unstructured) bool {
	return object.GetAPIVersion() == "v1" && object.GetKind() == "Secret"
}

func secretValues(object *unstructured.Unstructured) []string {
	var values []string
	if data, found, _ := unstructured.NestedStringMap(object.Object, "data"); found {
		for _, encoded := range data {
			if decoded, err := base64.StdEncoding.DecodeString(encoded); err == nil {
				if value := string(decoded); value != "" {
					values = append(values, value)
				}
			}
		}
	}
	if data, found, _ := unstructured.NestedStringMap(object.Object, "stringData"); found {
		for _, value := range data {
			if value != "" {
				values = append(values, value)
			}
		}
	}
	return values
}

func isRBAC(object *unstructured.Unstructured) bool {
	if object.GetAPIVersion() != "rbac.authorization.k8s.io/v1" {
		return false
	}
	switch object.GetKind() {
	case "Role", "ClusterRole", "RoleBinding", "ClusterRoleBinding":
		return true
	default:
		return false
	}
}

func rbacSignature(object *unstructured.Unstructured) [32]byte {
	records := []string{object.GetAPIVersion() + "\x00" + object.GetKind()}
	switch object.GetKind() {
	case "Role", "ClusterRole":
		records = append(records, canonicalPermissions(object)...)
	case "RoleBinding", "ClusterRoleBinding":
		records = append(records, canonicalBinding(object)...)
	}
	sort.Strings(records)
	records = deduplicate(records)
	return sha256.Sum256([]byte(strings.Join(records, "\x00")))
}

func canonicalPermissions(object *unstructured.Unstructured) []string {
	rules, _, _ := unstructured.NestedSlice(object.Object, "rules")
	var records []string
	for _, rawRule := range rules {
		rule, ok := rawRule.(map[string]any)
		if !ok {
			continue
		}
		verbs := stringsFrom(rule["verbs"])
		groups := stringsFrom(rule["apiGroups"])
		resources := stringsFrom(rule["resources"])
		names := stringsFrom(rule["resourceNames"])
		if len(names) == 0 {
			names = []string{"<all-identities>"}
		} else {
			names = []string{"<identity>"}
		}
		for _, verb := range verbs {
			for _, group := range groups {
				for _, resource := range resources {
					for _, name := range names {
						records = append(records, strings.Join([]string{"resource", verb, group, resource, name}, "\x01"))
					}
				}
			}
			for _, url := range stringsFrom(rule["nonResourceURLs"]) {
				records = append(records, strings.Join([]string{"non-resource", verb, url}, "\x01"))
			}
		}
	}
	sort.Strings(records)
	return deduplicate(records)
}

func canonicalBinding(object *unstructured.Unstructured) []string {
	var records []string
	if roleRef, found, _ := unstructured.NestedMap(object.Object, "roleRef"); found {
		records = append(records, strings.Join([]string{"role-ref", stringValue(roleRef["apiGroup"]), stringValue(roleRef["kind"]), stringValue(roleRef["name"])}, "\x01"))
	}
	if subjects, found, _ := unstructured.NestedSlice(object.Object, "subjects"); found {
		for _, rawSubject := range subjects {
			subject, ok := rawSubject.(map[string]any)
			if !ok {
				continue
			}
			records = append(records, strings.Join([]string{"subject", stringValue(subject["apiGroup"]), stringValue(subject["kind"]), stringValue(subject["name"]), stringValue(subject["namespace"])}, "\x01"))
		}
	}
	sort.Strings(records)
	return deduplicate(records)
}

func stringsFrom(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	values := make([]string, 0, len(items))
	for _, item := range items {
		if value, ok := item.(string); ok {
			values = append(values, value)
		}
	}
	sort.Strings(values)
	return deduplicate(values)
}

func stringValue(value any) string {
	valueString, _ := value.(string)
	return valueString
}

func deduplicate(values []string) []string {
	if len(values) == 0 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}

func build(root string) ([]*unstructured.Unstructured, error) {
	command := exec.Command("kustomize", "build", root)
	output, err := command.CombinedOutput()
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
		if !entry.IsDir() && isKustomizationName(entry.Name()) {
			entries = append(entries, path)
		}
		return nil
	})
	sort.Strings(entries)
	return entries, err
}

func findKustomization(dir string) (string, error) {
	for _, name := range []string{"kustomization.yaml", "kustomization.yml", "Kustomization"} {
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}
	return "", fmt.Errorf("resource directory has no Kustomization: %s", dir)
}

func isKustomizationName(name string) bool {
	return name == "kustomization.yaml" || name == "kustomization.yml" || name == "Kustomization"
}

func canonical(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

func canonicalDir(path string) (string, error) {
	resolved, err := canonical(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", path, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory: %s", resolved)
	}
	return resolved, nil
}

func within(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func check(err error) {
	if err != nil {
		fatalf("%v", err)
	}
}
func fatalf(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...); os.Exit(1) }
