package redfish

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/stmcginnis/gofish"
	"github.com/stmcginnis/gofish/schemas"
)

// resetTypes returns the ComputerSystem.Reset types a System advertises,
// inline or through its ActionInfo, and the action target. Both must belong to
// the System at sysPath. It issues at most one GET, of the ActionInfo.
func resetTypes(api *gofish.APIClient, sysPath string, raw resetAction) (target string, types []string, err error) {
	reset := raw.Actions.Reset
	target, err = cleanPath(reset.Target)
	if err != nil || !strings.HasPrefix(target, sysPath+"/") {
		return "", nil, fmt.Errorf("%w: ComputerSystem.Reset target not on the System", ErrUnsupported)
	}
	if len(reset.Allowed) != 0 || reset.Info == "" {
		return target, reset.Allowed, nil
	}
	info, err := cleanPath(reset.Info)
	if err != nil || !strings.HasPrefix(info, sysPath+"/") {
		return "", nil, fmt.Errorf("%w: ActionInfo not on the System", ErrUnsupported)
	}
	// Not schemas.ActionInfo: its typed decode rejects the whole document
	// for one off-schema property, as the DMTF emulator's string
	// "Required": "true". Only the ResetType values are read.
	var ai struct {
		Parameters []struct {
			Name, DataType  string
			AllowableValues []string
		}
	}
	if err := getJSON(api, info, &ai); err != nil {
		return "", nil, err
	}
	for _, p := range ai.Parameters {
		if p.Name == "ResetType" && (p.DataType == "" || p.DataType == "String") {
			types = p.AllowableValues
		}
	}
	return target, types, nil
}

// PowerSystem is the one System a power operation acts on, found by UUID.
type PowerSystem struct {
	Path        string
	ID          string
	UUID        string
	ResetTarget string
	ResetTypes  []string
}

// PowerState is one read of a System's power.
type PowerState struct {
	State string
	// LastResetTime is when the BMC reports the System last came out of a
	// reset, or zero when it does not report one.
	LastResetTime time.Time
	ObservedAt    time.Time
}

// FindPowerSystem reads every member of the Systems collection, across all
// pages, and returns the only one that carries uuid with its reset target and
// offered reset types. No member, or more than one, is ErrIdentity: a
// replaced server behind the same BMC address is refused here. It issues GETs
// only.
func (c *Client) FindPowerSystem(ctx context.Context, uuid string) (PowerSystem, error) {
	api, err := c.connect(ctx)
	if err != nil {
		return PowerSystem{}, err
	}
	var root struct{ Systems link }
	if err := getJSON(api, schemas.DefaultServiceRoot, &root); err != nil {
		return PowerSystem{}, err
	}
	if root.Systems.ID == "" {
		return PowerSystem{}, fmt.Errorf("%w: service root has no Systems collection", ErrUnsupported)
	}
	collection, err := cleanPath(root.Systems.ID)
	if err != nil {
		return PowerSystem{}, err
	}
	members, err := schemas.GetCollectionObjects[schemas.ComputerSystem](api, collection)
	if err != nil {
		// A partially read collection cannot prove the UUID is unique.
		return PowerSystem{}, classify(err)
	}
	var found []*schemas.ComputerSystem
	for _, s := range members {
		p, err := cleanPath(s.ODataID)
		if err != nil || !strings.HasPrefix(p, collection+"/") {
			return PowerSystem{}, fmt.Errorf("%w: system member outside the collection", ErrIdentity)
		}
		if canonicalUUID(s.UUID) == uuid {
			found = append(found, s)
		}
	}
	if len(found) != 1 {
		return PowerSystem{}, fmt.Errorf("%w: %d members carry the expected UUID", ErrIdentity, len(found))
	}
	sys := PowerSystem{ID: found[0].ID, UUID: uuid}
	sys.Path, _ = cleanPath(found[0].ODataID)
	var raw resetAction
	if err := json.Unmarshal(found[0].RawData, &raw); err != nil {
		return PowerSystem{}, classify(err)
	}
	if sys.ResetTarget, sys.ResetTypes, err = resetTypes(api, sys.Path, raw); err != nil {
		return PowerSystem{}, err
	}
	return sys, nil
}

// ObservePower reads the System again, re-checks that it still carries the
// same Id and UUID, and returns its power state. A GET only.
func (c *Client) ObservePower(ctx context.Context, sys PowerSystem) (PowerState, error) {
	api, err := c.connect(ctx)
	if err != nil {
		return PowerState{}, err
	}
	var doc struct {
		ID            string `json:"Id"`
		UUID          string
		PowerState    string
		LastResetTime string
	}
	if err := getJSON(api, sys.Path, &doc); err != nil {
		return PowerState{}, err
	}
	if doc.ID != sys.ID || canonicalUUID(doc.UUID) != sys.UUID {
		return PowerState{}, fmt.Errorf("%w: the System changed identity", ErrIdentity)
	}
	out := PowerState{State: doc.PowerState, ObservedAt: time.Now().UTC()}
	if doc.LastResetTime != "" {
		if t, err := time.Parse(time.RFC3339, doc.LastResetTime); err == nil {
			out.LastResetTime = t.UTC()
		}
	}
	return out, nil
}

// ResetPower posts exactly one ComputerSystem.Reset of kind to the System's
// own action target. It is never retried, and HTTP success is not evidence of
// the effect: callers observe the System afterwards.
func (c *Client) ResetPower(ctx context.Context, sys PowerSystem, kind string) error {
	if !contains(sys.ResetTypes, kind) {
		return fmt.Errorf("%w: ResetType %s", ErrUnsupported, kind)
	}
	return c.Reset(ctx, System{ResetTarget: sys.ResetTarget, ResetTypes: sys.ResetTypes}, kind)
}
