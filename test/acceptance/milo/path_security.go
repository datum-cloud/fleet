package miloacceptance

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// rejectSymlinkComponents is the fail-closed path boundary for the current
// non-concurrent harness. Every existing component must be real, and every
// parent must be a directory. A future concurrent live executor must replace
// path traversal with held directory descriptors and openat-style no-follow
// operations; a check followed by path reuse is not a general race-safe API.
func rejectSymlinkComponents(path string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("path must be canonical and absolute")
	}
	volume := filepath.VolumeName(path)
	root := volume + string(os.PathSeparator)
	relative := strings.TrimPrefix(path, root)
	current := root
	if relative == "" {
		relative = "."
	}
	parts := strings.Split(relative, string(os.PathSeparator))
	for index, part := range parts {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return errors.New("path component is unavailable")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("path contains a symlink component")
		}
		if index < len(parts)-1 && !info.IsDir() {
			return errors.New("path parent component is not a directory")
		}
	}
	return nil
}
