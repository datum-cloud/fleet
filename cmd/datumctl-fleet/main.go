// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"os"

	"go.datum.net/datumctl/plugin"

	fleetcmd "go.datum.net/fleet/internal/cmd/fleet"
)

var version = "dev"

func main() {
	plugin.ServeManifest(plugin.Manifest{Name: "fleet", Version: version, Description: "Manage Datum Fleet machines and secure management sessions", APIVersion: 1, MinAPIVersion: 1})
	if err := fleetcmd.Command().Execute(); err != nil {
		var exit *fleetcmd.ExitError
		if errors.As(err, &exit) {
			fmt.Fprintln(os.Stderr, "Error:", exit.Err)
			os.Exit(exit.Code)
		}
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
