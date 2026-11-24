package devicepower

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// Config is the dispatcher configuration file, usually a mounted ConfigMap.
type Config struct {
	// Project is the Milo project the kubeconfig addresses. It is the same
	// project BMC discovery serves in this site.
	Project string `json:"project"`
	// MiloKubeconfig is the path of a kubeconfig for that one project.
	MiloKubeconfig string `json:"miloKubeconfig"`
	// DeviceNamespaces are the project namespaces whose operations are served.
	DeviceNamespaces []string `json:"deviceNamespaces"`
	// WorkerNamespace is the site namespace of the stage records, worker Jobs
	// and the credential Secrets they mount. It defaults to $POD_NAMESPACE.
	WorkerNamespace string `json:"workerNamespace,omitempty"`
	// LocalKubeconfig reaches the site cluster. Empty uses the in-cluster
	// service account.
	LocalKubeconfig string `json:"localKubeconfig,omitempty"`
	// WorkerImage is the image worker Jobs run. The site admission policy
	// admits only this image.
	WorkerImage string `json:"workerImage"`
	// DiscoveryMaxAge bounds how old discovery's last inventory read of the
	// BMC may be when an operation is accepted.
	DiscoveryMaxAge metav1.Duration `json:"discoveryMaxAge,omitempty"`
	// ObserveTimeout is how long a worker watches for the requested power
	// state or a reset after its command.
	ObserveTimeout metav1.Duration `json:"observeTimeout,omitempty"`
	// RequestTimeout bounds each HTTP request a worker makes to a BMC.
	RequestTimeout metav1.Duration `json:"requestTimeout,omitempty"`
	// Retention is how long site objects of a finished operation are kept.
	Retention metav1.Duration `json:"retention,omitempty"`
	// HealthProbeAddress serves /healthz and /readyz; empty disables them.
	HealthProbeAddress string `json:"healthProbeAddress,omitempty"`
}

// LoadConfig reads, defaults and validates a configuration file.
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read dispatcher config: %w", err)
	}
	var c Config
	if err := yaml.UnmarshalStrict(data, &c); err != nil {
		return Config{}, fmt.Errorf("parse dispatcher config: %w", err)
	}
	c.Default()
	return c, c.Validate()
}

// Default fills unset values.
func (c *Config) Default() {
	set := func(d *metav1.Duration, v time.Duration) {
		if d.Duration == 0 {
			d.Duration = v
		}
	}
	set(&c.DiscoveryMaxAge, 2*time.Hour)
	set(&c.ObserveTimeout, 3*time.Minute)
	set(&c.RequestTimeout, 20*time.Second)
	set(&c.Retention, 24*time.Hour)
	if c.WorkerNamespace == "" {
		c.WorkerNamespace = os.Getenv("POD_NAMESPACE")
	}
}

// Validate rejects an incomplete or out-of-range configuration.
func (c Config) Validate() error {
	var errs []error
	if c.Project == "" || c.MiloKubeconfig == "" || c.WorkerNamespace == "" || c.WorkerImage == "" || len(c.DeviceNamespaces) == 0 {
		errs = append(errs, errors.New("project, miloKubeconfig, deviceNamespaces, workerImage and workerNamespace (or $POD_NAMESPACE) are required"))
	}
	for _, ns := range c.DeviceNamespaces {
		if strings.TrimSpace(ns) == "" {
			errs = append(errs, errors.New("deviceNamespaces must not contain an empty name"))
		}
	}
	if c.DiscoveryMaxAge.Duration < time.Minute || c.ObserveTimeout.Duration < 10*time.Second || c.ObserveTimeout.Duration > 10*time.Minute ||
		c.RequestTimeout.Duration <= 0 || c.RequestTimeout.Duration > time.Minute || c.Retention.Duration < time.Minute {
		errs = append(errs, errors.New("need discoveryMaxAge >= 1m, 10s <= observeTimeout <= 10m, 0 < requestTimeout <= 1m and retention >= 1m"))
	}
	return errors.Join(errs...)
}

// jobDeadline bounds a worker Job: its start window, the command, the
// observation and its record writes.
func (c Config) jobDeadline() time.Duration {
	return StaleAfter + 4*c.RequestTimeout.Duration + c.ObserveTimeout.Duration + time.Minute
}
