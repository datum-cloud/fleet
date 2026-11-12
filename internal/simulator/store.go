package simulator

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	currentSchemaVersion = 1
	maxIdentifierBytes   = 256
	maxFenceTokenBytes   = 1024
	maxInputsBytes       = 1 << 20
)

var (
	ErrConflict       = errors.New("operation identity conflicts with retained input")
	ErrFenced         = errors.New("operation is fenced by retained asset authority")
	ErrInvalid        = errors.New("invalid operation request")
	ErrOutcomeUnknown = errors.New("operation persistence outcome is unknown")
)

type Request struct {
	ID         string          `json:"id"`
	Kind       string          `json:"kind"`
	AssetID    string          `json:"assetID"`
	BindingID  string          `json:"bindingID"`
	Epoch      int64           `json:"epoch"`
	FenceToken string          `json:"fenceToken"`
	Inputs     json.RawMessage `json:"inputs,omitempty"`
}

type Operation struct {
	Request
	InputDigest string     `json:"inputDigest"`
	Phase       string     `json:"phase"`
	Result      string     `json:"result,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
}

type OperationResult struct {
	ID          string     `json:"id"`
	Kind        string     `json:"kind"`
	Phase       string     `json:"phase"`
	Result      string     `json:"result,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
}

func (o Operation) ResultView() OperationResult {
	return OperationResult{
		ID: o.ID, Kind: o.Kind, Phase: o.Phase,
		Result: o.Result, CreatedAt: o.CreatedAt, CompletedAt: cloneTime(o.CompletedAt),
	}
}

type Asset struct {
	ID              string `json:"id"`
	BindingID       string `json:"bindingID,omitempty"`
	Epoch           int64  `json:"epoch"`
	FenceToken      string `json:"fenceToken,omitempty"`
	AuthorityClosed bool   `json:"authorityClosed,omitempty"`
	InstalledImage  string `json:"installedImage,omitempty"`
	Dirty           bool   `json:"dirty"`
	PowerState      string `json:"powerState"`
}

type persisted struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Assets        map[string]*Asset     `json:"assets"`
	Operations    map[string]*Operation `json:"operations"`
	Faults        map[string]bool       `json:"faults"`
}

type persistFunc func(path string, state persisted) (renamed bool, err error)

type Store struct {
	mu      sync.Mutex
	readyMu sync.Mutex
	path    string
	state   persisted
	persist persistFunc
}

func Open(path string) (*Store, error) {
	state := emptyState()
	b, err := os.ReadFile(path)
	if err == nil {
		var loaded persisted
		if err := json.Unmarshal(b, &loaded); err != nil {
			return nil, fmt.Errorf("decode simulator state: %w", err)
		}
		state = loaded
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read simulator state: %w", err)
	}
	if err := validateState(state); err != nil {
		return nil, fmt.Errorf("validate simulator state: %w", err)
	}
	return &Store{path: path, state: state, persist: persistState}, nil
}

func emptyState() persisted {
	return persisted{
		SchemaVersion: currentSchemaVersion,
		Assets:        map[string]*Asset{},
		Operations:    map[string]*Operation{},
		Faults:        map[string]bool{},
	}
}

func (s *Store) Submit(req Request) (Operation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	req, err := validateRequest(req)
	if err != nil {
		return Operation{}, false, err
	}
	d := digest(req)
	if old := s.state.Operations[req.ID]; old != nil {
		if old.InputDigest != d {
			return Operation{}, false, ErrConflict
		}
		return cloneOperation(*old), false, nil
	}

	next, err := cloneState(s.state)
	if err != nil {
		return Operation{}, false, fmt.Errorf("clone simulator state: %w", err)
	}
	asset := next.Assets[req.AssetID]
	if asset == nil {
		asset = &Asset{ID: req.AssetID, PowerState: "Off"}
		next.Assets[req.AssetID] = asset
	}
	if err := admitAuthority(asset, req); err != nil {
		return Operation{}, false, err
	}

	lost := next.Faults["lost-response"]
	if lost {
		next.Faults["lost-response"] = false
	}
	now := time.Now().UTC()
	op := &Operation{
		Request: req, InputDigest: d, Phase: "Succeeded",
		CreatedAt: now, CompletedAt: &now,
	}
	apply(asset, op)
	next.Operations[req.ID] = op

	renamed, err := s.persist(s.path, next)
	if renamed {
		s.state = next
	}
	if err != nil {
		if renamed {
			return Operation{}, false, fmt.Errorf("%w: %v", ErrOutcomeUnknown, err)
		}
		return Operation{}, false, fmt.Errorf("persist operation: %w", err)
	}
	return cloneOperation(*op), lost, nil
}

func admitAuthority(asset *Asset, req Request) error {
	if req.Epoch < asset.Epoch {
		return ErrFenced
	}
	if req.Epoch == asset.Epoch && asset.Epoch != 0 {
		if asset.AuthorityClosed || asset.BindingID != req.BindingID || asset.FenceToken != req.FenceToken {
			return ErrFenced
		}
		return nil
	}
	if asset.Epoch != 0 && !asset.AuthorityClosed {
		return ErrFenced
	}
	asset.Epoch = req.Epoch
	asset.BindingID = req.BindingID
	asset.FenceToken = req.FenceToken
	asset.AuthorityClosed = false
	return nil
}

func apply(asset *Asset, op *Operation) {
	switch op.Kind {
	case "install":
		var in installInputs
		mustDecode(op.Inputs, &in)
		asset.Dirty = true
		asset.InstalledImage = in.Image
		op.Result = "installed"
	case "cleanup":
		asset.InstalledImage = ""
		asset.Dirty = false
		asset.PowerState = "Off"
		asset.AuthorityClosed = true
		op.Result = "clean"
	case "power":
		var in powerInputs
		mustDecode(op.Inputs, &in)
		asset.PowerState = in.State
		op.Result = in.State
	default:
		panic("validated operation kind became unsupported")
	}
}

func (s *Store) Get(id string) (Operation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, ok := s.state.Operations[id]
	if !ok {
		return Operation{}, false
	}
	return cloneOperation(*op), true
}

func (s *Store) Snapshot() persisted {
	s.mu.Lock()
	defer s.mu.Unlock()
	out, err := cloneState(s.state)
	if err != nil {
		panic(fmt.Sprintf("clone validated simulator state: %v", err))
	}
	return out
}

func (s *Store) SetFault(name string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name != "lost-response" {
		return fmt.Errorf("%w: unsupported fault %q", ErrInvalid, name)
	}
	next, err := cloneState(s.state)
	if err != nil {
		return fmt.Errorf("clone simulator state: %w", err)
	}
	next.Faults[name] = enabled
	renamed, err := s.persist(s.path, next)
	if renamed {
		s.state = next
	}
	if err != nil {
		if renamed {
			return fmt.Errorf("%w: %v", ErrOutcomeUnknown, err)
		}
		return fmt.Errorf("persist fault: %w", err)
	}
	return nil
}

func (s *Store) Ready() error {
	s.readyMu.Lock()
	defer s.readyMu.Unlock()
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".fleet-ready-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err := f.Write([]byte("ready")); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Remove(name); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

type installInputs struct {
	Image string `json:"image"`
}

type powerInputs struct {
	State string `json:"state"`
}

func validateRequest(req Request) (Request, error) {
	if err := validateIdentifier("id", req.ID, maxIdentifierBytes); err != nil {
		return Request{}, err
	}
	if err := validateIdentifier("assetID", req.AssetID, maxIdentifierBytes); err != nil {
		return Request{}, err
	}
	if err := validateIdentifier("bindingID", req.BindingID, maxIdentifierBytes); err != nil {
		return Request{}, err
	}
	if err := validateIdentifier("fenceToken", req.FenceToken, maxFenceTokenBytes); err != nil {
		return Request{}, err
	}
	if req.Epoch < 1 {
		return Request{}, fmt.Errorf("%w: epoch must be positive", ErrInvalid)
	}
	if len(req.Inputs) > maxInputsBytes {
		return Request{}, fmt.Errorf("%w: inputs exceed %d bytes", ErrInvalid, maxInputsBytes)
	}

	switch req.Kind {
	case "install":
		var in installInputs
		if err := decodeExactInputs(req.Inputs, &in, false); err != nil {
			return Request{}, fmt.Errorf("%w: install inputs: %v", ErrInvalid, err)
		}
		if strings.TrimSpace(in.Image) == "" {
			return Request{}, fmt.Errorf("%w: install image is required", ErrInvalid)
		}
	case "power":
		var in powerInputs
		if err := decodeExactInputs(req.Inputs, &in, false); err != nil {
			return Request{}, fmt.Errorf("%w: power inputs: %v", ErrInvalid, err)
		}
		if in.State != "On" && in.State != "Off" {
			return Request{}, fmt.Errorf("%w: power state must be On or Off", ErrInvalid)
		}
	case "cleanup":
		var in struct{}
		if err := decodeExactInputs(req.Inputs, &in, true); err != nil {
			return Request{}, fmt.Errorf("%w: cleanup inputs: %v", ErrInvalid, err)
		}
	default:
		return Request{}, fmt.Errorf("%w: unsupported operation kind %q", ErrInvalid, req.Kind)
	}

	req.Inputs = append(json.RawMessage(nil), req.Inputs...)
	return req, nil
}

func validateIdentifier(field, value string, limit int) error {
	if value == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalid, field)
	}
	if len(value) > limit || !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s is invalid or exceeds %d bytes", ErrInvalid, field, limit)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: %s contains control characters", ErrInvalid, field)
		}
	}
	return nil
}

func decodeExactInputs(raw json.RawMessage, target any, allowEmpty bool) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		if allowEmpty {
			return nil
		}
		return errors.New("object is required")
	}
	if trimmed[0] != '{' {
		return errors.New("JSON object is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func mustDecode(raw json.RawMessage, target any) {
	if err := json.Unmarshal(raw, target); err != nil {
		panic("validated simulator input became invalid")
	}
}

func validateState(state persisted) error {
	if state.SchemaVersion != currentSchemaVersion {
		return fmt.Errorf("unsupported schema version %d", state.SchemaVersion)
	}
	if state.Assets == nil || state.Operations == nil || state.Faults == nil {
		return errors.New("state maps must be present")
	}
	for key, asset := range state.Assets {
		if asset == nil || asset.ID != key || asset.Epoch < 0 {
			return fmt.Errorf("invalid asset entry %q", key)
		}
		if asset.Epoch > 0 && (asset.BindingID == "" || asset.FenceToken == "") {
			return fmt.Errorf("asset %q has incomplete authority", key)
		}
		if asset.PowerState != "On" && asset.PowerState != "Off" {
			return fmt.Errorf("asset %q has invalid power state", key)
		}
		if err := validateIdentifier("asset.id", asset.ID, maxIdentifierBytes); err != nil {
			return fmt.Errorf("asset %q: %w", key, err)
		}
		if asset.Epoch > 0 {
			if err := validateIdentifier("asset.bindingID", asset.BindingID, maxIdentifierBytes); err != nil {
				return fmt.Errorf("asset %q: %w", key, err)
			}
			if err := validateIdentifier("asset.fenceToken", asset.FenceToken, maxFenceTokenBytes); err != nil {
				return fmt.Errorf("asset %q: %w", key, err)
			}
		}
		if asset.AuthorityClosed && (asset.Dirty || asset.PowerState != "Off" || asset.InstalledImage != "") {
			return fmt.Errorf("asset %q has closed authority without verified cleanup", key)
		}
		if asset.Dirty != (asset.InstalledImage != "") {
			return fmt.Errorf("asset %q has inconsistent installation state", key)
		}
	}
	for key, op := range state.Operations {
		if op == nil || op.ID != key {
			return fmt.Errorf("invalid operation entry %q", key)
		}
		req, err := validateRequest(op.Request)
		if err != nil {
			return fmt.Errorf("operation %q: %w", key, err)
		}
		if op.InputDigest != digest(req) || op.Phase != "Succeeded" || op.CreatedAt.IsZero() || op.CompletedAt == nil || op.CompletedAt.IsZero() {
			return fmt.Errorf("operation %q has invalid retained result", key)
		}
		if op.CompletedAt.Before(op.CreatedAt) {
			return fmt.Errorf("operation %q completed before it was created", key)
		}
		asset := state.Assets[op.AssetID]
		if asset == nil || op.Epoch > asset.Epoch {
			return fmt.Errorf("operation %q has no compatible retained asset", key)
		}
		if op.Epoch == asset.Epoch && (op.BindingID != asset.BindingID || op.FenceToken != asset.FenceToken) {
			return fmt.Errorf("operation %q conflicts with current asset authority", key)
		}
		switch op.Kind {
		case "install":
			if op.Result != "installed" {
				return fmt.Errorf("operation %q has invalid install result", key)
			}
		case "cleanup":
			if op.Result != "clean" || (op.Epoch == asset.Epoch && !asset.AuthorityClosed) {
				return fmt.Errorf("operation %q has invalid cleanup result", key)
			}
		case "power":
			var in powerInputs
			mustDecode(op.Inputs, &in)
			if op.Result != in.State {
				return fmt.Errorf("operation %q has invalid power result", key)
			}
		}
	}
	for name := range state.Faults {
		if name != "lost-response" {
			return fmt.Errorf("unsupported retained fault %q", name)
		}
	}
	return nil
}

func persistState(path string, state persisted) (bool, error) {
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return false, err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return false, err
	}
	f, err := os.CreateTemp(dir, ".fleet-state-*")
	if err != nil {
		return false, err
	}
	tmp := f.Name()
	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return false, err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return false, err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return false, err
	}
	if err := f.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return false, err
	}
	renamed = true
	d, err := os.Open(dir)
	if err != nil {
		return true, err
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return true, err
	}
	return true, nil
}

func cloneState(in persisted) (persisted, error) {
	b, err := json.Marshal(in)
	if err != nil {
		return persisted{}, err
	}
	var out persisted
	if err := json.Unmarshal(b, &out); err != nil {
		return persisted{}, err
	}
	return out, nil
}

func cloneOperation(in Operation) Operation {
	in.Inputs = append(json.RawMessage(nil), in.Inputs...)
	in.CompletedAt = cloneTime(in.CompletedAt)
	return in
}

func cloneTime(in *time.Time) *time.Time {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func digest(r Request) string {
	copy := r
	copy.ID = ""
	b, err := json.Marshal(copy)
	if err != nil {
		panic("marshal validated simulator request")
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
