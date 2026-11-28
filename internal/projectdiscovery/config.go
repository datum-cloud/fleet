// Package projectdiscovery selects the Milo project control planes the
// fleet-manager serves and owns the readiness of that selection. In milo
// mode it engages every project with an Active ServiceConsumer for the Bare
// Metal service, through the service catalog's multicluster-runtime consumer
// provider; in single mode it serves one named project.
package projectdiscovery

import (
	"errors"
	"fmt"
	"os"
	"strings"

	multiclusterproviders "go.miloapis.com/milo/pkg/multicluster-runtime"
	"sigs.k8s.io/yaml"
)

const (
	// ConfigAPIVersion and ConfigKind identify the manager's configuration
	// file. It is a file format read at startup, not a served API.
	ConfigAPIVersion = "config.fleet.datumapis.com/v1alpha1"
	ConfigKind       = "FleetManager"

	// MetalServiceName is the canonical Service.spec.serviceName of the Bare
	// Metal service registered by config/components/service-catalog. Fleet's
	// APIs are served only in projects that consume it.
	MetalServiceName = "metal.datumapis.com"
)

// Config is the fleet-manager configuration file. Its discovery block mirrors
// the shape Compute uses for consumer-scoped projection so environment owners
// wire both services the same way.
type Config struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	// AuthorityID names the configured Milo authority recorded in every
	// project identity.
	AuthorityID string          `json:"authorityID"`
	Discovery   DiscoveryConfig `json:"discovery"`
}

type DiscoveryConfig struct {
	// Mode is "milo" (every project consuming the metal service) or "single"
	// (one named project, for standalone use and tests).
	Mode multiclusterproviders.Provider `json:"mode"`

	// DiscoveryKubeconfigPath is a root Milo kubeconfig. It lists Services to
	// resolve canonical service names and reads Projects to resolve each
	// engaged project's immutable identity. Required.
	DiscoveryKubeconfigPath string `json:"discoveryKubeconfigPath"`

	// ProjectKubeconfigPath is the Milo kubeconfig template whose host is
	// re-addressed to each project's control plane. Defaults to
	// DiscoveryKubeconfigPath.
	ProjectKubeconfigPath string `json:"projectKubeconfigPath,omitempty"`

	// ConsumerScopedProjection selects the engaged projects in milo mode. It
	// is required there: the manager never watches every project.
	ConsumerScopedProjection *ConsumerScopedProjectionConfig `json:"consumerScopedProjection,omitempty"`

	// Project is the one served project in single mode.
	Project string `json:"project,omitempty"`
}

type ConsumerScopedProjectionConfig struct {
	// ProviderProject hosts the metal ServiceConsumers (the Service's producer
	// project).
	ProviderProject string `json:"providerProject"`
	// ServiceNames are canonical service names. Exactly MetalServiceName is
	// accepted.
	ServiceNames []string `json:"serviceNames"`
}

var ErrInvalidConfig = errors.New("invalid fleet-manager configuration")

// LoadConfig reads and strictly validates the configuration file. Unknown
// fields are rejected so a misspelled key cannot silently widen discovery.
func LoadConfig(path string) (Config, error) {
	if strings.TrimSpace(path) == "" {
		return Config{}, fmt.Errorf("%w: --config is required", ErrInvalidConfig)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("%w: read %s: %v", ErrInvalidConfig, path, err)
	}
	return ParseConfig(data)
}

func ParseConfig(data []byte) (Config, error) {
	var config Config
	if err := yaml.UnmarshalStrict(data, &config); err != nil {
		return Config{}, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	if config.Discovery.ProjectKubeconfigPath == "" {
		config.Discovery.ProjectKubeconfigPath = config.Discovery.DiscoveryKubeconfigPath
	}
	return config, config.Validate()
}

func (c Config) Validate() error {
	if c.APIVersion != ConfigAPIVersion || c.Kind != ConfigKind {
		return fmt.Errorf("%w: apiVersion/kind must be %s/%s", ErrInvalidConfig, ConfigAPIVersion, ConfigKind)
	}
	if strings.TrimSpace(c.AuthorityID) == "" {
		return fmt.Errorf("%w: authorityID is required", ErrInvalidConfig)
	}
	d := c.Discovery
	if strings.TrimSpace(d.DiscoveryKubeconfigPath) == "" {
		return fmt.Errorf("%w: discovery.discoveryKubeconfigPath is required; the host cluster is never used as Milo", ErrInvalidConfig)
	}
	switch d.Mode {
	case multiclusterproviders.ProviderSingle:
		if strings.TrimSpace(d.Project) == "" || d.ConsumerScopedProjection != nil {
			return fmt.Errorf("%w: single mode requires discovery.project and accepts no consumerScopedProjection", ErrInvalidConfig)
		}
		return nil
	case multiclusterproviders.ProviderMilo:
		if d.Project != "" {
			return fmt.Errorf("%w: milo mode discovers projects and accepts no discovery.project", ErrInvalidConfig)
		}
		if d.ConsumerScopedProjection == nil {
			return fmt.Errorf("%w: milo mode requires discovery.consumerScopedProjection; watching every project is not supported", ErrInvalidConfig)
		}
		return d.ConsumerScopedProjection.Validate()
	default:
		return fmt.Errorf("%w: discovery.mode must be %q or %q", ErrInvalidConfig, multiclusterproviders.ProviderMilo, multiclusterproviders.ProviderSingle)
	}
}

// Validate requires a provider project and exactly the metal service.
func (csp ConsumerScopedProjectionConfig) Validate() error {
	if strings.TrimSpace(csp.ProviderProject) == "" {
		return fmt.Errorf("%w: discovery.consumerScopedProjection.providerProject is required", ErrInvalidConfig)
	}
	if len(csp.ServiceNames) != 1 || csp.ServiceNames[0] != MetalServiceName {
		return fmt.Errorf("%w: discovery.consumerScopedProjection.serviceNames must be exactly [%s]", ErrInvalidConfig, MetalServiceName)
	}
	return nil
}
