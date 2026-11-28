// fleet-multiplane is an opt-in local acceptance binary. Production managers do
// not import its synthetic admission, quota or discovery fixtures.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"go.datum.net/fleet/internal/coordination"
	"go.datum.net/fleet/internal/multiplane"
	"go.datum.net/fleet/internal/redfish"
	"go.datum.net/fleet/internal/site"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "multi-plane command failed:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: fleet-multiplane coordinator|site-worker|site-observer|image|boot-service|boot-withhold|media-stale|media-service|boot-denials|dhcp-feed|redfish-probe|<management phase> [flags]")
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ContinueOnError)
	once := fs.Bool("once", false, "one attempt, for fault injection")
	handoff := fs.String("handoff", "", "exact delivered handoff name (site-worker)")
	origin := fs.String("origin", "", "boot listener origin to probe, http://IPv4:port (boot-denials; required)")
	interval := fs.Duration("interval", 10*time.Second, "observation refresh interval (site-observer)")
	configPath := fs.String("config", "", "private acceptance config JSON")
	kubeconfig := fs.String("kubeconfig", "", "cell-local kubeconfig; default in-cluster ServiceAccount")
	cell := fs.String("cell", "", "exact Karmada member name")
	extraMAC := fs.String("extra-mac", "", "another enrolled NIC whose boot script must be denied (boot-denials, Redfish mode)")
	endpoint := fs.String("endpoint", "", "Redfish endpoint (redfish-probe)")
	credentials := fs.String("credentials", "", "directory holding username and password files (redfish-probe)")
	system := fs.String("system", "", "enrolled Redfish System Id (redfish-probe)")
	uuid := fs.String("uuid", "", "enrolled Redfish System UUID (redfish-probe)")
	pin := fs.String("pin", "", "the BMC's approved key digest; the probe connects only to that key (redfish-probe)")
	syncToken := fs.String("sync-token-file", "", "file holding the DHCP synchronizer bearer token (dhcp-feed)")
	installationDeadline := fs.Duration("installation-deadline", 0, "replace the installation deadline for operations this coordinator admits (coordinator)")
	tlsCert := fs.String("tls-cert", "", "TLS certificate the DHCP feed or media service serves (dhcp-feed, media-service)")
	tlsKey := fs.String("tls-key", "", "TLS key of the DHCP feed or media service (dhcp-feed, media-service)")
	deviceName := fs.String("device", "", "exact <namespace>/<name> Device (media-stale)")
	listen := fs.String("listen", "127.0.0.1:18443", "listen address (media-service)")
	mediaRoot := fs.String("state-dir", "", "the media service's own state directory (media-service)")
	mediaArtifacts := fs.String("artifacts", "", "directory of the image's pinned artifacts (media-service)")
	mediaClients := fs.String("clients", "", "comma-separated IPv4 prefixes the media service serves (media-service)")
	proxyListen := fs.String("proxy-listen", "", "comma-separated addresses where report relays connect with a PROXY header (media-service)")
	relays := fs.String("relays", "", "comma-separated prefixes of the report relays trusted to name a report's source (media-service)")
	upstream := fs.String("upstream", "", "the media service's PROXY listener, IP:port (report-relay)")
	clientCA := fs.String("client-ca", "", "CA that signs the site workers' client certificates (media-service)")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	switch os.Args[1] {
	case "coordinator":
		config, err := multiplane.ReadConfig(*configPath)
		if err != nil {
			return err
		}
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		fmt.Println("coordinator: custody coordinator running")
		return multiplane.Coordinate(ctx, config, *installationDeadline, func(err error) {
			fmt.Fprintf(os.Stderr, "%s coordinator: pass incomplete: %v\n", time.Now().UTC().Format(time.RFC3339), err)
		})
	case "seed", "allocate", "install", "admit-install", "admit-install-b", "release", "release-b", "reuse", "recovery-mismatch", "probe-unallocatable", "tenant-isolation", "media-not-offered", "media-unpinned", "media-install-held", "media-install-download", "media-install-lost", "media-install":
		return phase(os.Args[1], *configPath, *once)
	case "redfish-probe":
		return probe(*endpoint, *credentials, *system, *uuid, *pin)
	case "image":
		image, err := siteImage()
		if err != nil {
			return err
		}
		fmt.Println(image)
		return nil
	case "boot-service", "boot-denials", "dhcp-feed":
		return boot(os.Args[1], *origin, *extraMAC, *syncToken, *tlsCert, *tlsKey)
	case "boot-withhold":
		return armWithhold(fs.Arg(0))
	case "media-stale":
		return mediaStale(*deviceName)
	case "media-service":
		config, err := multiplane.ReadConfig(*configPath)
		if err != nil {
			return err
		}
		var clients, trusted []netip.Prefix
		for _, value := range strings.Split(*mediaClients, ",") {
			prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
			if err != nil {
				return fmt.Errorf("media-service: -clients must list IPv4 prefixes")
			}
			clients = append(clients, prefix.Masked())
		}
		for _, value := range strings.Split(*relays, ",") {
			if value = strings.TrimSpace(value); value == "" {
				continue
			}
			prefix, err := netip.ParsePrefix(value)
			if err != nil {
				return fmt.Errorf("media-service: -relays must list prefixes")
			}
			trusted = append(trusted, prefix.Masked())
		}
		if *proxyListen != "" && len(trusted) == 0 {
			return fmt.Errorf("media-service: -proxy-listen needs -relays")
		}
		if *mediaRoot == "" || *mediaArtifacts == "" || *tlsCert == "" || *tlsKey == "" || *clientCA == "" {
			return fmt.Errorf("media-service needs -state-dir, -artifacts, -tls-cert, -tls-key and -client-ca")
		}
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		fmt.Println("media-service: central media service running")
		return multiplane.MediaService(ctx, config, *listen, *proxyListen, *tlsCert, *tlsKey, *clientCA, *mediaRoot, *mediaArtifacts, clients, trusted)
	case "report-relay":
		// Lab only: forwards the guests' report connections, naming each
		// guest's address, to the media service's PROXY listener.
		if *upstream == "" {
			return fmt.Errorf("report-relay needs -listen and -upstream")
		}
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		return multiplane.ReportRelay(ctx, *listen, *upstream)
	case "site-worker", "site-observer":
	default:
		return fmt.Errorf("unknown command")
	}
	if *cell == "" || (os.Args[1] == "site-worker" && *handoff == "") {
		return fmt.Errorf("cell and (for site-worker) handoff required")
	}
	cfg, err := rest.InClusterConfig()
	if *kubeconfig != "" {
		cfg, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
	}
	if err != nil {
		return err
	}
	cfg.Timeout = 30 * time.Second
	api, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	store, err := openStore()
	if err != nil {
		return err
	}
	cellView := &coordination.Cell{API: api, Namespace: coordinationNamespace, Name: *cell}
	if os.Args[1] == "site-observer" {
		// The observer never runs work: it reports what the store holds.
		return cellView.Observe(ctx, *interval, store, site.NewResults(store), func(name string, err error) {
			fmt.Fprintf(os.Stderr, "observer: handoff %s not published: %s\n", name, classify(err))
		})
	}
	runner, err := siteRunner(ctx, cfg, *cell, store)
	if err != nil {
		return err
	}
	err = cellView.Work(ctx, *handoff, runner)
	fmt.Fprintf(os.Stderr, "site-worker: handoff %s: %s\n", *handoff, classify(err))
	switch {
	case err == nil, errors.Is(err, site.ErrAlreadyRunning):
		return nil
	case errors.Is(err, site.ErrStaleFence), errors.Is(err, coordination.ErrNotDelivered):
		os.Exit(exitRejected)
	}
	os.Exit(exitRetry)
	return nil
}

// phase runs one management phase until it converges, retrying pending
// effects.
func phase(name, configPath string, once bool) error {
	config, err := multiplane.ReadConfig(configPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if name == "seed" {
		return multiplane.Seed(ctx, config)
	}
	r, err := multiplane.New(ctx, config)
	if err != nil {
		return err
	}
	action := map[string]func(context.Context) error{
		"allocate": r.Allocate, "admit-install": r.AdmitInstall, "install": r.Install,
		"release": r.Release, "release-b": r.ReleaseSecond, "reuse": r.Reuse,
		"admit-install-b": r.AdmitInstallSecond, "recovery-mismatch": r.RecoveryMismatch, "probe-unallocatable": r.ProbeUnallocatable, "tenant-isolation": r.TenantIsolation,
		"media-not-offered": r.MediaNotOffered, "media-unpinned": r.MediaUnpinned, "media-install-held": r.MediaInstallHeld, "media-install-download": r.MediaInstallDownload, "media-install-lost": r.MediaInstallLost, "media-install": r.MediaInstall,
	}[name]
	last := ""
	for {
		err = action(ctx)
		// Report each new reason a phase is waiting, so a phase that times
		// out shows what it waited for.
		if err != nil && err.Error() != last {
			last = err.Error()
			fmt.Printf("%s: waiting: %v\n", name, err)
		}
		if strings.HasPrefix(name, "admit-install") && errors.Is(err, multiplane.ErrAdmittedUnknown) {
			fmt.Println("Installation admitted and delivered; its result remains Unknown")
			return nil
		}
		if err == nil && (name == "recovery-mismatch" || name == "probe-unallocatable" || name == "tenant-isolation" || strings.HasPrefix(name, "media-")) {
			return nil
		}
		if err == nil {
			fmt.Println(name + ": verified local acceptance phase (synthetic authority)")
			return nil
		}
		if once || multiplane.Terminal(err) || errors.Is(err, multiplane.ErrLocationNotServed) {
			return err
		}
		if ctx.Err() != nil {
			return fmt.Errorf("%s did not converge; last error: %w", name, err)
		}
		time.Sleep(3 * time.Second)
	}
}

const coordinationNamespace = "fleet-coordination"

// redfishMode selects the Redfish workers for site roles. The site's Flux
// layers set it; there is no automatic choice between them.
func redfishMode() bool { return os.Getenv("FLEET_SITE_PROVIDER") == "redfish" }

// probe performs read-only Redfish discovery of one enrolled System and
// prints only an outcome class. Exit codes: 0 discovered, 10 credential
// rejected, 11 capability unsupported, 12 identity mismatch, 13 TLS key
// other than the pin.
func probe(endpoint, credentials, system, uuid, pin string) error {
	user, err1 := os.ReadFile(credentials + "/username")
	password, err2 := os.ReadFile(credentials + "/password")
	if credentials == "" || err1 != nil || err2 != nil {
		return fmt.Errorf("redfish-probe: credentials unavailable")
	}
	c, err := redfish.NewPinnedClient(endpoint, strings.TrimSpace(string(user)), strings.TrimSpace(string(password)), pin, 20*time.Second)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s, err := c.Discover(ctx, redfish.Target{SystemID: system, SystemUUID: uuid})
	switch {
	case err == nil:
		fmt.Printf("redfish-probe: discovered %s at %s; ResetType %s; boot targets %s\n", s.ID, s.Path, strings.Join(s.ResetTypes, ","), strings.Join(s.BootTargets, ","))
		return nil
	case errors.Is(err, redfish.ErrUnauthorized):
		fmt.Println("redfish-probe: credential rejected; no effect issued")
		os.Exit(10)
	case errors.Is(err, redfish.ErrUnsupported):
		fmt.Println("redfish-probe: required capability not advertised; no effect issued")
		os.Exit(11)
	case errors.Is(err, redfish.ErrPinMismatch):
		fmt.Println("redfish-probe: the BMC's TLS key does not match the pin; no request sent")
		os.Exit(13)
	case errors.Is(err, redfish.ErrIdentity):
		fmt.Println("redfish-probe: enrolled identity not matched exactly; no effect issued")
		os.Exit(12)
	}
	return err
}

// Worker exit codes are the Job podFailurePolicy contract: a handoff that is
// not this cell's, or an assignment below the Device's stored fence, fails
// the Job without retry; anything else is retried by the Job controller
// against the same assignment. Exit 0 follows a stored result, which only
// the coordinator turns into custody.
const (
	exitRejected = 65
	exitRetry    = 75
)

// classify names an outcome without printing handoff or journal contents.
func classify(err error) string {
	switch {
	case err == nil:
		return "result stored in the site store"
	case errors.Is(err, site.ErrAlreadyRunning):
		return "another worker runs this assignment"
	case errors.Is(err, site.ErrStaleFence):
		return "fenced: a newer operation was admitted for the Device"
	case errors.Is(err, coordination.ErrNotDelivered):
		return "invalid: not an assignment for this cell"
	default:
		return "pending: " + err.Error()
	}
}
