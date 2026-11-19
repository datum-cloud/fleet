// Package netbox is a read-only NetBox REST client for the inventory observer.
// It issues GET requests only, decodes an allowlist of device and interface
// fields, and never follows server-supplied URLs, so the API token is sent only
// to the configured origin.
package netbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	ErrNotFound     = errors.New("netbox object not found")
	ErrUnauthorized = errors.New("netbox rejected the credential")
	ErrUnavailable  = errors.New("netbox unavailable")
)

const (
	maxBodyBytes  = 4 << 20
	pageSize      = 200
	maxInterfaces = 2000
)

// Client reads one NetBox origin. Token is called for every request so a
// rotated credential file takes effect without restarting the process.
type Client struct {
	base  *url.URL
	token func() (string, error)
	http  *http.Client
}

// NewClient validates the origin. HTTPS is required except for loopback hosts,
// which exist only for tests and local development.
func NewClient(origin string, token func() (string, error), httpClient *http.Client) (*Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("netbox origin must be an absolute URL without credentials, query, or fragment")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && loopback(parsed.Hostname())) {
		return nil, fmt.Errorf("netbox origin must use https")
	}
	parsed.Path = strings.TrimSuffix(strings.TrimSuffix(parsed.Path, "/"), "/api")
	if token == nil {
		return nil, fmt.Errorf("netbox token source is required")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	copied := *httpClient
	copied.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{base: parsed, token: token, http: &copied}, nil
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Ref is a nested NetBox object or choice reference.
type Ref struct {
	Name  string `json:"name"`
	Slug  string `json:"slug"`
	Value string `json:"value"`
}

// IPRef is a nested NetBox IP address reference.
type IPRef struct {
	Address string `json:"address"`
}

// Device is the allowlisted subset of a NetBox dcim device.
type Device struct {
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	Serial     string   `json:"serial"`
	AssetTag   *string  `json:"asset_tag"`
	Position   *float64 `json:"position"`
	DeviceType struct {
		Model        string `json:"model"`
		Manufacturer Ref    `json:"manufacturer"`
	} `json:"device_type"`
	Role        *Ref       `json:"role"`
	Site        *Ref       `json:"site"`
	Rack        *Ref       `json:"rack"`
	Face        *Ref       `json:"face"`
	Status      *Ref       `json:"status"`
	PrimaryIP4  *IPRef     `json:"primary_ip4"`
	PrimaryIP6  *IPRef     `json:"primary_ip6"`
	OOBIP       *IPRef     `json:"oob_ip"`
	LastUpdated *time.Time `json:"last_updated"`
}

// Interface is the allowlisted subset of a NetBox dcim interface.
type Interface struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Type     *Ref   `json:"type"`
	Speed    *int64 `json:"speed"`
	Enabled  bool   `json:"enabled"`
	MgmtOnly bool   `json:"mgmt_only"`
	LAG      *Ref   `json:"lag"`
	// MACAddress is the pre-4.2 field; MACAddresses and PrimaryMAC replace it.
	MACAddress   *string `json:"mac_address"`
	MACAddresses []struct {
		MACAddress string `json:"mac_address"`
	} `json:"mac_addresses"`
	PrimaryMAC *struct {
		MACAddress string `json:"mac_address"`
	} `json:"primary_mac_address"`
	LinkPeersType string `json:"link_peers_type"`
	LinkPeers     []struct {
		Name    string `json:"name"`
		Display string `json:"display"`
		Device  *Ref   `json:"device"`
	} `json:"link_peers"`
}

// Device reads one device by its stable NetBox ID.
func (c *Client) Device(ctx context.Context, id int64) (*Device, error) {
	if id <= 0 {
		return nil, fmt.Errorf("netbox device id must be positive")
	}
	var device Device
	if err := c.get(ctx, "dcim/devices/"+strconv.FormatInt(id, 10)+"/", nil, &device); err != nil {
		return nil, err
	}
	if device.ID != id {
		return nil, fmt.Errorf("%w: response names device %d, requested %d", ErrUnavailable, device.ID, id)
	}
	return &device, nil
}

// Interfaces reads every interface of one device using offset pagination it
// computes itself; the response's next URL is never followed.
func (c *Client) Interfaces(ctx context.Context, deviceID int64) ([]Interface, error) {
	var out []Interface
	for offset := 0; ; offset += pageSize {
		var page struct {
			Count   int         `json:"count"`
			Results []Interface `json:"results"`
		}
		query := url.Values{"device_id": {strconv.FormatInt(deviceID, 10)}, "limit": {strconv.Itoa(pageSize)}, "offset": {strconv.Itoa(offset)}, "ordering": {"name"}}
		if err := c.get(ctx, "dcim/interfaces/", query, &page); err != nil {
			return nil, err
		}
		if page.Count > maxInterfaces {
			return nil, fmt.Errorf("%w: device reports %d interfaces, above the %d limit", ErrUnavailable, page.Count, maxInterfaces)
		}
		out = append(out, page.Results...)
		if len(page.Results) == 0 || len(out) >= page.Count {
			if len(out) != page.Count {
				return nil, fmt.Errorf("%w: interface count changed during pagination", ErrUnavailable)
			}
			return out, nil
		}
	}
}

func (c *Client) get(ctx context.Context, path string, query url.Values, into any) error {
	token, err := c.token()
	if err != nil {
		return fmt.Errorf("read netbox token: %w", err)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("%w: empty token", ErrUnauthorized)
	}
	target := *c.base
	target.Path = target.Path + "/api/" + path
	target.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return err
	}
	scheme := "Token"
	if strings.HasPrefix(token, "nbt_") {
		scheme = "Bearer"
	}
	request.Header.Set("Authorization", scheme+" "+token)
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("%w: %s %s: %v", ErrUnavailable, request.Method, target.Path, redactedError(err))
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusOK:
	case response.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return ErrUnauthorized
	default:
		return fmt.Errorf("%w: %s returned HTTP %d", ErrUnavailable, target.Path, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes+1))
	if err != nil {
		return fmt.Errorf("%w: read %s: %v", ErrUnavailable, target.Path, err)
	}
	if len(body) > maxBodyBytes {
		return fmt.Errorf("%w: %s response exceeds %d bytes", ErrUnavailable, target.Path, maxBodyBytes)
	}
	if err := json.Unmarshal(body, into); err != nil {
		return fmt.Errorf("%w: decode %s: %v", ErrUnavailable, target.Path, err)
	}
	return nil
}

// redactedError drops the URL from transport errors; the query and path are
// already reported by the caller and the URL never carries the token, but this
// keeps logs stable across retries.
func redactedError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}
