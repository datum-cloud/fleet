package registration

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ControlRequest struct {
	Action          string    `json:"action"`
	ID              string    `json:"id"`
	Reason          string    `json:"reason"`
	Authority       string    `json:"authority"`
	Projects        []string  `json:"projects"`
	RootUID         types.UID `json:"rootUID"`
	ResourceVersion string    `json:"resourceVersion"`
	Epoch           uint64    `json:"epoch"`
	FenceDigest     string    `json:"fenceDigest,omitempty"`
}

type SignedControl struct {
	Request   ControlRequest `json:"request"`
	Signature []byte         `json:"signature"`
}

func SignControl(request ControlRequest, key ed25519.PrivateKey) SignedControl {
	encoded, _ := json.Marshal(request)
	return SignedControl{Request: request, Signature: ed25519.Sign(key, append([]byte("fleet-registration-control-v2\n"), encoded...))}
}

func (store Store) verifyControl(control SignedControl, uid types.UID) error {
	request := control.Request
	encoded, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if len(store.ControlKey) != ed25519.PublicKeySize || len(encoded) > 8192 || !ed25519.Verify(store.ControlKey, append([]byte("fleet-registration-control-v2\n"), encoded...), control.Signature) {
		return errors.New("registration control signature unavailable or invalid")
	}
	if request.RootUID != uid || request.Authority != store.Authority || !reflect.DeepEqual(request.Projects, store.Projects) || request.ResourceVersion == "" || request.ID == "" || request.Reason == "" || (request.Action != "expand" && request.Action != "activate") {
		return errors.New("registration control authority or action mismatch")
	}
	return nil
}

func (store Store) root(ctx context.Context) (*corev1.Secret, Routing, error) {
	secret := &corev1.Secret{}
	if err := store.Client.Get(ctx, client.ObjectKey{Namespace: store.Namespace, Name: LedgerName}, secret); err != nil {
		return nil, Routing{}, err
	}
	if secret.UID == "" || !secret.DeletionTimestamp.IsZero() {
		return nil, Routing{}, errors.New("ledger lifetime unavailable")
	}
	if _, exists := secret.Data["routing.json"]; exists {
		route, err := decodeRouting(secret)
		return secret, route, err
	}
	return secret, Routing{Version: 2, RootUID: secret.UID}, nil
}

func encodeRoute(secret *corev1.Secret, route Routing) error {
	encoded, err := json.Marshal(route)
	if err != nil {
		return err
	}
	if _, exists := secret.Data["routing.json"]; !exists {
		secret.Data["tail.json"] = secret.Data["ledger.json"]
		secret.Data["ledger.json"] = []byte(`{"authority":"v2-routing-required","projects":[]}`)
	}
	secret.Data["routing.json"] = encoded
	return nil
}

func (handler *Handler) ApplyControl(ctx context.Context, control SignedControl) error {
	secret, route, err := handler.Store.root(ctx)
	if err != nil {
		return err
	}
	if err := handler.Store.verifyControl(control, secret.UID); err != nil {
		return err
	}
	if control.Request.Action == "expand" && control.Request.Epoch < route.Epoch {
		reference := route.Segments[control.Request.Epoch]
		segment := &corev1.Secret{}
		if err := handler.Store.Client.Get(ctx, client.ObjectKey{Namespace: handler.Store.Namespace, Name: reference.Name}, segment); err != nil {
			return err
		}
		encoded, _ := json.Marshal(control)
		if segment.UID != reference.UID || segment.Immutable == nil || !*segment.Immutable || !segment.DeletionTimestamp.IsZero() || digest(segment.Data["ledger.json"]) != reference.Digest || string(segment.Data["control.json"]) != string(encoded) {
			return errors.New("stale expansion does not match retained completion")
		}
		return nil
	}
	if route.Pending != nil {
		if !reflect.DeepEqual(*route.Pending, control) {
			return errors.New("another signed expansion is pending")
		}
		return handler.Store.ResumeExpansion(ctx)
	}
	if route.Activation != nil && reflect.DeepEqual(*route.Activation, control) {
		return nil
	}
	if route.Epoch != control.Request.Epoch || secret.ResourceVersion != control.Request.ResourceVersion {
		return errors.New("stale control epoch or CAS baseline; inspect current root")
	}
	if _, _, err := handler.Store.Read(ctx); err != nil {
		return err
	}
	switch control.Request.Action {
	case "activate":
		if err := handler.verifyActivation(ctx, control); err != nil {
			return err
		}
		if route.Activation != nil {
			if len(route.ActivationHistory) >= 7 {
				return errors.New("activation audit budget exhausted; retained history requires a reviewed migration")
			}
			route.ActivationHistory = append(route.ActivationHistory, *route.Activation)
		}
		route.Activation = &control
	case "expand":
		if len(route.Segments) >= MaxSegments {
			return ErrGrowthStopped
		}
		route.Pending = &control
	}
	if err := encodeRoute(secret, route); err != nil {
		return err
	}
	if err := handler.Store.updateRoot(ctx, secret); err != nil {
		return err
	}
	slog.InfoContext(ctx, "registration signed control retained", "action", control.Request.Action, "id", control.Request.ID, "rootUID", secret.UID, "epoch", route.Epoch, "signer", digest(handler.Store.ControlKey))
	if route.Pending != nil {
		return handler.Store.ResumeExpansion(ctx)
	}
	return nil
}

func (store Store) ResumeExpansion(ctx context.Context) error {
	secret, route, err := store.root(ctx)
	if err != nil || route.Pending == nil {
		return err
	}
	if err := store.verifyControl(*route.Pending, secret.UID); err != nil {
		return err
	}
	if route.Pending.Request.Action != "expand" || route.Pending.Request.Epoch != route.Epoch || len(route.Segments) >= MaxSegments {
		return errors.New("invalid pending expansion")
	}
	if _, _, err := store.readSnapshot(ctx, secret); err != nil {
		return err
	}
	tail := secret.Data["tail.json"]
	if len(tail) > SegmentBudget {
		return ErrGrowthStopped
	}
	control, _ := json.Marshal(route.Pending)
	immutable := true
	segment := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: store.Namespace, Name: segmentName(secret.UID, route.Epoch)}, Immutable: &immutable, Data: map[string][]byte{"ledger.json": tail, "control.json": control, "rootUID": []byte(secret.UID)}}
	if err := store.Client.Create(ctx, segment); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	stored := &corev1.Secret{}
	if err := store.Client.Get(ctx, client.ObjectKeyFromObject(segment), stored); err != nil {
		return err
	}
	if stored.UID == "" || !stored.DeletionTimestamp.IsZero() || stored.Immutable == nil || !*stored.Immutable || !reflect.DeepEqual(stored.Data, segment.Data) {
		return errors.New("expansion segment identity or contents conflict; remain fenced")
	}
	route.Segments = append(route.Segments, Segment{Name: stored.Name, UID: stored.UID, Digest: digest(tail)})
	route.Epoch++
	route.Pending = nil
	empty, _ := json.Marshal(Ledger{Authority: store.Authority, Projects: store.Projects})
	secret.Data["tail.json"] = empty
	if err := encodeRoute(secret, route); err != nil {
		return err
	}
	if err := store.updateRoot(ctx, secret); err != nil {
		return err
	}
	slog.InfoContext(ctx, "registration expansion committed", "rootUID", secret.UID, "epoch", route.Epoch, "segmentUID", stored.UID)
	return nil
}

func (handler *Handler) verifyActivation(ctx context.Context, control SignedControl) error {
	if handler.VerifyFence == nil || handler.FenceDigest == nil {
		return errors.New("activation fence verification unavailable")
	}
	if err := handler.VerifyFence(ctx); err != nil {
		return err
	}
	actual, err := handler.FenceDigest(ctx)
	if err != nil {
		return err
	}
	if actual == "" || actual != control.Request.FenceDigest {
		return errors.New("installed fence differs from signed activation")
	}
	return nil
}

func (handler *Handler) Ready(ctx context.Context) error {
	secret, route, err := handler.Store.root(ctx)
	if err != nil {
		return err
	}
	return handler.readySnapshot(ctx, secret, route)
}

func (handler *Handler) readySnapshot(ctx context.Context, secret *corev1.Secret, route Routing) error {
	if route.Pending != nil || route.Activation == nil {
		return errors.New("registration is non-admitting: signed activation or expansion incomplete")
	}
	if err := handler.Store.verifyControl(*route.Activation, secret.UID); err != nil {
		return err
	}
	if route.Activation.Request.Action != "activate" {
		return errors.New("invalid activation action")
	}
	if err := handler.verifyActivation(ctx, *route.Activation); err != nil {
		return err
	}
	_, _, err := handler.Store.readSnapshot(ctx, secret)
	return err
}
