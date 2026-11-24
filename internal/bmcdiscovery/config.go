// Package bmcdiscovery reads what each Device's BMC reports, over Redfish GETs
// only, and writes it into Device status. It never writes spec, never changes
// power or boot, and sends a credential only to a BMC whose TLS public key
// matches the operator-approved pin. See docs/operations/bmc-discovery.md.
package bmcdiscovery

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// Config is the discovery configuration file, usually a mounted ConfigMap.
type Config struct {
	// Project is the Milo project the kubeconfig addresses. Credential Secrets
	// must name it, so Secrets for another project are never used.
	Project string `json:"project"`
	// MiloKubeconfig is the path of a kubeconfig for that one project.
	MiloKubeconfig string `json:"miloKubeconfig"`
	// DeviceNamespaces are the project namespaces whose Devices are read.
	DeviceNamespaces []string `json:"deviceNamespaces"`
	// CredentialNamespace holds the credential Secrets in the cluster where
	// discovery runs. It defaults to $POD_NAMESPACE.
	CredentialNamespace string `json:"credentialNamespace,omitempty"`
	// LocalKubeconfig reaches that cluster. Empty uses the in-cluster
	// service account.
	LocalKubeconfig string `json:"localKubeconfig,omitempty"`
	// Interval is the re-discovery period after a completed attempt.
	Interval metav1.Duration `json:"interval,omitempty"`
	// RetryBase is the first backoff after an uncertain attempt; it doubles
	// per consecutive failure up to Interval.
	RetryBase metav1.Duration `json:"retryBase,omitempty"`
	// MinContactInterval is the least time between two attempts against one
	// BMC endpoint, whatever triggers them.
	MinContactInterval metav1.Duration `json:"minContactInterval,omitempty"`
	// RequestTimeout bounds each HTTP request to a BMC.
	RequestTimeout metav1.Duration `json:"requestTimeout,omitempty"`
	// Concurrency is the number of BMCs contacted at once.
	Concurrency int `json:"concurrency,omitempty"`
	// HealthProbeAddress serves /healthz and /readyz; empty disables them.
	HealthProbeAddress string `json:"healthProbeAddress,omitempty"`
}

// LoadConfig reads, defaults and validates a configuration file.
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read discovery config: %w", err)
	}
	var c Config
	if err := yaml.UnmarshalStrict(data, &c); err != nil {
		return Config{}, fmt.Errorf("parse discovery config: %w", err)
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
	set(&c.Interval, time.Hour)
	set(&c.RetryBase, 30*time.Second)
	set(&c.MinContactInterval, time.Minute)
	set(&c.RequestTimeout, 20*time.Second)
	if c.Concurrency == 0 {
		c.Concurrency = 4
	}
	if c.CredentialNamespace == "" {
		c.CredentialNamespace = os.Getenv("POD_NAMESPACE")
	}
}

// Validate rejects an incomplete or out-of-range configuration.
func (c Config) Validate() error {
	var errs []error
	if c.Project == "" || c.MiloKubeconfig == "" || c.CredentialNamespace == "" || len(c.DeviceNamespaces) == 0 {
		errs = append(errs, errors.New("project, miloKubeconfig, deviceNamespaces and credentialNamespace (or $POD_NAMESPACE) are required"))
	}
	for _, ns := range c.DeviceNamespaces {
		if strings.TrimSpace(ns) == "" {
			errs = append(errs, errors.New("deviceNamespaces must not contain an empty name"))
		}
	}
	if c.Interval.Duration < time.Minute || c.RetryBase.Duration <= 0 || c.RetryBase.Duration > c.Interval.Duration ||
		c.MinContactInterval.Duration < 0 || c.MinContactInterval.Duration > c.Interval.Duration || c.RequestTimeout.Duration <= 0 {
		errs = append(errs, errors.New("need interval >= 1m, 0 < retryBase <= interval, 0 <= minContactInterval <= interval and requestTimeout > 0"))
	}
	if c.Concurrency < 1 || c.Concurrency > 64 {
		errs = append(errs, errors.New("concurrency must be between 1 and 64"))
	}
	return errors.Join(errs...)
}
