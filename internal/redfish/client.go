// Package redfish implements a Redfish provisioning executor for one site.
//
// It drives exact enrolled ComputerSystems through standard Redfish requests:
// service-root discovery, ComputerSystem.Reset and a one-time boot source
// override. It holds no hypervisor credential and never shells out. The local
// acceptance runs it against the DMTF Redfish Interface Emulator with a
// lab-only libvirt adapter (hack/redfish-lab); that adapter is not part of
// this package. See docs/operations/redfish-lab.md for what is verified and
// what remains hardware-unqualified.
//
// Redfish requests, schema decoding and collection paging use gofish. Fleet
// issues every mutation itself as one request: gofish's SetBoot (a second
// PATCH to the Settings resource on failure) and Reset (a resend without
// If-Match on 412) are deliberately not used.
package redfish

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
	"sync"
	"time"

	"github.com/stmcginnis/gofish"
	"github.com/stmcginnis/gofish/schemas"
)

const maxBody = 1 << 20

var (
	// ErrUnauthorized means the BMC rejected the enrolled credential.
	ErrUnauthorized = errors.New("redfish: credential rejected")
	// ErrUnsupported means a required action or value is not advertised.
	ErrUnsupported = errors.New("redfish: required capability not advertised")
	// ErrIdentity means the enrolled System is not exactly present.
	ErrIdentity = errors.New("redfish: enrolled system identity not matched exactly")
	// ErrPrecondition means the BMC refused a mutation's If-Match. It is not
	// retried without the precondition; the caller observes and decides.
	ErrPrecondition = errors.New("redfish: System changed since it was observed")
)

// Client is a minimal Redfish client over gofish. Its transport admits only
// /redfish/v1 paths on the configured endpoint, never follows redirects or an
// environment proxy, and bounds every response body.
type Client struct {
	config gofish.ClientConfig
	mu     sync.Mutex
	api    *gofish.APIClient
}

// NewClient accepts only an http(s)://IP:port endpoint without path or query.
// It performs no request until first use.
func NewClient(endpoint, user, password string, timeout time.Duration) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("redfish endpoint must be http(s)://IPv4:port")
	}
	port, err := strconv.Atoi(u.Port())
	if ip := net.ParseIP(u.Hostname()); ip == nil || ip.To4() == nil || err != nil || port < 1 || port > 65535 {
		return nil, errors.New("redfish endpoint must be http(s)://IPv4:port")
	}
	if user == "" || password == "" || strings.ContainsAny(user, ":\r\n") || strings.ContainsAny(password, "\r\n") {
		return nil, errors.New("redfish credential must be a nonempty single-line username and password")
	}
	if timeout <= 0 {
		return nil, errors.New("redfish client requires a positive timeout")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &Client{config: gofish.ClientConfig{
		Endpoint: u.Scheme + "://" + u.Host, Username: user, Password: password,
		// Basic authentication: creating a session would itself be a BMC
		// mutation with its own lifetime and cleanup.
		BasicAuth: true, NoModifyTransport: true,
		HTTPClient: &http.Client{Timeout: timeout, Transport: guard{scheme: u.Scheme, host: u.Host, next: transport},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}}, nil
}

func cleanPath(p string) (string, error) {
	if (p != "/redfish/v1" && !strings.HasPrefix(p, "/redfish/v1/")) || strings.Contains(p, "..") || strings.ContainsAny(p, "?#\\ ") {
		return "", fmt.Errorf("%w: unexpected resource path", ErrIdentity)
	}
	return strings.TrimSuffix(p, "/"), nil
}

// guard is the only path from gofish to the network. A link the BMC returns
// cannot direct a request to another host or outside /redfish/v1, and a
// mutation cannot carry a query.
type guard struct {
	scheme, host string
	next         http.RoundTripper
}

func (g guard) RoundTrip(r *http.Request) (*http.Response, error) {
	u := r.URL
	if _, err := cleanPath(u.Path); err != nil || u.Scheme != g.scheme || u.Host != g.host || u.User != nil || u.Fragment != "" || (u.RawQuery != "" && r.Method != http.MethodGet) {
		if r.Body != nil {
			r.Body.Close()
		}
		return nil, fmt.Errorf("%w: unexpected resource path", ErrIdentity)
	}
	r = r.Clone(r.Context())
	r.Header.Set("OData-Version", "4.0")
	resp, err := g.next.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	resp.Body = &bounded{ReadCloser: resp.Body, left: maxBody}
	return resp, nil
}

type bounded struct {
	io.ReadCloser
	left int64
}

func (b *bounded) Read(p []byte) (int, error) {
	if b.left <= 0 {
		return 0, errors.New("redfish: response exceeds size bound")
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.ReadCloser.Read(p)
	b.left -= int64(n)
	return n, err
}

// Status is a non-2xx Redfish response other than an authentication failure.
type Status struct{ Code int }

func (s Status) Error() string { return "redfish: HTTP " + strconv.Itoa(s.Code) }

// classify reduces a gofish error to Fleet's outcome classes. The result
// never carries a URL, header or response body.
func classify(err error) error {
	var coll *schemas.CollectionError
	if errors.As(err, &coll) {
		var worst error
		for _, e := range coll.Failures {
			e = classify(e)
			if worst == nil || errors.Is(e, ErrIdentity) || (errors.Is(e, ErrUnauthorized) && !errors.Is(worst, ErrIdentity)) {
				worst = e
			}
		}
		return worst
	}
	var rf *schemas.Error
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrIdentity):
		return fmt.Errorf("%w: unexpected resource path", ErrIdentity)
	case errors.As(err, &rf) && (rf.HTTPReturnedStatusCode == http.StatusUnauthorized || rf.HTTPReturnedStatusCode == http.StatusForbidden):
		return ErrUnauthorized
	case errors.As(err, &rf) && rf.HTTPReturnedStatusCode == http.StatusPreconditionFailed:
		return ErrPrecondition
	case errors.As(err, &rf) && rf.HTTPReturnedStatusCode != 0:
		return Status{rf.HTTPReturnedStatusCode}
	}
	// Transport failure, unreadable or oversized response, or a body gofish
	// cannot decode as the expected schema.
	return errors.New("redfish: transport failure or unreadable response")
}

// connect reads the unauthenticated service root once and binds the
// stateless basic-auth client to each call's context.
func (c *Client) connect(ctx context.Context) (*gofish.APIClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.api == nil {
		api, err := gofish.ConnectContext(ctx, c.config)
		if err != nil {
			return nil, classify(err)
		}
		c.api = api
	}
	return c.api.WithContext(ctx), nil
}

func contains[T ~string](list []T, v string) bool {
	for _, x := range list {
		if string(x) == v {
			return true
		}
	}
	return false
}

// System is the discovered, retained identity and capability of one target.
type System struct {
	Path        string   `json:"path"`
	ID          string   `json:"id"`
	UUID        string   `json:"uuid"`
	ResetTarget string   `json:"resetTarget"`
	ResetTypes  []string `json:"resetTypes"`
	BootTargets []string `json:"bootTargets"`
}

// Power is one observation of a System's power and boot override state.
type Power struct {
	State           string `json:"state"`
	OverrideEnabled string `json:"overrideEnabled"`
	OverrideTarget  string `json:"overrideTarget"`
	etag            string
}

// resetAction holds the annotations gofish decodes into unexported fields.
type resetAction struct {
	Actions struct {
		Reset struct {
			Target  string   `json:"target"`
			Info    string   `json:"@Redfish.ActionInfo"`
			Allowed []string `json:"ResetType@Redfish.AllowableValues"`
		} `json:"#ComputerSystem.Reset"`
	}
	Boot struct {
		Enabled *[]string `json:"BootSourceOverrideEnabled@Redfish.AllowableValues"`
	}
}

// Discover walks the service root to the enrolled System. Every member of the
// Systems collection, across all pages, must be read: exactly one must carry
// the enrolled UUID, and it must have the enrolled Id; a member with the
// enrolled Id but another UUID is a renumbered or swapped target. The reset
// target and ActionInfo must belong to that System, and On, ForceOff, Pxe and
// Hdd must be advertised. Discovery issues GETs only.
func (c *Client) Discover(ctx context.Context, t Target) (System, error) {
	api, err := c.connect(ctx)
	if err != nil {
		return System{}, err
	}
	// gofish keeps the Systems link unexported; read the root once more to
	// validate it, then walk exactly that collection.
	resp, err := api.Get(schemas.DefaultServiceRoot)
	var root struct {
		Systems struct {
			ID string `json:"@odata.id"`
		}
	}
	if err == nil {
		err = json.NewDecoder(resp.Body).Decode(&root)
	}
	schemas.DeferredCleanupHTTPResponse(resp)
	if err != nil {
		return System{}, classify(err)
	}
	if root.Systems.ID == "" {
		return System{}, fmt.Errorf("%w: service root has no Systems collection", ErrUnsupported)
	}
	collection, err := cleanPath(root.Systems.ID)
	if err != nil {
		return System{}, err
	}
	members, err := schemas.GetCollectionObjects[schemas.ComputerSystem](api, collection)
	if err != nil {
		// A partially read collection cannot prove the UUID is unique.
		return System{}, classify(err)
	}
	var found []*schemas.ComputerSystem
	for _, s := range members {
		p, err := cleanPath(s.ODataID)
		if err != nil || !strings.HasPrefix(p, collection+"/") {
			return System{}, fmt.Errorf("%w: system member outside the collection", ErrIdentity)
		}
		if strings.EqualFold(s.UUID, t.SystemUUID) {
			found = append(found, s)
		} else if s.ID == t.SystemID {
			return System{}, fmt.Errorf("%w: enrolled Id reports another UUID", ErrIdentity)
		}
	}
	if len(found) != 1 || found[0].ID != t.SystemID {
		return System{}, fmt.Errorf("%w: %d members carry the enrolled UUID", ErrIdentity, len(found))
	}
	s := found[0]
	sys := System{ID: t.SystemID, UUID: t.SystemUUID}
	sys.Path, _ = cleanPath(s.ODataID)
	var raw resetAction
	if err := json.Unmarshal(s.RawData, &raw); err != nil {
		return System{}, classify(err)
	}
	reset := raw.Actions.Reset
	target, err := cleanPath(reset.Target)
	if err != nil || !strings.HasPrefix(target, sys.Path+"/") {
		return System{}, fmt.Errorf("%w: ComputerSystem.Reset target not on the enrolled System", ErrUnsupported)
	}
	sys.ResetTarget = target
	sys.ResetTypes = reset.Allowed
	if len(sys.ResetTypes) == 0 && reset.Info != "" {
		info, err := cleanPath(reset.Info)
		if err != nil || !strings.HasPrefix(info, sys.Path+"/") {
			return System{}, fmt.Errorf("%w: ActionInfo not on the enrolled System", ErrUnsupported)
		}
		// Not schemas.ActionInfo: its typed decode rejects the whole document
		// for one off-schema property, as the DMTF emulator's string
		// "Required": "true". Only the ResetType values are read.
		resp, err := api.Get(info)
		var ai struct {
			Parameters []struct {
				Name, DataType  string
				AllowableValues []string
			}
		}
		if err == nil {
			err = json.NewDecoder(resp.Body).Decode(&ai)
		}
		schemas.DeferredCleanupHTTPResponse(resp)
		if err != nil {
			return System{}, classify(err)
		}
		for _, p := range ai.Parameters {
			if p.Name == "ResetType" && (p.DataType == "" || p.DataType == "String") {
				sys.ResetTypes = p.AllowableValues
			}
		}
	}
	for _, b := range s.Boot.AllowableBootSourceOverrideTargetValues {
		sys.BootTargets = append(sys.BootTargets, string(b))
	}
	for _, need := range []string{"On", "ForceOff"} {
		if !contains(sys.ResetTypes, need) {
			return System{}, fmt.Errorf("%w: ResetType %s", ErrUnsupported, need)
		}
	}
	for _, need := range []string{"Pxe", "Hdd"} {
		if !contains(sys.BootTargets, need) {
			return System{}, fmt.Errorf("%w: boot override target %s", ErrUnsupported, need)
		}
	}
	if raw.Boot.Enabled != nil && !contains(*raw.Boot.Enabled, "Once") {
		return System{}, fmt.Errorf("%w: one-time boot override", ErrUnsupported)
	}
	return sys, nil
}

func observe(api *gofish.APIClient, sys System) (Power, error) {
	s, err := schemas.GetObject[schemas.ComputerSystem](api, sys.Path)
	if err != nil {
		return Power{}, classify(err)
	}
	if s.ID != sys.ID || !strings.EqualFold(s.UUID, sys.UUID) {
		return Power{}, fmt.Errorf("%w: retained System path changed identity", ErrIdentity)
	}
	return Power{State: string(s.PowerState), OverrideEnabled: string(s.Boot.BootSourceOverrideEnabled),
		OverrideTarget: string(s.Boot.BootSourceOverrideTarget), etag: s.ODataEtag}, nil
}

// Observe reads the retained System again and re-checks its identity.
func (c *Client) Observe(ctx context.Context, sys System) (Power, error) {
	api, err := c.connect(ctx)
	if err != nil {
		return Power{}, err
	}
	return observe(api, sys)
}

// SetBootOnce observes the System, sends exactly one PATCH of the one-time
// boot override (conditional on the observed ETag when the BMC provides one),
// and reads it back. A refused precondition is returned, not retried.
func (c *Client) SetBootOnce(ctx context.Context, sys System, target string) error {
	if !contains(sys.BootTargets, target) {
		return fmt.Errorf("%w: boot override target %s", ErrUnsupported, target)
	}
	api, err := c.connect(ctx)
	if err != nil {
		return err
	}
	before, err := observe(api, sys)
	if err != nil {
		return err
	}
	headers := map[string]string{}
	if before.etag != "" {
		headers["If-Match"] = before.etag
	}
	resp, err := api.PatchWithHeaders(sys.Path, map[string]any{"Boot": map[string]string{"BootSourceOverrideTarget": target, "BootSourceOverrideEnabled": "Once"}}, headers)
	schemas.DeferredCleanupHTTPResponse(resp)
	if err != nil {
		return classify(err)
	}
	p, err := observe(api, sys)
	if err != nil {
		return err
	}
	if p.OverrideTarget != target || p.OverrideEnabled != "Once" {
		return errors.New("redfish: boot override not observed after PATCH")
	}
	return nil
}

// Reset posts exactly one ComputerSystem.Reset to the discovered action
// target. HTTP success, including 202 with a task monitor, is not evidence of
// the effect; callers observe the System.
func (c *Client) Reset(ctx context.Context, sys System, kind string) error {
	if !contains(sys.ResetTypes, kind) {
		return fmt.Errorf("%w: ResetType %s", ErrUnsupported, kind)
	}
	api, err := c.connect(ctx)
	if err != nil {
		return err
	}
	resp, err := api.PostWithHeaders(sys.ResetTarget, map[string]string{"ResetType": kind}, nil)
	schemas.DeferredCleanupHTTPResponse(resp)
	return classify(err)
}
