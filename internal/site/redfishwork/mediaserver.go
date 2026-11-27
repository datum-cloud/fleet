package redfishwork

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MediaServer is the central media service. It is not part of any site: it
// keeps its own windows and takes each Device's fence from custody, the
// authority every site follows. It serves virtual media to BMCs only through
// open windows, and takes the install report the installed OS posts at
// first boot.
//
// A window belongs to one admitted operation at one fence. The service
// opens it only while custody names that operation active at that fence,
// and serves it only while that is still true, so a fence rise closes every
// window of the Device without a worker. It serves the boot medium by its
// digest and the config medium by its URL capability until the fetch
// deadline, and takes one report carrying the window's report token until
// the report deadline. It records each transfer's start and end, and each
// report's outcome, without URLs, capabilities or tokens.
type MediaServer struct {
	// Root holds the service's own state: windows, reports and transfers.
	Root string
	// Artifacts holds the boot medium and the manifest that pins it.
	Artifacts string
	// Custody reads which operation custody names active for a Device.
	Custody CustodyReader
	// Clients are the networks BMCs and installed servers reach the
	// service from. Requests from any other address are refused.
	Clients []netip.Prefix
	// Workers authorizes the worker API: it reports whether the verified
	// client certificate identity may open, close and read windows of the
	// Device. The TLS listener must verify client certificates against the
	// site workers' CA when one is presented; BMCs and installed servers
	// present none and use only the media and report endpoints.
	Workers func(identity, device string) bool
	Now     func() time.Time
	mu      sync.Mutex
}

// CustodyReader reads custody's active operation for a "<namespace>/<name>"
// Device: its UID and fence, and false when none is active.
type CustodyReader interface {
	Active(ctx context.Context, device string) (uid string, fence int64, ok bool, err error)
}

// Window is one operation's media window as the media service keeps it.
type Window struct {
	Device    string `json:"device"`
	Operation string `json:"operation"`
	Fence     int64  `json:"fence"`
	// BootDigest is the boot medium artifact's SHA-256, from the image's
	// pinned manifest.
	BootDigest string `json:"bootDigest"`
	// CapabilitySHA256 and TokenSHA256 are digests of the config URL's
	// capability and of the report token. Neither value is kept.
	CapabilitySHA256 string `json:"capabilitySHA256"`
	TokenSHA256      string `json:"tokenSHA256"`
	// ConfigMedium is the rendered config medium, kept only while the
	// window is open.
	ConfigMedium []byte `json:"configMedium,omitempty"`
	// ReportFrom is where the installed OS's report may come from, as the
	// media service sees it. A report from any other address is refused.
	ReportFrom    []string  `json:"reportFrom"`
	Opened        time.Time `json:"opened"`
	FetchExpires  time.Time `json:"fetchExpires"`
	ReportExpires time.Time `json:"reportExpires"`
	Closed        bool      `json:"closed,omitempty"`
	// Report is the first report that carried the window's token.
	Report *ReceivedReport `json:"report,omitempty"`
	// Duplicates counts further, different reports with the window's
	// token; the token is single use. Rejected counts reports refused for another or
	// a missing token, or after the window ended.
	Duplicates int `json:"duplicates,omitempty"`
	Rejected   int `json:"rejected,omitempty"`
}

// ReceivedReport is an install report the media service took.
type ReceivedReport struct {
	Image     string `json:"image"`
	Bootstrap string `json:"bootstrap"`
	// Downloads are what the installer reports it downloaded.
	Downloads  []ReportedDownload `json:"downloads,omitempty"`
	ReceivedAt time.Time          `json:"receivedAt"`
}

// ReportedDownload is one artifact the installer downloaded, by the digest
// it fetched.
type ReportedDownload struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

// WindowStatus is what the media service reports about a window. It
// carries no capability, token or medium.
type WindowStatus struct {
	FetchOpen     bool            `json:"fetchOpen"`
	ReportOpen    bool            `json:"reportOpen"`
	Closed        bool            `json:"closed"`
	Report        *ReceivedReport `json:"report,omitempty"`
	Duplicates    int             `json:"duplicates"`
	Rejected      int             `json:"rejected"`
	OpenTransfers int             `json:"openTransfers"`
	// retire means the window keeps a config medium it can never serve.
	retire bool
}

// DeviceStatus is the media service's release report for a Device.
type DeviceStatus struct {
	Windows       int `json:"windows"`
	OpenWindows   int `json:"openWindows"`
	OpenTransfers int `json:"openTransfers"`
	Duplicates    int `json:"duplicates"`
}

func (m *MediaServer) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func validKey(key string) bool {
	if len(key) != 16 {
		return false
	}
	_, err := hex.DecodeString(key)
	return err == nil
}

func (m *MediaServer) load(key string) (Window, bool, error) {
	var window Window
	if !validKey(key) {
		return window, false, nil
	}
	data, err := os.ReadFile(filepath.Join(m.Root, "windows", key+".json"))
	if os.IsNotExist(err) {
		return window, false, nil
	}
	if err != nil {
		return window, false, err
	}
	return window, true, json.Unmarshal(data, &window)
}

func (m *MediaServer) save(key string, window Window) error {
	return writeFile(filepath.Join(m.Root, "windows"), key+".json", window)
}

func writeFile(dir, name string, value any) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, name))
}

// current reports whether custody still names the window's operation
// active at the window's fence.
func (m *MediaServer) current(ctx context.Context, window Window) (bool, error) {
	uid, fence, ok, err := m.Custody.Active(ctx, window.Device)
	if err != nil {
		return false, err
	}
	return ok && uid == window.Operation && fence == window.Fence, nil
}

func (m *MediaServer) status(ctx context.Context, key string, window Window) (WindowStatus, error) {
	current, err := m.current(ctx, window)
	if err != nil {
		return WindowStatus{}, err
	}
	// A window that can never serve again keeps no config medium, and so
	// no report token, even if no worker closed it. The caller drops it.
	retire := len(window.ConfigMedium) > 0 && (!current || !m.now().Before(window.ReportExpires))
	open, err := m.openTransfers(key)
	if err != nil {
		return WindowStatus{}, err
	}
	now := m.now()
	live := current && !window.Closed
	return WindowStatus{FetchOpen: live && now.Before(window.FetchExpires), ReportOpen: live && now.Before(window.ReportExpires), Closed: window.Closed,
		Report: window.Report, Duplicates: window.Duplicates, Rejected: window.Rejected, OpenTransfers: open, retire: retire}, nil
}

func (m *MediaServer) client(request *http.Request) bool {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		return false
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	for _, prefix := range m.Clients {
		if prefix.Contains(address.Unmap()) {
			return true
		}
	}
	return false
}

// OpenRequest is a worker's request to open its operation's window.
type OpenRequest struct {
	Device           string `json:"device"`
	Operation        string `json:"operation"`
	Fence            int64  `json:"fence"`
	BootDigest       string `json:"bootDigest"`
	CapabilitySHA256 string `json:"capabilitySHA256"`
	TokenSHA256      string `json:"tokenSHA256"`
	ConfigMedium     []byte `json:"configMedium"`
	// FetchExpires ends the media fetch, and ReportExpires the first-boot
	// report. Neither is later than the operation's deadline.
	FetchExpires  time.Time `json:"fetchExpires"`
	ReportExpires time.Time `json:"reportExpires"`
	// ReportFrom is the IPv4 or IPv6 prefixes the report may come from.
	ReportFrom []string `json:"reportFrom"`
}

// ErrMediaStale means the media service refused to open a window because
// custody does not name the operation active at that fence.
var ErrMediaStale = errors.New("media service: the operation does not hold the Device's fence")

// ErrMediaUnauthorized means the media service refused the site worker's
// client certificate, or found none.
var ErrMediaUnauthorized = errors.New("media service: the site worker is not authorized")

// Handler routes the worker API, the media and the report endpoint. It
// never serves a caller-selected path.
func (m *MediaServer) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if request.URL.RawQuery != "" || !m.client(request) {
			http.Error(w, "denied", http.StatusForbidden)
			return
		}
		parts := strings.Split(strings.TrimPrefix(request.URL.Path, "/"), "/")
		if parts[0] == "v1" && !m.authorized(w, request, parts) {
			return
		}
		switch {
		case len(parts) == 3 && parts[0] == "v1" && parts[1] == "windows" && request.Method == http.MethodPut:
			m.open(w, request, parts[2])
		case len(parts) == 4 && parts[0] == "v1" && parts[1] == "windows" && parts[3] == "close" && request.Method == http.MethodPost:
			m.close(w, parts[2])
		case len(parts) == 3 && parts[0] == "v1" && parts[1] == "windows" && request.Method == http.MethodGet:
			m.windowStatus(w, request, parts[2])
		case len(parts) == 4 && parts[0] == "v1" && parts[1] == "devices" && request.Method == http.MethodGet:
			m.deviceStatus(w, request, parts[2]+"/"+parts[3])
		case len(parts) == 2 && parts[0] == "report" && request.Method == http.MethodPost:
			m.report(w, request, parts[1])
		case len(parts) == 5 && parts[0] == "media" && (request.Method == http.MethodGet || request.Method == http.MethodHead):
			m.media(w, request, parts[1], parts[2], parts[3], parts[4])
		default:
			http.Error(w, "denied", http.StatusForbidden)
		}
	})
}

// ClientIdentity is the common name of the client certificate the TLS
// listener verified, or empty when the client presented none.
func ClientIdentity(request *http.Request) string {
	if request.TLS == nil || len(request.TLS.VerifiedChains) == 0 || len(request.TLS.VerifiedChains[0]) == 0 {
		return ""
	}
	return request.TLS.VerifiedChains[0][0].Subject.CommonName
}

// authorized refuses a worker API request from a client without a verified
// certificate, 401, or whose identity may not act on the Device, 403. The
// Device is the path's for a Device status, the window's own otherwise, and
// for an open the one the request names.
func (m *MediaServer) authorized(w http.ResponseWriter, request *http.Request, parts []string) bool {
	identity := ClientIdentity(request)
	if identity == "" {
		http.Error(w, "a site worker client certificate is required", http.StatusUnauthorized)
		return false
	}
	var device string
	switch {
	case len(parts) == 4 && parts[1] == "devices":
		device = parts[2] + "/" + parts[3]
	case len(parts) >= 3 && parts[1] == "windows" && request.Method == http.MethodPut:
		var open OpenRequest
		body, err := io.ReadAll(io.LimitReader(request.Body, 64<<10))
		if err != nil || json.Unmarshal(body, &open) != nil {
			http.Error(w, "invalid window", http.StatusBadRequest)
			return false
		}
		device = open.Device
		request.Body = io.NopCloser(bytes.NewReader(body))
	case len(parts) >= 3 && parts[1] == "windows":
		m.mu.Lock()
		window, found, err := m.load(parts[2])
		m.mu.Unlock()
		if err != nil || !found {
			http.Error(w, "no such window", http.StatusNotFound)
			return false
		}
		device = window.Device
	}
	if device == "" || m.Workers == nil || !m.Workers(identity, device) {
		http.Error(w, "this client may not act on the Device", http.StatusForbidden)
		return false
	}
	return true
}

// SweepResult is what one sweep pass did.
type SweepResult struct {
	// Dropped counts the config media it dropped.
	Dropped int
	// Skipped counts the windows it could not read or judge, such as an
	// unreadable window file or custody it could not read. The pass goes
	// on past each one; the next pass tries it again.
	Skipped int
}

// Sweep drops the config medium, and so the report token, of every window
// that can never serve again: custody no longer names its operation, or its
// report deadline passed. A window it cannot read or judge is counted and
// skipped, so one bad file never keeps every later window's token at rest.
// It returns an error, after the pass, when it skipped any.
func (m *MediaServer) Sweep(ctx context.Context) (SweepResult, error) {
	var result SweepResult
	paths, err := filepath.Glob(filepath.Join(m.Root, "windows", "*.json"))
	if err != nil {
		return result, err
	}
	var first error
	skip := func(key string, err error) {
		result.Skipped++
		if first == nil {
			first = fmt.Errorf("window %s: %w", key, err)
		}
	}
	for _, path := range paths {
		key := strings.TrimSuffix(filepath.Base(path), ".json")
		m.mu.Lock()
		window, found, err := m.load(key)
		m.mu.Unlock()
		switch {
		case err != nil:
			skip(key, err)
			continue
		case !found:
			// Gone since the listing: nothing left to drop.
			continue
		}
		status, err := m.status(ctx, key, window)
		if err != nil {
			skip(key, err)
			continue
		}
		if status.retire {
			if err := m.drop(key); err != nil {
				skip(key, err)
				continue
			}
			result.Dropped++
		}
	}
	if first != nil {
		return result, fmt.Errorf("the sweep skipped %d windows; first: %w", result.Skipped, first)
	}
	return result, nil
}

// RunSweeps sweeps every interval until ctx ends, so no window keeps a
// token at rest only because nothing reads it again.
func (m *MediaServer) RunSweeps(ctx context.Context, interval time.Duration, onError func(error)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := m.Sweep(ctx); err != nil && onError != nil {
				onError(err)
			}
		}
	}
}

func reply(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}

func (m *MediaServer) open(w http.ResponseWriter, request *http.Request, key string) {
	var open OpenRequest
	decoder := json.NewDecoder(io.LimitReader(request.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&open); err != nil || key != Key(open.Operation) || open.Device == "" || open.Fence < 1 || len(open.ConfigMedium) == 0 || len(open.ConfigMedium) > configMediumSize ||
		len(open.CapabilitySHA256) != 64 || len(open.TokenSHA256) != 64 || len(open.BootDigest) != 64 ||
		!m.now().Before(open.FetchExpires) || open.ReportExpires.Before(open.FetchExpires) || !validPrefixes(open.ReportFrom) {
		http.Error(w, "invalid window", http.StatusBadRequest)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	window := Window{Device: open.Device, Operation: open.Operation, Fence: open.Fence, BootDigest: open.BootDigest, CapabilitySHA256: open.CapabilitySHA256, TokenSHA256: open.TokenSHA256,
		ReportFrom: open.ReportFrom}
	if current, err := m.current(request.Context(), window); err != nil {
		http.Error(w, "custody unreadable", http.StatusServiceUnavailable)
		return
	} else if !current {
		http.Error(w, ErrMediaStale.Error(), http.StatusConflict)
		return
	}
	existing, found, err := m.load(key)
	if err != nil {
		http.Error(w, "state unreadable", http.StatusInternalServerError)
		return
	}
	if found {
		// A replayed open of the same window is answered again; any other
		// is refused. A window is opened once.
		if existing.Operation == open.Operation && existing.Fence == open.Fence && existing.TokenSHA256 == open.TokenSHA256 && existing.CapabilitySHA256 == open.CapabilitySHA256 && !existing.Closed {
			reply(w, http.StatusOK, map[string]string{"window": key})
			return
		}
		http.Error(w, "the window was already opened", http.StatusConflict)
		return
	}
	now := m.now().UTC()
	window.ConfigMedium = open.ConfigMedium
	window.Opened = now
	window.FetchExpires, window.ReportExpires = open.FetchExpires.UTC(), open.ReportExpires.UTC()
	if err := m.save(key, window); err != nil {
		http.Error(w, "state unwritable", http.StatusInternalServerError)
		return
	}
	reply(w, http.StatusCreated, map[string]string{"window": key})
}

// drop removes a window's config medium.
func (m *MediaServer) drop(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	window, found, err := m.load(key)
	if err != nil || !found || window.ConfigMedium == nil {
		return err
	}
	window.ConfigMedium = nil
	return m.save(key, window)
}

// close closes a window and drops its config medium. Closing needs no
// fence: it only takes delivery away.
func (m *MediaServer) close(w http.ResponseWriter, key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	window, found, err := m.load(key)
	switch {
	case err != nil:
		http.Error(w, "state unreadable", http.StatusInternalServerError)
		return
	case !found:
		http.Error(w, "no such window", http.StatusNotFound)
		return
	}
	window.Closed, window.ConfigMedium = true, nil
	if err := m.save(key, window); err != nil {
		http.Error(w, "state unwritable", http.StatusInternalServerError)
		return
	}
	reply(w, http.StatusOK, map[string]string{"window": key})
}

func (m *MediaServer) windowStatus(w http.ResponseWriter, request *http.Request, key string) {
	m.mu.Lock()
	window, found, err := m.load(key)
	m.mu.Unlock()
	switch {
	case err != nil:
		http.Error(w, "state unreadable", http.StatusInternalServerError)
		return
	case !found:
		http.Error(w, "no such window", http.StatusNotFound)
		return
	}
	status, err := m.status(request.Context(), key, window)
	if err == nil && status.retire {
		err = m.drop(key)
	}
	if err != nil {
		http.Error(w, "custody unreadable", http.StatusServiceUnavailable)
		return
	}
	reply(w, http.StatusOK, status)
}

// deviceStatus covers every window the service ever opened for the Device,
// so release also covers windows of earlier, held operations.
func (m *MediaServer) deviceStatus(w http.ResponseWriter, request *http.Request, device string) {
	paths, err := filepath.Glob(filepath.Join(m.Root, "windows", "*.json"))
	if err != nil {
		http.Error(w, "state unreadable", http.StatusInternalServerError)
		return
	}
	var out DeviceStatus
	for _, path := range paths {
		key := strings.TrimSuffix(filepath.Base(path), ".json")
		m.mu.Lock()
		window, _, err := m.load(key)
		m.mu.Unlock()
		if err != nil {
			http.Error(w, "state unreadable", http.StatusInternalServerError)
			return
		}
		if window.Device != device {
			continue
		}
		status, err := m.status(request.Context(), key, window)
		if err == nil && status.retire {
			err = m.drop(key)
		}
		if err != nil {
			http.Error(w, "custody unreadable", http.StatusServiceUnavailable)
			return
		}
		out.Windows++
		if status.FetchOpen || status.ReportOpen {
			out.OpenWindows++
		}
		out.OpenTransfers += status.OpenTransfers
		out.Duplicates += status.Duplicates
	}
	reply(w, http.StatusOK, out)
}

// InstallReport is the report the installed OS posts at first boot.
type InstallReport struct {
	Token     string             `json:"token"`
	Image     string             `json:"image"`
	Bootstrap string             `json:"bootstrap"`
	Downloads []ReportedDownload `json:"downloads,omitempty"`
}

func validPrefixes(prefixes []string) bool {
	for _, value := range prefixes {
		if _, err := netip.ParsePrefix(value); err != nil {
			return false
		}
	}
	return len(prefixes) > 0
}

// fromAny reports whether the request's source address is in one of the
// prefixes.
func fromAny(request *http.Request, prefixes []string) bool {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		return false
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	for _, value := range prefixes {
		if prefix, err := netip.ParsePrefix(value); err == nil && prefix.Contains(address.Unmap()) {
			return true
		}
	}
	return false
}

// report takes one install report per window: it must carry the window's
// token, arrive before the report deadline, and come while custody still
// names the operation. A refused or repeated report is counted, never
// taken, and the token is never recorded.
func (m *MediaServer) report(w http.ResponseWriter, request *http.Request, key string) {
	var report InstallReport
	decoder := json.NewDecoder(io.LimitReader(request.Body, 4<<10))
	decoder.DisallowUnknownFields()
	decodeErr := decoder.Decode(&report)
	m.mu.Lock()
	defer m.mu.Unlock()
	window, found, err := m.load(key)
	switch {
	case err != nil:
		http.Error(w, "report denied", http.StatusForbidden)
		return
	case !found:
		http.Error(w, "report denied", http.StatusNotFound)
		return
	}
	status, err := m.status(request.Context(), key, window)
	if err != nil {
		http.Error(w, "custody unreadable", http.StatusServiceUnavailable)
		return
	}
	if status.retire {
		window.ConfigMedium = nil
	}
	outcome, code := "accepted", http.StatusOK
	switch {
	case !fromAny(request, window.ReportFrom):
		// Checked first, so a report from elsewhere is refused for where it
		// came from, whatever token it carries.
		outcome, code = "rejected-source", http.StatusForbidden
		window.Rejected++
	case decodeErr != nil || len(report.Token) != 64 || subtle.ConstantTimeCompare([]byte(digest(report.Token)), []byte(window.TokenSHA256)) != 1:
		outcome, code = "rejected-token", http.StatusForbidden
		window.Rejected++
	case !status.ReportOpen:
		outcome, code = "rejected-window", http.StatusForbidden
		window.Rejected++
	case window.Report != nil && window.Report.Image == report.Image && window.Report.Bootstrap == report.Bootstrap && slices.Equal(window.Report.Downloads, report.Downloads):
		// The installed OS retried after a lost answer: the same report
		// again changes nothing.
		outcome = "repeated"
	case window.Report != nil:
		// The token is single use: a second, different report means two
		// installs, and the operation can no longer succeed.
		outcome, code = "duplicate", http.StatusConflict
		window.Duplicates++
	default:
		window.Report = &ReceivedReport{Image: report.Image, Bootstrap: report.Bootstrap, Downloads: report.Downloads, ReceivedAt: m.now().UTC()}
	}
	if err := m.save(key, window); err != nil {
		http.Error(w, "state unwritable", http.StatusInternalServerError)
		return
	}
	_ = appendEvidence(filepath.Join(m.Root, "transfers", key), "reports.log", fmt.Sprintf("%s report %s\n", m.now().UTC().Format(time.RFC3339), outcome))
	reply(w, code, map[string]string{"report": outcome})
}

func (m *MediaServer) media(w http.ResponseWriter, request *http.Request, key, kind, secret, name string) {
	deny := func() { http.Error(w, "media denied", http.StatusForbidden) }
	m.mu.Lock()
	window, found, err := m.load(key)
	m.mu.Unlock()
	if err != nil || !found {
		deny()
		return
	}
	status, err := m.status(request.Context(), key, window)
	if err == nil && status.retire {
		err = m.drop(key)
	}
	if err != nil || !status.FetchOpen {
		deny()
		return
	}
	var body io.ReadSeeker
	var size int64
	switch {
	case kind == "boot" && name == "boot.iso" && subtle.ConstantTimeCompare([]byte(secret), []byte(window.BootDigest)) == 1:
		// Hash the pinned artifact, then serve the same open file.
		file, err := os.Open(filepath.Join(m.Artifacts, "boot.iso"))
		if err != nil {
			deny()
			return
		}
		defer file.Close()
		hash := sha256.New()
		if size, err = io.Copy(hash, file); err != nil || hex.EncodeToString(hash.Sum(nil)) != window.BootDigest {
			deny()
			return
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			deny()
			return
		}
		body = file
	case kind == "config" && name == "config.img" && subtle.ConstantTimeCompare([]byte(digest(secret)), []byte(window.CapabilitySHA256)) == 1 && len(window.ConfigMedium) > 0:
		body, size = bytes.NewReader(window.ConfigMedium), int64(len(window.ConfigMedium))
	default:
		deny()
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	if request.Method == http.MethodHead {
		return
	}
	id, err := transferID()
	dir := filepath.Join(m.Root, "transfers", key)
	if err != nil || appendEvidence(dir, transferLog, fmt.Sprintf("%s start %s %s\n", m.now().UTC().Format(time.RFC3339), id, kind)) != nil {
		deny()
		return
	}
	written, err := io.Copy(w, body)
	outcome := "complete"
	if err != nil || written != size {
		outcome = "incomplete"
	}
	// The end is recorded even when the client went away: the transfer is
	// over either way.
	_ = appendEvidence(dir, transferLog, fmt.Sprintf("%s end %s %s bytes=%d %s\n", m.now().UTC().Format(time.RFC3339), id, kind, written, outcome))
}

const transferLog = "media-transfer.log"

func transferID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Transfer is one media transfer the media service recorded.
type Transfer struct {
	ID, Kind string
	Ended    bool
	Bytes    int64
	Complete bool
}

// Transfers reads the media service's transfer records for a window.
func (m *MediaServer) Transfers(key string) ([]Transfer, error) {
	data, err := os.ReadFile(filepath.Join(m.Root, "transfers", key, transferLog))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Transfer
	index := map[string]int{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		switch {
		case len(fields) == 4 && fields[1] == "start":
			index[fields[2]] = len(out)
			out = append(out, Transfer{ID: fields[2], Kind: fields[3]})
		case len(fields) == 6 && fields[1] == "end":
			i, ok := index[fields[2]]
			if !ok {
				return nil, errors.New("a media transfer ended that never started")
			}
			n, _ := strconv.ParseInt(strings.TrimPrefix(fields[4], "bytes="), 10, 64)
			out[i].Ended, out[i].Bytes, out[i].Complete = true, n, fields[5] == "complete"
		case line == "":
		default:
			return nil, errors.New("an unreadable media transfer record")
		}
	}
	return out, nil
}

// openTransfers counts a window's transfers that started and never ended.
// An unreadable record is not proof that none is open.
func (m *MediaServer) openTransfers(key string) (int, error) {
	transfers, err := m.Transfers(key)
	if err != nil {
		return 0, err
	}
	open := 0
	for _, transfer := range transfers {
		if !transfer.Ended {
			open++
		}
	}
	return open, nil
}
