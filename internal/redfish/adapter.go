// SPDX-License-Identifier: Apache-2.0

package redfish

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

var ErrResponseLost = errors.New("BMC may have applied the effect but its response was lost")

type System struct {
	PowerState string
	BootTarget string
}

type Adapter interface {
	Observe(context.Context) (System, error)
	SetOneTimeBoot(context.Context, string) error
	Reset(context.Context, string) error
	PhysicalTarget() string
}

// SushyAdapter calls exactly one internally selected sushy-emulator system.
// Customer URLs, Host headers, links, and IDs never enter these requests.
type SushyAdapter struct {
	BaseURL               string
	SystemID              string
	Username              string
	Password              string
	Client                *http.Client
	DropResetResponseOnce bool
	dropped               bool
}

func (a *SushyAdapter) Observe(ctx context.Context) (System, error) {
	var body struct {
		PowerState string `json:"PowerState"`
		Boot       struct {
			Target string `json:"BootSourceOverrideTarget"`
		} `json:"Boot"`
	}
	if err := a.doJSON(ctx, http.MethodGet, a.systemPath(), nil, &body); err != nil {
		return System{}, err
	}
	return System{PowerState: body.PowerState, BootTarget: physicalToVirtualBoot(body.Boot.Target)}, nil
}

func (a *SushyAdapter) SetOneTimeBoot(ctx context.Context, target string) error {
	physical, ok := map[string]string{"Pxe": "Pxe", "Hdd": "Hdd"}[target]
	if !ok {
		return fmt.Errorf("unsupported boot target %q", target)
	}
	body := map[string]any{"Boot": map[string]any{"BootSourceOverrideEnabled": "Once", "BootSourceOverrideTarget": physical}}
	return a.doJSON(ctx, http.MethodPatch, a.systemPath(), body, nil)
}

func (a *SushyAdapter) Reset(ctx context.Context, resetType string) error {
	if !supportedReset(resetType) {
		return fmt.Errorf("unsupported ResetType %q", resetType)
	}
	body := map[string]string{"ResetType": resetType}
	err := a.doJSON(ctx, http.MethodPost, a.systemPath()+"/Actions/ComputerSystem.Reset", body, nil)
	if err == nil && a.DropResetResponseOnce && !a.dropped {
		a.dropped = true
		return ErrResponseLost
	}
	return err
}

func (a *SushyAdapter) PhysicalTarget() string { return "bmc-system:" + a.SystemID }

func (a *SushyAdapter) systemPath() string {
	return "/redfish/v1/Systems/" + url.PathEscape(a.SystemID)
}

func (a *SushyAdapter) doJSON(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(a.BaseURL, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.SetBasicAuth(a.Username, a.Password)
	client := a.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("BMC unavailable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("BMC rejected the qualified operation with status %d", resp.StatusCode)
	}
	if out != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
			return fmt.Errorf("invalid BMC response: %w", err)
		}
	}
	return nil
}

func physicalToVirtualBoot(v string) string {
	if v == "Pxe" {
		return "Pxe"
	}
	return "Hdd"
}
func supportedReset(v string) bool {
	return v == "On" || v == "ForceOff" || v == "GracefulShutdown" || v == "GracefulRestart" || v == "ForceRestart"
}
