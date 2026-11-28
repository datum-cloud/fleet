package redfish

import (
	"fmt"
	"strings"

	"github.com/stmcginnis/gofish"
)

// resetTypes returns the ComputerSystem.Reset types a System advertises,
// inline or through its ActionInfo, and the action target. Both must belong to
// the System at sysPath. It issues at most one GET, of the ActionInfo, and
// never invokes the action.
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
