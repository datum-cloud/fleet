package coordination

import (
	"context"
	"errors"
	"go.datum.net/fleet/internal/executor"
	"go.datum.net/fleet/internal/simulator"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	"testing"
)

func TestLocalMappingRequiresExactAssetAndRecoversDigest(t *testing.T) {
	i, s, _ := fixture()
	api := fake.NewSimpleDynamicClient(runtime.NewScheme(), object("private", i))
	translated := i.Request
	translated.AssetID = "local-physical-device"
	digest, _ := simulator.RequestDigest(translated)
	result := s.Reflected[0].Observation.Result
	result.InputDigest = digest
	backend := &effect{result: result}
	mapping := LocalAssetMapping{API: api, Namespace: "private", Cell: i.Cell, ScopedAsset: i.Request.AssetID, PhysicalAsset: translated.AssetID, Backend: backend}
	original, err := mapping.Execute(context.Background(), i.Request)
	if err != nil {
		t.Fatal(err)
	}
	if original.InputDigest != s.Reflected[0].Observation.Result.InputDigest {
		t.Fatal("lost original digest")
	}
	recovered, found, err := mapping.Lookup(context.Background(), i.Request.ID)
	if err != nil || !found || recovered.InputDigest != original.InputDigest {
		t.Fatal("restart lost exact mapping", err)
	}
	mapping.ScopedAsset = "another-project/same-device"
	if _, err := mapping.Execute(context.Background(), i.Request); !errors.Is(err, executor.ErrFenced) {
		t.Fatal("accepted alias", err)
	}
	if backend.calls != 1 {
		t.Fatal("duplicate execution")
	}
}
