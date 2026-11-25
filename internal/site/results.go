package site

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"

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

// Put stores a result, named by its operation UID and fence.
func (r *Results) Put(device string, result custody.SiteResult) error {
	dir := r.dir(device)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return writeAtomic(dir, resultName(result), storedResult{Device: device, Result: result})
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
