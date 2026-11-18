package main

import (
	"flag"
	"io"
	"testing"
)

func TestRequestedHardwareProjectorFlagDefaultsToEnabled(t *testing.T) {
	flags := flag.NewFlagSet("fleet-manager", flag.ContinueOnError)
	enabled := registerRequestedHardwareProjectorFlag(flags)
	if err := flags.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if !*enabled {
		t.Fatal("projector must stay enabled by default so existing deployments keep their single writer")
	}
}

func TestRequestedHardwareProjectorFlagDisables(t *testing.T) {
	flags := flag.NewFlagSet("fleet-manager", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	enabled := registerRequestedHardwareProjectorFlag(flags)
	if err := flags.Parse([]string{"--enable-requested-hardware-projector=false"}); err != nil {
		t.Fatal(err)
	}
	if *enabled {
		t.Fatal("--enable-requested-hardware-projector=false did not disable the projector")
	}
}
