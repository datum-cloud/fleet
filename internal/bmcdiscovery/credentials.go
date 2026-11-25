package bmcdiscovery

import (
	"errors"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"go.datum.net/fleet/internal/redfish"
)

// Credential Secret contract. Secrets live in the cluster where discovery
// runs, never in Milo or on the Device.
const (
	// CredentialLabel selects credential Secrets; its value must be "true".
	CredentialLabel = "fleet.datumapis.com/bmc-credential"
	// The annotations name the one Device a Secret is for.
	ProjectAnnotation   = "fleet.datumapis.com/device-project"
	NamespaceAnnotation = "fleet.datumapis.com/device-namespace"
	NameAnnotation      = "fleet.datumapis.com/device-name"

	UsernameKey = "username"
	PasswordKey = "password"
	// PinKey holds the operator-approved SHA-256 of the BMC's TLS public key.
	// Until it is set discovery records the observed key and sends nothing.
	PinKey = "tlsPublicKeySHA256"
)

// Credential is a parsed credential Secret.
type Credential struct {
	Username, Password string
	// Pin is empty until an operator approves the BMC's key.
	Pin string
	// Version changes whenever the Secret does, so an approval triggers a new
	// attempt at once.
	Version string
}

var (
	errCredentialConflict = errors.New("more than one credential Secret names this Device")
	errCredentialInvalid  = errors.New("credential Secret is malformed")
)

// deviceKey is the Device a credential Secret names, or "" when the Secret
// does not name one in project.
func deviceKey(secret *corev1.Secret, project string) string {
	if secret.Labels[CredentialLabel] != "true" || secret.Annotations[ProjectAnnotation] != project {
		return ""
	}
	ns, name := secret.Annotations[NamespaceAnnotation], secret.Annotations[NameAnnotation]
	if ns == "" || name == "" {
		return ""
	}
	return ns + "/" + name
}

// selectCredential picks the one Secret for a Device. It returns found=false
// when none names it. A conflict or malformed Secret is an error with a
// Version, so the condition is reported and the attempt still records the
// anonymous TLS observation.
func selectCredential(secrets []corev1.Secret) (cred Credential, found bool, err error) {
	if len(secrets) == 0 {
		return Credential{}, false, nil
	}
	versions := make([]string, 0, len(secrets))
	for _, s := range secrets {
		versions = append(versions, string(s.UID)+"@"+s.ResourceVersion)
	}
	sort.Strings(versions)
	version := strings.Join(versions, ",")
	if len(secrets) > 1 {
		return Credential{Version: version}, true, errCredentialConflict
	}
	data := secrets[0].Data
	cred = Credential{Username: strings.TrimSpace(string(data[UsernameKey])), Password: strings.TrimSpace(string(data[PasswordKey])), Version: version}
	if cred.Username == "" || cred.Password == "" || strings.ContainsAny(cred.Username, ":\r\n") || strings.ContainsAny(cred.Password, "\r\n") {
		return Credential{Version: version}, true, errCredentialInvalid
	}
	if pin := strings.TrimSpace(string(data[PinKey])); pin != "" {
		if cred.Pin, err = redfish.ParsePin(pin); err != nil {
			return Credential{Version: version}, true, errCredentialInvalid
		}
	}
	return cred, true, nil
}

// CredentialFor returns the credential for one Device, named
// "<namespace>/<name>" in project, from the credential Secrets of its site.
// The site worker uses it, with the same contract as discovery.
func CredentialFor(secrets []corev1.Secret, project, device string) (Credential, bool, error) {
	var matched []corev1.Secret
	for index := range secrets {
		if deviceKey(&secrets[index], project) == device {
			matched = append(matched, secrets[index])
		}
	}
	return selectCredential(matched)
}
