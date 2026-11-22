package registration

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"reflect"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const LedgerName = "fleet-capacity-registration-v1"

var ErrConflict = errors.New("registration conflicts with retained identity or an unknown operation; operator reconciliation is required")

type Entry struct {
	Operation     types.UID                 `json:"operation"`
	Actor         authenticationv1.UserInfo `json:"actor"`
	Project       string                    `json:"project"`
	ProjectUID    types.UID                 `json:"projectUID"`
	NamespaceUID  types.UID                 `json:"namespaceUID"`
	Name          string                    `json:"name"`
	CapacityUID   types.UID                 `json:"capacityUID"`
	DeviceName    string                    `json:"deviceName"`
	DeviceUID     types.UID                 `json:"deviceUID"`
	DeviceVersion string                    `json:"deviceVersion"`
	Aliases       []string                  `json:"aliases"`
	AssetID       string                    `json:"assetID"`
	Input         json.RawMessage           `json:"input"`
	Outcome       string                    `json:"outcome"`
}

type DeviceFence struct {
	Project      string    `json:"project"`
	ProjectUID   types.UID `json:"projectUID,omitempty"`
	Namespace    string    `json:"namespace,omitempty"`
	NamespaceUID types.UID `json:"namespaceUID,omitempty"`
	Name         string    `json:"name,omitempty"`
	UID          types.UID `json:"uid"`
	Aliases      []string  `json:"aliases,omitempty"`
	Deleted      bool      `json:"deleted,omitempty"`
	Version      string    `json:"version,omitempty"`
}

type Ledger struct {
	Authority    string        `json:"authority"`
	Projects     []string      `json:"projects"`
	Entries      []Entry       `json:"entries"`
	DeviceFences []DeviceFence `json:"deviceFences"`
}

type Store struct {
	Client     client.Client
	Namespace  string
	Authority  string
	Projects   []string
	ControlKey ed25519.PublicKey
	// AllowInactiveForTests skips the signed activation gate. The zero value
	// is gated; production wiring forces it off.
	AllowInactiveForTests bool
}

func (store Store) Read(ctx context.Context) (*corev1.Secret, Ledger, error) {
	secret := &corev1.Secret{}
	if err := store.Client.Get(ctx, client.ObjectKey{Namespace: store.Namespace, Name: LedgerName}, secret); err != nil {
		return nil, Ledger{}, err
	}
	ledger, _, err := store.readSnapshot(ctx, secret)
	if err != nil {
		return nil, Ledger{}, err
	}
	if secret.UID == "" || !secret.DeletionTimestamp.IsZero() || ledger.Authority != store.Authority || !reflect.DeepEqual(ledger.Projects, store.Projects) {
		return nil, Ledger{}, errors.New("registration ledger lifetime or configured scope is unavailable")
	}
	return secret, ledger, nil
}

func (store Store) Write(ctx context.Context, secret *corev1.Secret, ledger Ledger) error {
	if _, exists := secret.Data["routing.json"]; exists {
		return store.writeSnapshot(ctx, secret, ledger)
	}
	encoded, err := json.Marshal(ledger)
	if err != nil {
		return err
	}
	if len(encoded) > TailBudget {
		return errors.New("registration ledger is full; sign an expand control request to seal it losslessly, see docs/development/registration-storage-rollout.md; reservations were not released")
	}
	secret.Data["ledger.json"] = encoded
	return store.Client.Update(ctx, secret)
}

func overlaps(left, right []string) bool {
	for _, first := range left {
		for _, second := range right {
			if first == second {
				return true
			}
		}
	}
	return false
}
