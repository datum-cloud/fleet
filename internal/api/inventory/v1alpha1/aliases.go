package v1alpha1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// Aliases returns the normalized hardware identifiers of a Device: serial,
// system UUID and, when present, the provider host reference. Two Devices
// sharing any alias may be the same machine.
func Aliases(device *Device) []string {
	var result []string
	pairs := [][2]string{{"serial", strings.ToLower(strings.TrimSpace(device.Spec.Identity.Serial))}, {"uuid", strings.ToLower(strings.TrimSpace(device.Spec.Identity.SystemUUID))}}
	if reference := device.Spec.ProviderHostRef; reference != nil {
		encoded, _ := json.Marshal([]string{strings.ToLower(strings.TrimSpace(reference.System)), strings.ToLower(strings.TrimSpace(reference.Type)), strings.ToLower(strings.TrimSpace(reference.ID))})
		pairs = append(pairs, [2]string{"provider", string(encoded)})
	}
	for _, pair := range pairs {
		if pair[1] != "" {
			digest := sha256.Sum256([]byte(pair[0] + ":" + pair[1]))
			result = append(result, pair[0]+":"+hex.EncodeToString(digest[:]))
		}
	}
	return result
}
