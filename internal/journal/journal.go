// SPDX-License-Identifier: Apache-2.0

// Package journal durably records admitted hardware mutations before effects.
package journal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Outcome string

const (
	Pending   Outcome = "Pending"
	Succeeded Outcome = "Succeeded"
	Failed    Outcome = "Failed"
	Unknown   Outcome = "Unknown"
)

type Operation struct {
	ID                     string          `json:"id"`
	SessionUID             string          `json:"sessionUID"`
	SessionCorrelationID   string          `json:"sessionCorrelationID"`
	OperationCorrelationID string          `json:"operationCorrelationID"`
	ClaimUID               string          `json:"claimUID"`
	MachineUID             string          `json:"machineUID"`
	BindingID              string          `json:"bindingID"`
	BindingEpoch           int64           `json:"bindingEpoch"`
	Method                 string          `json:"method"`
	VirtualTarget          string          `json:"virtualTarget"`
	AcceptedBody           json.RawMessage `json:"acceptedBody"`
	PhysicalTarget         string          `json:"physicalTarget"`
	IdempotencyIdentity    string          `json:"idempotencyIdentity"`
	OperationEpoch         int64           `json:"operationEpoch"`
	ConflictPrecondition   string          `json:"conflictPrecondition"`
	AdapterRoute           string          `json:"adapterRoute"`
	CreatedAt              time.Time       `json:"createdAt"`
	UpdatedAt              time.Time       `json:"updatedAt"`
	Outcome                Outcome         `json:"outcome"`
	Evidence               string          `json:"evidence,omitempty"`
	HTTPStatus             int             `json:"httpStatus,omitempty"`
}

func Identity(sessionUID, method, target string, body []byte) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\n%s\n%s\n", sessionUID, method, target)
	_, _ = h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

type Store struct {
	dir string
	mu  sync.Mutex
}

var machineLocks = struct {
	sync.Mutex
	locks map[string]*sync.Mutex
}{locks: map[string]*sync.Mutex{}}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func (s *Store) MachineLock(machineUID string) func() {
	machineLocks.Lock()
	l := machineLocks.locks[machineUID]
	if l == nil {
		l = &sync.Mutex{}
		machineLocks.locks[machineUID] = l
	}
	machineLocks.Unlock()
	l.Lock()
	return l.Unlock
}

func (s *Store) Admit(op Operation) (Operation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if op.ID == "" {
		return Operation{}, false, errors.New("operation ID is required")
	}
	if existing, err := s.read(op.ID); err == nil {
		return existing, false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Operation{}, false, err
	}
	now := time.Now().UTC()
	op.CreatedAt, op.UpdatedAt, op.Outcome = now, now, Pending
	if err := s.write(op); err != nil {
		return Operation{}, false, err
	}
	return op, true, nil
}

func (s *Store) Get(id string) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read(id)
}

func (s *Store) List() ([]Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	operations := make([]Operation, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		op, err := s.read(id)
		if err != nil {
			return nil, err
		}
		operations = append(operations, op)
	}
	return operations, nil
}

func (s *Store) Finish(id string, outcome Outcome, status int, evidence string) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, err := s.read(id)
	if err != nil {
		return Operation{}, err
	}
	op.Outcome, op.HTTPStatus, op.Evidence, op.UpdatedAt = outcome, status, evidence, time.Now().UTC()
	if err := s.write(op); err != nil {
		return Operation{}, err
	}
	return op, nil
}

func (s *Store) read(id string) (Operation, error) {
	b, err := os.ReadFile(s.path(id))
	if err != nil {
		return Operation{}, err
	}
	var op Operation
	if err := json.Unmarshal(b, &op); err != nil {
		return Operation{}, err
	}
	return op, nil
}

func (s *Store) write(op Operation) error {
	b, err := json.MarshalIndent(op, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, ".operation-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, s.path(op.ID)); err != nil {
		return err
	}
	d, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (s *Store) path(id string) string {
	return filepath.Join(s.dir, id+".json")
}
