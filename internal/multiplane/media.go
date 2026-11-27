package multiplane

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"go.datum.net/fleet/internal/allocation"
	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleet "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/redfish"
	"go.datum.net/fleet/internal/site/redfishwork"
)

// The virtual media scenarios (run.sh --redfish --virtual-media). Each
// phase checks one outcome through the provider project API and never
// writes custody.

// MediaNotOffered creates, while the cell declares only network boot, a
// virtual media installation for consumer A's allocation, as a Metal
// provider that skipped selection would. Admission refuses it before
// anything is delivered, with custody's fence unchanged.
func (r *Runner) MediaNotOffered(ctx context.Context) error {
	for _, offer := range r.Config.DeliveryMethods {
		if offer.Method == ops.DeliveryVirtualMedia {
			return fmt.Errorf("%w: the cell already offers virtual media", errTerminal)
		}
	}
	delivery := custody.OfferedMethod{Method: ops.DeliveryVirtualMedia, Addresses: ops.AddressesDataNetwork, MediaService: r.Config.MediaService}.Input()
	return r.refusedInstallation(ctx, "00000000-0000-4000-8000-000000000227", delivery, nil, custody.ReasonDeliveryNotOffered)
}

// MediaUnpinned creates, once the cell offers virtual media, a virtual
// media installation whose installer would download an image by tag, as an
// image like Talos's would without a digest. Admission refuses it before
// anything is delivered.
func (r *Runner) MediaUnpinned(ctx context.Context) error {
	index := -1
	for i, offer := range r.Config.DeliveryMethods {
		if offer.Method == ops.DeliveryVirtualMedia {
			index = i
		}
	}
	if index < 0 {
		return fmt.Errorf("%w: the cell does not offer virtual media", errTerminal)
	}
	downloads := []ops.DownloadInput{{Name: "installer", Reference: "registry.example/installer:v1.0.0"}}
	return r.refusedInstallation(ctx, "00000000-0000-4000-8000-000000000228", r.Config.DeliveryMethods[index].Input(), downloads, custody.ReasonDownloadUnpinned)
}

// refusedInstallation creates an installation for consumer A's allocation
// with a synthetic request UID, outside the Metal provider's selection, and
// requires admission to refuse it for the reason with custody unchanged.
func (r *Runner) refusedInstallation(ctx context.Context, request string, delivery ops.DeliveryInput, downloads []ops.DownloadInput, reason string) error {
	q, err := r.request(ctx, 0)
	if err != nil {
		return err
	}
	current, err := r.Allocator.Get(ctx, q.alloc.Machine)
	if err != nil {
		return err
	}
	if current == nil || current.Status.Phase != fleet.AllocationActive {
		return errNoActiveAllocation
	}
	before := &ops.DeviceCustody{}
	if err := r.Provider.API.Get(ctx, client.ObjectKey{Namespace: DeviceNamespace, Name: Device}, before); err != nil {
		return err
	}
	device := &inventory.Device{}
	if err := r.Provider.API.Get(ctx, client.ObjectKey{Namespace: DeviceNamespace, Name: Device}, device); err != nil {
		return err
	}
	image, err := allocation.ParseImage(r.Config.Image)
	if err != nil {
		return err
	}
	image.Downloads = downloads
	// A synthetic request UID keeps its name apart from every Installation.
	operation := &ops.DeviceInstallation{}
	key := client.ObjectKey{Namespace: DeviceNamespace, Name: custody.InstallationName(current.Name, request)}
	if err := r.Provider.API.Get(ctx, key, operation); apierrors.IsNotFound(err) {
		operation = &ops.DeviceInstallation{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
			Spec: ops.DeviceInstallationSpec{
				OperationEnvelope: ops.OperationEnvelope{Creator: r.Allocator.ProviderCreator, DeviceRef: ops.DeviceReference{Name: device.Name, UID: string(device.UID)},
					Authority: ops.Authority{AllocationRef: &ops.LocalReference{Name: current.Name}}},
				RequestUID: request, Image: image, BootInterface: "primary", TargetDisks: []string{"disk-0"},
				Delivery: delivery,
			},
		}
		if err := r.Provider.API.Create(ctx, operation); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	switch phase := operation.Status.Phase; {
	case phase == ops.PhaseFailed && operation.Status.Reason == reason:
	case phase.Ended() || phase.Running():
		return fmt.Errorf("%w: the virtual media installation is %s: %s", errTerminal, phase, operation.Status.Reason)
	default:
		return fmt.Errorf("%w: the virtual media installation is %q", errPending, phase)
	}
	after := &ops.DeviceCustody{}
	if err := r.Provider.API.Get(ctx, client.ObjectKey{Namespace: DeviceNamespace, Name: Device}, after); err != nil {
		return err
	}
	if after.Spec.Fence != before.Spec.Fence || after.Spec.ActiveOperation != nil || operation.Status.Fence != 0 || operation.Status.AdmittedAt != nil {
		return fmt.Errorf("%w: the refused installation changed custody", errTerminal)
	}
	fmt.Printf("%s refused for %s before admission; fence %d unchanged, nothing delivered\n", operation.Name, operation.Status.Reason, after.Spec.Fence)
	return nil
}

// mediaInstallation creates one of consumer A's Installations, as the
// customer, and its DeviceInstallation through the Metal provider, which
// selects the delivery method. It requires the selection to be virtual
// media.
func (r *Runner) mediaInstallation(ctx context.Context, name string) (*request, *fleet.Installation, *ops.DeviceInstallation, error) {
	customer, err := API(filepath.Join(r.Config.State, "consumer-a", "customer-project.json"))
	if err != nil {
		return nil, nil, nil, err
	}
	install := &fleet.Installation{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: Namespace}, Spec: fleet.InstallationSpec{MachineRef: fleet.MachineReference{Name: "allocation"},
		ExpectedPredecessor: fleet.InstallationPredecessor{Type: "None"}, ImageRef: fleet.LocalReference{Name: r.Config.Image}}}
	if err := ensure(ctx, customer, install); err != nil {
		return nil, nil, nil, err
	}
	q, _, install, operation, err := r.installationNamed(ctx, 0, name)
	if err != nil {
		return nil, nil, nil, err
	}
	if operation.Spec.Delivery.Method != ops.DeliveryVirtualMedia || operation.Spec.BootNetwork != nil {
		return nil, nil, nil, fmt.Errorf("%w: the Metal provider selected %+v", errTerminal, operation.Spec.Delivery)
	}
	return q, install, operation, nil
}

// MediaInstallHeld installs consumer A's first virtual media Installation
// while the lab BMC corrupts the report token on the config medium it
// holds. While the report window is open, it also posts, as a stray client
// would, a report with no token and one with another token. No report is
// taken, so the installation ends OutcomeUnknown at its report deadline,
// never Succeeded or Failed, with a hold that records virtual media.
func (r *Runner) MediaInstallHeld(ctx context.Context) error {
	return r.mediaHeld(ctx, "debian-media-1", true)
}

// MediaInstallDownload installs consumer A's second virtual media
// Installation, which settles the first hold, while the lab BMC adds a
// download the operation never pinned to the config medium. The report
// names it, so the installation ends OutcomeUnknown with a new hold.
func (r *Runner) MediaInstallDownload(ctx context.Context) error {
	return r.mediaHeld(ctx, "debian-media-2", false)
}

// MediaInstallLost installs consumer A's third virtual media Installation,
// which settles the second hold, while the lab BMC withholds an eject
// response. It ends OutcomeUnknown with a new hold.
func (r *Runner) MediaInstallLost(ctx context.Context) error {
	return r.mediaHeld(ctx, "debian-media-3", false)
}

func (r *Runner) mediaHeld(ctx context.Context, name string, probe bool) error {
	_, _, operation, err := r.mediaInstallation(ctx, name)
	if err != nil {
		return err
	}
	if probe && operation.Status.Phase.Running() {
		if err := r.strayReports(ctx, operation); err != nil {
			return fmt.Errorf("%w: %v", errPending, err)
		}
	}
	switch phase := operation.Status.Phase; {
	case phase == ops.PhaseOutcomeUnknown:
	case phase.Ended():
		return fmt.Errorf("%w: the installation ended %s: %s", errTerminal, phase, operation.Status.Reason)
	default:
		return fmt.Errorf("%w: the installation is %q", errPending, phase)
	}
	state := &ops.DeviceCustody{}
	if err := r.Provider.API.Get(ctx, client.ObjectKey{Namespace: DeviceNamespace, Name: Device}, state); err != nil {
		return err
	}
	method, held := custody.HeldMethod(state, "DeviceInstallation")
	if !held || state.Spec.RecoveryHold.UID != string(operation.UID) || method != ops.DeliveryVirtualMedia {
		return fmt.Errorf("%w: the hold does not record the virtual media installation: %+v", errTerminal, state.Spec.RecoveryHold)
	}
	if probe {
		if _, err := os.Stat(filepath.Join(r.Config.State, "media-stray-reports.json")); err != nil {
			return fmt.Errorf("%w: the stray reports were never posted while the report window was open", errTerminal)
		}
	}
	fmt.Printf("%s ended OutcomeUnknown (%s); the recovery hold at fence %d records %s\n", operation.Name, operation.Status.Reason, state.Spec.RecoveryHold.Fence, method)
	return nil
}

// strayReports posts, once, a report with no token and one with another
// token to the operation's report URL while its report window is open, and
// records the answers. Each must be refused.
func (r *Runner) strayReports(ctx context.Context, operation *ops.DeviceInstallation) error {
	record := filepath.Join(r.Config.State, "media-stray-reports.json")
	if _, err := os.Stat(record); err == nil {
		return nil
	}
	ca, err := os.ReadFile(filepath.Join(r.Config.State, "redfish", "media-tls.crt"))
	if err != nil {
		return err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return errors.New("the media service certificate is unreadable")
	}
	// The workstation reaches the central service on loopback. Reading a
	// window's status is the worker API, so it presents the cell's worker
	// certificate; the reports themselves carry none, as an installed OS's.
	identity, err := tls.LoadX509KeyPair(filepath.Join(r.Config.State, "redfish", "media-worker.crt"), filepath.Join(r.Config.State, "redfish", "media-worker.key"))
	if err != nil {
		return err
	}
	origin := "https://127.0.0.1:28443"
	worker := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{identity}}}}
	client := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	key := redfishwork.Key(string(operation.UID))
	status, err := worker.Get(origin + "/v1/windows/" + key)
	if err != nil {
		return err
	}
	var window redfishwork.WindowStatus
	err = json.NewDecoder(status.Body).Decode(&window)
	status.Body.Close()
	if err != nil || status.StatusCode != http.StatusOK || !window.ReportOpen || window.Report != nil {
		return errors.New("the report window is not open yet")
	}
	answers := map[string]int{}
	// The installed servers' reports reach the service on IPv4 loopback; a
	// report over IPv6 loopback comes from another address.
	posts := map[string]struct{ origin, body string }{
		"missing-token": {origin, `{"image":"sha256:` + strings.Repeat("0", 64) + `"}`},
		"other-token":   {origin, `{"token":"` + strings.Repeat("e", 64) + `"}`},
		"other-address": {"https://[::1]:28443", `{"token":"` + strings.Repeat("e", 64) + `"}`},
	}
	for label, post := range posts {
		response, err := client.Post(redfishwork.ReportURL(post.origin, key), "application/json", strings.NewReader(post.body))
		if err != nil {
			return err
		}
		response.Body.Close()
		answers[label] = response.StatusCode
		if response.StatusCode != http.StatusForbidden {
			return fmt.Errorf("%w: a report with %s was answered HTTP %d", errTerminal, label, response.StatusCode)
		}
	}
	data, _ := json.Marshal(map[string]any{"operation": operation.Name, "answers": answers})
	return os.WriteFile(record, data, 0o600)
}

// MediaInstall installs consumer A's fourth virtual media Installation. It
// settles the third hold, so the Metal provider selects the held method, and it
// must succeed with the hold cleared. The Installation and Machine then
// report Ready.
func (r *Runner) MediaInstall(ctx context.Context) error {
	q, install, operation, err := r.mediaInstallation(ctx, "debian-media-4")
	if err != nil {
		return err
	}
	switch phase := operation.Status.Phase; {
	case phase.Ended() && phase != ops.PhaseSucceeded:
		return fmt.Errorf("%w: the installation ended %s: %s", errTerminal, phase, operation.Status.Reason)
	case phase != ops.PhaseSucceeded:
		return fmt.Errorf("%w: the installation is %s", errPending, phase)
	}
	state := &ops.DeviceCustody{}
	if err := r.Provider.API.Get(ctx, client.ObjectKey{Namespace: DeviceNamespace, Name: Device}, state); err != nil {
		return err
	}
	if state.Spec.RecoveryHold != nil {
		return fmt.Errorf("%w: the hold survived the settling installation: %+v", errTerminal, state.Spec.RecoveryHold)
	}
	install.Status.Phase = "Ready"
	install.Status.Conditions = []metav1.Condition{condition("Ready", metav1.ConditionTrue, install.Generation, "LocalVirtualMediaVerified")}
	if err := q.project.API.Status().Update(ctx, install); err != nil {
		return err
	}
	m := q.machine
	m.Status.CurrentInstallationRef = &fleet.LocalReference{Name: install.Name}
	m.Status.Phase = "Ready"
	meta.SetStatusCondition(&m.Status.Conditions, condition("Ready", metav1.ConditionTrue, m.Generation, "LocalVirtualMediaVerified"))
	if err := q.project.API.Status().Update(ctx, m); err != nil {
		return err
	}
	fmt.Printf("media-install: %s succeeded at fence %d over virtual media and settled the hold\n", operation.Name, operation.Status.Fence)
	return nil
}

// custodyReader reads custody's active operation through the provider
// project API.
type custodyReader struct{ api client.Client }

func (c custodyReader) Active(ctx context.Context, device string) (string, int64, bool, error) {
	namespace, name, ok := strings.Cut(device, "/")
	if !ok {
		return "", 0, false, nil
	}
	state := &ops.DeviceCustody{}
	if err := c.api.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, state); apierrors.IsNotFound(err) {
		return "", 0, false, nil
	} else if err != nil {
		return "", 0, false, err
	}
	active := state.Spec.ActiveOperation
	if active == nil {
		return "", 0, false, nil
	}
	return active.UID, active.Fence, true, nil
}

// WorkerIdentity is the client certificate common name of a cell's site
// workers at the central media service.
func WorkerIdentity(cell string) string { return "fleet-site-worker:" + cell }

// MediaService runs the central media service for the acceptance: on the
// workstation, beside the coordinator, with its own state and the
// coordinator's read of custody. Site workers authenticate with client
// certificates from clientCA; a worker of the configured cell may act only
// on the Devices that cell is enrolled to serve. It serves over TLS on
// listen until ctx ends, and sweeps stale config media every half minute.
func MediaService(ctx context.Context, c Config, listen, certFile, keyFile, clientCA, root, artifacts string, clients []netip.Prefix) error {
	api, err := API(filepath.Join(c.State, "provider", "coordinator-project.json"))
	if err != nil {
		return err
	}
	identity, err := redfish.VerifyArtifacts(artifacts)
	if err != nil || identity != c.Image {
		return fmt.Errorf("the media service's artifacts are not the acceptance image")
	}
	caBytes, err := os.ReadFile(clientCA)
	if err != nil {
		return err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caBytes) {
		return errors.New("the site workers' client CA is invalid")
	}
	workers := func(identity, device string) bool {
		_, enrolled := c.Enrollment[device]
		return identity == WorkerIdentity(c.Cell) && enrolled
	}
	media := &redfishwork.MediaServer{Root: root, Artifacts: artifacts, Custody: custodyReader{api: api}, Clients: clients, Workers: workers}
	go media.RunSweeps(ctx, 30*time.Second, func(err error) { fmt.Fprintf(os.Stderr, "media-service: sweep incomplete: %v\n", err) })
	// BMCs and installed servers present no certificate; the worker API
	// requires one the client CA verified.
	// listen is a comma-separated list of addresses, such as loopback over
	// IPv4 and IPv6.
	addresses := strings.Split(listen, ",")
	errs := make(chan error, len(addresses))
	var servers []*http.Server
	for _, address := range addresses {
		server := &http.Server{Addr: strings.TrimSpace(address), Handler: media.Handler(), ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 30 * time.Minute, IdleTimeout: 5 * time.Second,
			TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, ClientAuth: tls.VerifyClientCertIfGiven, ClientCAs: pool}}
		servers = append(servers, server)
		go func() { errs <- server.ListenAndServeTLS(certFile, keyFile) }()
	}
	err = nil
	select {
	case <-ctx.Done():
	case err = <-errs:
	}
	for _, server := range servers {
		_ = server.Close()
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
