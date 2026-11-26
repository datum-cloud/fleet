package site

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"go.datum.net/fleet/internal/custody"
)

// Results is the site's copy of each result until the coordinator accepts or
// rejects it. Results live beside the Device's journal.
type Results struct{ store *Store }

func NewResults(store *Store) *Results { return &Results{store: store} }

type storedResult struct {
	Device string             `json:"device"`
	Result custody.SiteResult `json:"result"`
}

func (r *Results) dir(device string) string {
	return filepath.Join(r.store.deviceDir(device), "results")
}

// Put stores a result, named by its operation UID and fence, unless one is
// already stored for them. A stored result is final: a redelivered
// assignment never replaces it, so a success still waiting for delivery
// cannot be lost, and Deliver never deletes a result it did not offer. Put
// returns the result that is stored.
func (r *Results) Put(device string, result custody.SiteResult) (custody.SiteResult, error) {
	dir := r.dir(device)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return result, err
	}
	data, err := json.Marshal(storedResult{Device: device, Result: result})
	if err != nil {
		return result, err
	}
	file, err := os.CreateTemp(dir, "result.tmp-*")
	if err != nil {
		return result, err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return result, err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return result, err
	}
	if err := file.Close(); err != nil {
		return result, err
	}
	// A hard link creates the name only if it is absent, with its whole
	// content, so a reader never sees a partial result.
	if err := os.Link(file.Name(), filepath.Join(dir, resultName(result))); err != nil {
		if os.IsExist(err) {
			stored, found, readErr := r.Get(device, result.Operation.UID, result.Fence)
			if readErr != nil || !found {
				return result, errors.New("site: a stored result is unreadable")
			}
			return stored, nil
		}
		return result, err
	}
	parent, err := os.Open(dir)
	if err != nil {
		return result, err
	}
	defer parent.Close()
	return result, parent.Sync()
}

// Get returns the stored result for an operation and fence.
func (r *Results) Get(device, operation string, fence int64) (custody.SiteResult, bool, error) {
	data, err := os.ReadFile(filepath.Join(r.dir(device), resultName(custody.SiteResult{Operation: ops.OperationReference{UID: operation}, Fence: fence})))
	if os.IsNotExist(err) {
		return custody.SiteResult{}, false, nil
	}
	if err != nil {
		return custody.SiteResult{}, false, err
	}
	var stored storedResult
	if err := json.Unmarshal(data, &stored); err != nil {
		return custody.SiteResult{}, false, errors.New("site: a stored result is unreadable")
	}
	return stored.Result, true, nil
}

func resultName(result custody.SiteResult) string {
	return result.Operation.UID + "-" + jsonInt(result.Fence) + ".json"
}

func jsonInt(value int64) string { data, _ := json.Marshal(value); return string(data) }

// Pending returns every stored result, oldest file name first.
func (r *Results) Pending() ([]storedResult, error) {
	paths, err := filepath.Glob(filepath.Join(r.store.root, "devices", "*", "results", "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var out []storedResult
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var stored storedResult
		if err := json.Unmarshal(data, &stored); err != nil {
			return nil, errors.New("site: a stored result is unreadable")
		}
		out = append(out, stored)
	}
	return out, nil
}

// Deliver offers every stored result to accept, which reports it to the
// coordinator. The site deletes its copy once the coordinator accepts it or
// rejects it for a stale fence. Any other error keeps the copy for the next
// delivery.
func (r *Results) Deliver(ctx context.Context, accept func(context.Context, custody.SiteResult) error) error {
	pending, err := r.Pending()
	if err != nil {
		return err
	}
	var failed error
	for _, stored := range pending {
		err := accept(ctx, stored.Result)
		if err != nil && !errors.Is(err, custody.ErrStaleFence) {
			failed = errors.Join(failed, err)
			continue
		}
		if err := os.Remove(filepath.Join(r.dir(stored.Device), resultName(stored.Result))); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return failed
}
