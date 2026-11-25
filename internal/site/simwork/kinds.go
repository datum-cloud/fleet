// Package simwork is the simulator site's install and wipe workers for
// synthetic Devices that have no BMC. The site reaches them through
// fixture.Access. The simulated server is a
// record in the site's store, so every effect goes through the same fenced
// journal as a real one, and a site restart keeps both. It is never a
// production executor.
package simwork

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/site"
	"go.datum.net/fleet/internal/site/fixture"
)

// Server is the simulated server's state.
type Server struct {
	// Image is the digest written to the disks, or empty after a wipe.
	Image string `json:"image,omitempty"`
	// Booted is the digest the server last booted from disk.
	Booted string `json:"booted,omitempty"`
	// Bootstrap is the salted digest of the bootstrap data written with the
	// image, recomputed from what was written, as the guest would read it
	// on first boot. The data itself is not kept.
	Bootstrap string `json:"bootstrap,omitempty"`
}

const serverRecords = "simulator"

// serverName is the one record name for a Device's simulated server.
const serverName = "server"

func read(work *site.Work) (Server, error) {
	var server Server
	_, err := work.Store().GetRecord(work.Device, serverRecords, serverName, &server)
	return server, err
}

func write(work *site.Work, server Server) error {
	return work.Store().PutRecord(work.Device, work.Fence, serverRecords, serverName, server)
}

// ReadServer returns a Device's simulated server, for evidence checks.
func ReadServer(store *site.Store, device string) (Server, error) {
	var server Server
	_, err := store.GetRecord(device, serverRecords, serverName, &server)
	return server, err
}

func connection(work *site.Work) *fixture.Connection {
	connection, _ := work.Connection.(*fixture.Connection)
	return connection
}

func checkDisks(work *site.Work, disks []string) *custody.Refusal {
	if connection(work) == nil {
		return &custody.Refusal{Reason: custody.ReasonTargetUnsupported, Message: "this site does not run the simulator"}
	}
	if len(disks) == 0 || !slices.Equal(slices.Sorted(slices.Values(disks)), slices.Sorted(slices.Values(connection(work).Target.Disks))) {
		return &custody.Refusal{Reason: custody.ReasonTargetDisks, Message: "the target disks are not the site's enrolled disks"}
	}
	return nil
}

// Installation is the simulator's DeviceInstallation worker.
type Installation struct{}

// Wipe is the simulator's DeviceWipe worker.
type Wipe struct{}

var (
	_ site.Kind = Installation{}
	_ site.Kind = Wipe{}
)

func installSpec(work *site.Work) (ops.DeviceInstallationSpec, *custody.Refusal) {
	var spec ops.DeviceInstallationSpec
	if err := json.Unmarshal(work.Inputs, &spec); err != nil || spec.Image.Digest == "" {
		return spec, &custody.Refusal{Reason: custody.ReasonInputsUnreadable, Message: "the admitted inputs are not a DeviceInstallation spec"}
	}
	return spec, nil
}

func (Installation) Check(_ context.Context, work *site.Work) *custody.Refusal {
	spec, refusal := installSpec(work)
	if refusal != nil {
		return refusal
	}
	return checkDisks(work, spec.TargetDisks)
}

func (Installation) Run(ctx context.Context, work *site.Work) (any, error) {
	spec, refusal := installSpec(work)
	if refusal != nil {
		return nil, refusal
	}
	// The framework verified the payload against the admitted digest.
	bootstrap := ""
	if payload := work.Bootstrap(); payload != nil {
		hash := sha256.New()
		hash.Write(payload.Salt)
		hash.Write(payload.Data)
		bootstrap = "sha256:" + hex.EncodeToString(hash.Sum(nil))
	}
	var evidence ops.InstallationEvidence
	steps := []site.Step{
		{
			Name: "write-image",
			Send: func(context.Context) error {
				server, err := read(work)
				if err != nil {
					return err
				}
				server.Image, server.Booted, server.Bootstrap = spec.Image.Digest, "", bootstrap
				return write(work, server)
			},
			Observe: func(context.Context, json.RawMessage) (bool, error) {
				server, err := read(work)
				return server.Image == spec.Image.Digest && server.Bootstrap == bootstrap, err
			},
		},
		{
			Name: "boot-disk",
			Send: func(context.Context) error {
				server, err := read(work)
				if err != nil {
					return err
				}
				server.Booted = server.Image
				return write(work, server)
			},
			Observe: func(context.Context, json.RawMessage) (bool, error) {
				server, err := read(work)
				return server.Booted == spec.Image.Digest, err
			},
		},
	}
	for _, step := range steps {
		if err := work.Step(ctx, step); err != nil {
			return nil, err
		}
		at, _ := work.JournaledAt(step.Name)
		evidence.Steps = append(evidence.Steps, ops.StepEvidence{Name: step.Name, CompletedAt: metav1.NewTime(at.UTC())})
	}
	server, err := read(work)
	if err != nil {
		return nil, err
	}
	if server.Booted != spec.Image.Digest {
		return nil, &custody.Refusal{Reason: site.ReasonStep, Message: "the simulated server did not boot the installed image"}
	}
	evidence.InstallerVerifiedBootstrap = server.Bootstrap
	return evidence, nil
}

func wipeSpec(work *site.Work) (ops.DeviceWipeSpec, *custody.Refusal) {
	var spec ops.DeviceWipeSpec
	if err := json.Unmarshal(work.Inputs, &spec); err != nil || spec.CleaningPolicyDigest == "" {
		return spec, &custody.Refusal{Reason: custody.ReasonInputsUnreadable, Message: "the admitted inputs are not a DeviceWipe spec"}
	}
	return spec, nil
}

func (Wipe) Check(_ context.Context, work *site.Work) *custody.Refusal {
	spec, refusal := wipeSpec(work)
	if refusal != nil {
		return refusal
	}
	return checkDisks(work, spec.TargetDisks)
}

func (Wipe) Run(ctx context.Context, work *site.Work) (any, error) {
	spec, refusal := wipeSpec(work)
	if refusal != nil {
		return nil, refusal
	}
	step := site.Step{
		Name: "wipe",
		Send: func(context.Context) error { return write(work, Server{}) },
		Observe: func(context.Context, json.RawMessage) (bool, error) {
			server, err := read(work)
			return server == Server{}, err
		},
		// Wiping a wiped disk again changes nothing.
		Idempotent: true,
	}
	if err := work.Step(ctx, step); err != nil {
		return nil, err
	}
	at, _ := work.JournaledAt(step.Name)
	return ops.WipeEvidence{
		Steps:        []ops.StepEvidence{{Name: step.Name, CompletedAt: metav1.NewTime(at.UTC())}},
		WipedDisks:   slices.Clone(spec.TargetDisks),
		PolicyDigest: spec.CleaningPolicyDigest,
	}, nil
}
