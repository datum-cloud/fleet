package main

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func served(t *testing.T, handler http.Handler, path string) int {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	return recorder.Code
}

var ok = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

func TestWithholdIsOffWithoutTheLabVariable(t *testing.T) {
	t.Setenv(labWithholdEnv, "")
	if err := armWithhold("arm"); err == nil {
		t.Fatal("the hook armed without its lab variable")
	}
	if code := served(t, withhold(ok), "/pxe/52-54-00-77-01-0a/boot.ipxe"); code != http.StatusOK {
		t.Fatalf("an unconfigured hook changed the response: %d", code)
	}
}

func TestAnArmedWithholdDeniesEveryBootRequest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "withhold")
	t.Setenv(labWithholdEnv, path)
	handler := withhold(ok)
	paths := []string{"/pxe/boot.ipxe", "/pxe/52-54-00-77-01-0a/boot.ipxe", "/0123456789abcdef/token/kernel", "/0123456789abcdef/token/installer.gz"}
	for _, p := range paths {
		if code := served(t, handler, p); code != http.StatusOK {
			t.Fatalf("an unarmed hook denied %s: %d", p, code)
		}
	}
	if err := armWithhold("arm"); err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		if code := served(t, handler, p); code != http.StatusForbidden {
			t.Fatalf("an armed hook served %s: %d", p, code)
		}
	}
	if data, err := os.ReadFile(path + deniedSuffix); err != nil || strings.Count(string(data), "\n") != len(paths) {
		t.Fatalf("denials counted = %q, %v, want %d", data, err, len(paths))
	}
	// Arming again starts a new count.
	if err := armWithhold("arm"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + deniedSuffix); !os.IsNotExist(err) {
		t.Fatalf("arming kept the earlier count: %v", err)
	}
	if err := armWithhold("clear"); err != nil {
		t.Fatal(err)
	}
	if err := armWithhold("clear"); err != nil {
		t.Fatalf("clearing a cleared hook: %v", err)
	}
	if code := served(t, handler, paths[1]); code != http.StatusOK {
		t.Fatalf("a cleared hook still denies: %d", code)
	}
}

// TestOnlyTheLabSiteSourceSetsTheWithholdHook keeps the hook out of
// production. The primary control is the binary: only this acceptance
// binary reads the variable, and no production image runs it. The test adds
// that only the boot service of the local multi-plane site's Flux source
// sets the variable, that no other configuration includes or points a Flux
// path at that source, and that no other package names the variable.
func TestOnlyTheLabSiteSourceSetsTheWithholdHook(t *testing.T) {
	root := filepath.Join("..", "..")
	var setters []string
	err := filepath.WalkDir(filepath.Join(root, "config"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		text := string(data)
		if strings.Contains(text, labWithholdEnv) {
			setters = append(setters, rel)
		}
		lab := strings.HasPrefix(rel, filepath.Join("config", "site")+string(filepath.Separator)) || strings.HasPrefix(rel, filepath.Join("config", "multiplane")+string(filepath.Separator))
		if !lab && filepath.Ext(path) != ".md" && strings.Contains(text, "config/site") {
			t.Errorf("%s points at the lab site source", rel)
		}
		if !lab && filepath.Base(path) == "kustomization.yaml" {
			for _, line := range strings.Split(text, "\n") {
				entry := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
				if strings.HasPrefix(entry, "#") {
					continue
				}
				if strings.Contains(entry, "/site") && !strings.Contains(entry, "/site-") || strings.Contains(entry, "multiplane") {
					t.Errorf("%s references the lab site source: %s", rel, entry)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("config", "site", "services", "services.yaml")
	if len(setters) != 1 || setters[0] != want {
		t.Fatalf("the hook is set in %v, want only %s", setters, want)
	}
	services, err := os.ReadFile(filepath.Join(root, want))
	if err != nil {
		t.Fatal(err)
	}
	boot := strings.Index(string(services), "kind: Deployment\nmetadata:\n  name: fleet-site-boot\n")
	next := strings.Index(string(services)[boot+1:], "\n---")
	if boot < 0 || next < 0 || !strings.Contains(string(services)[boot:boot+1+next], labWithholdEnv) {
		t.Fatal("the hook is set outside the boot service's Deployment")
	}
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() || filepath.Dir(path) == "." {
				return err
			}
			if abs, _ := filepath.Abs(filepath.Dir(path)); abs == mustAbs(t, ".") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err == nil && strings.Contains(string(data), labWithholdEnv) {
				t.Errorf("%s names the lab hook", path)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func mustAbs(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// TestNothingElseInTheRepositorySetsTheWithholdHook scans every tracked
// kind of file outside the docs: only this package, the lab boot service's
// one env entry, and the lab steps that arm and clear it may name the hook.
func TestNothingElseInTheRepositorySetsTheWithholdHook(t *testing.T) {
	root := mustAbs(t, filepath.Join("..", ".."))
	allowed := map[string]int{
		filepath.Join("config", "site", "services", "services.yaml"): 1,
		filepath.Join("hack", "multiplane", "steps.sh"):              -1,
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if entry.IsDir() {
			switch rel {
			case ".git", "docs", filepath.Join("cmd", "fleet-multiplane"):
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(rel, ".md") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		count := strings.Count(string(data), labWithholdEnv)
		want, ok := allowed[rel]
		switch {
		case !ok && (count > 0 || strings.Contains(string(data), "boot-withhold")):
			t.Errorf("%s names the lab boot hook", rel)
		case ok && want >= 0 && count != want:
			t.Errorf("%s sets the lab boot hook %d times, want %d", rel, count, want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
