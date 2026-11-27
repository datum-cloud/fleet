// Package site runs admitted Device operations at the site that can reach the
// server, as proposed in datum-cloud/fleet#174 at d4d17f9.
//
// The framework owns everything a kind must not implement itself: the
// per-Device fence, the per-Device journal, stopping older workers, the
// credential lookup, the TLS pin and identity checks, the deadline, and the
// result that carries the fence. A kind supplies only its pre-effect checks,
// its journaled steps, how it observes after a lost response, and its typed
// evidence.
//
// Increment B is inert: nothing delivers assignments to it until the cutover
// in Increment C.
package site

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

var (
	// ErrStaleFence means a fence is below the Device's stored fence. The
	// site refuses the assignment, the step or the result.
	ErrStaleFence = errors.New("site: fence is below the Device's stored fence")
	// ErrStepReplayed means a step was journaled before and its outcome
	// must be observed, not sent again.
	ErrStepReplayed = errors.New("site: step was journaled before; observe it instead of sending again")
)

// StepState is how far a journaled step got.
type StepState string

const (
	// StepIntended is journaled before the step is sent. Finding it after
	// a crash means the step may or may not have reached the server.
	StepIntended StepState = "Intended"
	StepDone     StepState = "Done"
)

// Entry is one journaled step.
type Entry struct {
	Operation string    `json:"operation"`
	Fence     int64     `json:"fence"`
	Step      string    `json:"step"`
	State     StepState `json:"state"`
	At        time.Time `json:"at"`
	// Payload is the kind's bounded record of the step, such as the send
	// time or the power state before it. It is journaled before the step is
	// sent and read back when the step is observed after a crash.
	Payload json.RawMessage `json:"payload,omitempty"`
}

// maxPayload bounds a journaled step's payload.
const maxPayload = 1024

// deviceState is the durable state of one Device at this site.
type deviceState struct {
	Device string `json:"device"`
	// Fence is the highest fence the site has seen for the Device.
	Fence int64 `json:"fence"`
	// Stopped is the highest fence for which every older worker is known to
	// have stopped. The coordinator reads it as the site's fence report.
	Stopped int64   `json:"stopped"`
	Journal []Entry `json:"journal,omitempty"`
}

// Store is the site's per-Device fence store and journal. It must live on
// durable storage, the site's retained volume, so the site still refuses old
// fences after a restart. Each Device has its own directory, lock and state
// file, so work on one Device never waits for another.
type Store struct {
	root string
	now  func() time.Time
}

// OpenStore opens a store rooted at an existing, owner-only directory.
func OpenStore(root string, now func() time.Time) (*Store, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("site: the store root must be an owner-only directory")
	}
	if now == nil {
		now = time.Now
	}
	return &Store{root: root, now: now}, nil
}

// deviceDir is the Device's directory. Device names a Device as
// "<namespace>/<name>"; the directory is a digest so no name can escape root.
func (s *Store) deviceDir(device string) string {
	sum := sha256.Sum256([]byte(device))
	return filepath.Join(s.root, "devices", hex.EncodeToString(sum[:16]))
}

// locked runs change on the Device's state under its exclusive lock and
// saves the result atomically when change returns save=true.
func (s *Store) locked(device string, change func(*deviceState) (bool, error)) error {
	if device == "" {
		return errors.New("site: a Device is required")
	}
	dir := s.deviceDir(device)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "state.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	state := &deviceState{Device: device}
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	switch {
	case os.IsNotExist(err):
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(data, state); err != nil || state.Device != device {
			return errors.New("site: the Device's state file is unreadable; refusing to guess its fence")
		}
	}
	save, err := change(state)
	if err != nil || !save {
		return err
	}
	return writeAtomic(dir, "state.json", state)
}

func writeAtomic(dir, name string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, name+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), filepath.Join(dir, name)); err != nil {
		return err
	}
	parent, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}

// Raise stores fence as the Device's highest fence. A fence below the stored
// one is refused. An equal fence is accepted again: delivery is replayed from
// custody after a crash.
func (s *Store) Raise(device string, fence int64) error {
	if fence < 1 {
		return errors.New("site: a fence is at least 1")
	}
	return s.locked(device, func(state *deviceState) (bool, error) {
		if fence < state.Fence {
			return false, ErrStaleFence
		}
		if fence == state.Fence {
			return false, nil
		}
		state.Fence = fence
		return true, nil
	})
}

// Current returns the Device's stored fence and the fence for which older
// workers are confirmed stopped.
func (s *Store) Current(device string) (fence, stopped int64, err error) {
	err = s.locked(device, func(state *deviceState) (bool, error) {
		fence, stopped = state.Fence, state.Stopped
		return false, nil
	})
	return fence, stopped, err
}

// Peek reads a Device's fences without its lock, for readers on a read-only
// mount such as the boot server. Every write replaces the state file
// atomically, so a reader sees one complete state or none.
func (s *Store) Peek(device string) (fence, stopped int64, err error) {
	data, err := os.ReadFile(filepath.Join(s.deviceDir(device), "state.json"))
	if os.IsNotExist(err) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	var state deviceState
	if err := json.Unmarshal(data, &state); err != nil || state.Device != device {
		return 0, 0, errors.New("site: the Device's state is unreadable")
	}
	return state.Fence, state.Stopped, nil
}

// ConfirmStopped records that no worker below fence still runs for the
// Device. It is refused if fence is no longer the stored fence.
func (s *Store) ConfirmStopped(device string, fence int64) error {
	return s.locked(device, func(state *deviceState) (bool, error) {
		if fence != state.Fence {
			return false, ErrStaleFence
		}
		if state.Stopped >= fence {
			return false, nil
		}
		state.Stopped = fence
		return true, nil
	})
}

// Intend journals a step before it is sent. It refuses a fence below the
// stored fence. A step already journaled for the operation returns
// ErrStepReplayed if it was only intended, so the caller observes it, and
// its state if it was done.
func (s *Store) Intend(device string, fence int64, operation, step string) (StepState, error) {
	prior, _, err := s.IntendWithPayload(device, fence, operation, step, nil)
	return prior, err
}

// IntendWithPayload is Intend with a payload. For a step journaled before, it
// returns the payload recorded then.
func (s *Store) IntendWithPayload(device string, fence int64, operation, step string, payload json.RawMessage) (StepState, json.RawMessage, error) {
	if len(payload) > maxPayload {
		return "", nil, fmt.Errorf("site: a step payload is at most %d bytes", maxPayload)
	}
	var prior StepState
	var recorded json.RawMessage
	err := s.locked(device, func(state *deviceState) (bool, error) {
		if fence < state.Fence {
			return false, ErrStaleFence
		}
		if fence > state.Fence {
			return false, fmt.Errorf("site: fence %d was never raised", fence)
		}
		for _, entry := range state.Journal {
			if entry.Operation == operation && entry.Step == step {
				prior, recorded = entry.State, entry.Payload
				if entry.State == StepIntended {
					return false, ErrStepReplayed
				}
				return false, nil
			}
		}
		state.Journal = append(state.Journal, Entry{Operation: operation, Fence: fence, Step: step, State: StepIntended, At: s.now().UTC(), Payload: payload})
		recorded = payload
		return true, nil
	})
	return prior, recorded, err
}

// Done marks a journaled step done. It refuses a fence below the stored
// fence, so an older worker cannot record progress after a newer admission.
func (s *Store) Done(device string, fence int64, operation, step string) error {
	return s.locked(device, func(state *deviceState) (bool, error) {
		if fence < state.Fence {
			return false, ErrStaleFence
		}
		index := slices.IndexFunc(state.Journal, func(entry Entry) bool { return entry.Operation == operation && entry.Step == step })
		if index < 0 {
			return false, errors.New("site: a step must be journaled before it is done")
		}
		if state.Journal[index].State == StepDone {
			return false, nil
		}
		state.Journal[index].State = StepDone
		state.Journal[index].At = s.now().UTC()
		return true, nil
	})
}

// Journal returns the Device's journal.
func (s *Store) Journal(device string) ([]Entry, error) {
	var out []Entry
	err := s.locked(device, func(state *deviceState) (bool, error) {
		out = slices.Clone(state.Journal)
		return false, nil
	})
	return out, err
}

// runLock is held by the one worker running for a Device. A worker that
// raises the fence waits for it, so an older worker has stopped before the
// newer one starts. A worker that dies releases it with its process.
type runLock struct{ file *os.File }

func (s *Store) tryRunLock(device string) (*runLock, error) {
	dir := s.deviceDir(device)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(dir, "run.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, nil
		}
		return nil, err
	}
	return &runLock{file: file}, nil
}

func (l *runLock) release() { l.file.Close() }

// PutRecord stores a kind's per-operation record for a Device, such as its
// boot admission, beside the journal. A record below the stored fence is
// refused.
func (s *Store) PutRecord(device string, fence int64, collection, name string, value any) error {
	if !validRecordName(collection) || !validRecordName(name) {
		return errors.New("site: invalid record name")
	}
	return s.locked(device, func(state *deviceState) (bool, error) {
		if fence < state.Fence {
			return false, ErrStaleFence
		}
		dir := filepath.Join(s.deviceDir(device), collection)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return false, err
		}
		return false, writeAtomic(dir, name+".json", value)
	})
}

// DeleteRecord removes a kind's record, such as a rendered config medium
// once its operation ended. Removing never needs the fence: a worker a newer
// fence stopped still deletes what it rendered.
func (s *Store) DeleteRecord(device, collection, name string) error {
	if !validRecordName(collection) || !validRecordName(name) {
		return errors.New("site: invalid record name")
	}
	return s.locked(device, func(*deviceState) (bool, error) {
		err := os.Remove(filepath.Join(s.deviceDir(device), collection, name+".json"))
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	})
}

// GetRecord reads a kind's record. It returns false if there is none.
func (s *Store) GetRecord(device, collection, name string, into any) (bool, error) {
	if !validRecordName(collection) || !validRecordName(name) {
		return false, errors.New("site: invalid record name")
	}
	data, err := os.ReadFile(filepath.Join(s.deviceDir(device), collection, name+".json"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(data, into)
}

func validRecordName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.') || name == "." || name == ".." {
			return false
		}
	}
	return true
}

// Record is one stored per-operation record.
type Record struct {
	Device string
	Name   string
	Data   []byte
}

// Records returns every Device's records in a collection.
func (s *Store) Records(collection string) ([]Record, error) {
	if !validRecordName(collection) {
		return nil, errors.New("site: invalid record name")
	}
	dirs, err := filepath.Glob(filepath.Join(s.root, "devices", "*"))
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, dir := range dirs {
		data, err := os.ReadFile(filepath.Join(dir, "state.json"))
		if err != nil {
			continue
		}
		var state deviceState
		if json.Unmarshal(data, &state) != nil {
			continue
		}
		paths, err := filepath.Glob(filepath.Join(dir, collection, "*.json"))
		if err != nil {
			return nil, err
		}
		slices.Sort(paths)
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			out = append(out, Record{Device: state.Device, Name: strings.TrimSuffix(filepath.Base(path), ".json"), Data: data})
		}
	}
	return out, nil
}
