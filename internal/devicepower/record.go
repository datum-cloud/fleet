// Package devicepower runs staff power operations on unallocated Devices.
//
// The site dispatcher (Reconciler) accepts or refuses each
// DevicePowerOperation in one Milo project, starts one worker Job per accepted
// operation and is the only writer of the operation's status. It holds no BMC
// credential. The worker (RunWorker) reads one mounted credential, re-checks
// the server's TLS key, identity and offered actions, sends at most one
// ComputerSystem.Reset and observes the result. It cannot reach Milo: it
// records its progress in a site-local stage record, which the dispatcher
// publishes. See docs/operations/device-power.md.
package devicepower

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
)

// Labels and annotations on site objects. Every object the dispatcher creates
// carries PartOfLabel and the operation's UID.
const (
	PartOfLabel        = "app.kubernetes.io/part-of"
	PartOf             = "fleet-device-power"
	ComponentLabel     = "app.kubernetes.io/component"
	OperationUIDLabel  = "fleet.datumapis.com/power-operation-uid"
	OperationNamespace = "fleet.datumapis.com/power-operation-namespace"
	OperationName      = "fleet.datumapis.com/power-operation-name"
	DeviceNamespace    = "fleet.datumapis.com/device-namespace"
	DeviceName         = "fleet.datumapis.com/device-name"
	// InputsKey and ProgressKey are the stage record's two data keys. The
	// dispatcher writes inputs once; the worker writes only progress.
	InputsKey   = "inputs"
	ProgressKey = "progress"
)

// Fixed time bounds from the design.
const (
	// AcceptanceWindow is how long an operation may wait to be accepted.
	AcceptanceWindow = 5 * time.Minute
	// StaleAfter is how long after the dispatcher's last capacity check a
	// worker may still start.
	StaleAfter = 2 * time.Minute
)

// WorkerName is the name of the stage record, worker Job, ServiceAccount,
// Role and RoleBinding of one operation.
func WorkerName(uid types.UID) string { return "pwr-" + string(uid) }

func uidOf(in Inputs) types.UID { return types.UID(in.OperationUID) }

// CredentialSecretName is the one credential Secret a worker may mount: the
// Device's namespace and name joined by dots. Namespaces hold no dots, so the
// name is unambiguous. ok is false when the name would exceed 253 characters.
func CredentialSecretName(namespace, device string) (name string, ok bool) {
	name = "bmc." + namespace + "." + device
	return name, len(name) <= 253
}

// LockName is the site Lease that admits one running operation per Device.
func LockName(project, namespace, device string) string {
	sum := sha256.Sum256([]byte(project + "/" + namespace + "/" + device))
	return "pwr-lock-" + hex.EncodeToString(sum[:10])
}

// Stage is how far a worker got. Only the worker moves a record from Pending
// through Finished; the dispatcher moves an unfinished record to Abandoned
// when its worker is gone, with the same optimistic lock, so a lost worker
// can no longer send.
type Stage string

const (
	// StagePending: the worker has not claimed the operation.
	StagePending Stage = "Pending"
	// StageClaimed: one worker claimed it. Nothing has been sent.
	StageClaimed Stage = "Claimed"
	// StageSending is written before the one command. From here on a
	// command may have been sent.
	StageSending Stage = "Sending"
	// StageSent: the command's response, or its loss, was recorded.
	StageSent Stage = "Sent"
	// StageFinished: the worker recorded a result.
	StageFinished Stage = "Finished"
	// StageAbandoned: the dispatcher fenced a record whose worker is gone.
	StageAbandoned Stage = "Abandoned"
)

// mayHaveSent reports whether a command may have reached the BMC.
func (s Stage) mayHaveSent() bool { return s == StageSending || s == StageSent }

// Inputs are the exact inputs of one operation, retained before any effect.
type Inputs struct {
	OperationUID string `json:"operationUID"`
	Project      string `json:"project"`
	Namespace    string `json:"namespace"`
	Operation    string `json:"operation"`
	Device       string `json:"device"`
	Endpoint     string `json:"endpoint"`
	SystemUUID   string `json:"systemUUID"`
	// PublicKeySHA256 is the TLS key discovery last observed for this BMC.
	// The mounted credential's approved pin must equal it.
	PublicKeySHA256   string                `json:"publicKeySHA256"`
	Action            inventory.PowerAction `json:"action"`
	CapacityCheckedAt metav1.Time           `json:"capacityCheckedAt"`
	ObserveTimeout    metav1.Duration       `json:"observeTimeout"`
	RequestTimeout    metav1.Duration       `json:"requestTimeout"`
}

// Progress is the worker's record of one operation.
type Progress struct {
	Stage     Stage        `json:"stage"`
	ClaimedBy string       `json:"claimedBy,omitempty"`
	ClaimedAt *metav1.Time `json:"claimedAt,omitempty"`
	// Before is the power state read just before the command.
	Before        *inventory.PowerObservation `json:"before,omitempty"`
	RequestSentAt *metav1.Time                `json:"requestSentAt,omitempty"`
	// SendOutcome is Answered, Refused or Lost. A lost response is never
	// resent.
	SendOutcome string                      `json:"sendOutcome,omitempty"`
	After       *inventory.PowerObservation `json:"after,omitempty"`
	Result      inventory.PowerResult       `json:"result,omitempty"`
	Reason      string                      `json:"reason,omitempty"`
	Message     string                      `json:"message,omitempty"`
	FinishedAt  *metav1.Time                `json:"finishedAt,omitempty"`
}

// Send outcomes.
const (
	SendAnswered = "Answered"
	SendRefused  = "Refused"
	SendLost     = "Lost"
)

var errRecordInvalid = errors.New("stage record is malformed")

func decodeRecord(cm *corev1.ConfigMap) (Inputs, Progress, error) {
	var in Inputs
	var p Progress
	if err := json.Unmarshal([]byte(cm.Data[InputsKey]), &in); err != nil {
		return in, p, errRecordInvalid
	}
	if err := json.Unmarshal([]byte(cm.Data[ProgressKey]), &p); err != nil {
		return in, p, errRecordInvalid
	}
	if in.OperationUID == "" || string(cm.Labels[OperationUIDLabel]) != in.OperationUID || cm.Name != WorkerName(types.UID(in.OperationUID)) || p.Stage == "" {
		return in, p, errRecordInvalid
	}
	return in, p, nil
}

func encode(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("encode stage record: %v", err))
	}
	return string(b)
}

// setProgress writes p into the record.
func setProgress(cm *corev1.ConfigMap, p Progress) {
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[ProgressKey] = encode(p)
}
