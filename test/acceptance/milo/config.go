package miloacceptance

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/sys/unix"
	"k8s.io/apimachinery/pkg/util/validation"
)

const ConfigSchemaVersion = 1

var (
	hexRevision = regexp.MustCompile(`^[a-f0-9]{40}$`)
	imageDigest = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	runID       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	projectUID  = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
)

// Config identifies exact environment lifetimes. It deliberately contains only
// paths to credentials; credential material must never be embedded in config or
// copied into evidence.
type Config struct {
	SchemaVersion int            `json:"schemaVersion"`
	RunID         string         `json:"runID"`
	GitRevision   string         `json:"gitRevision"`
	MiloRevision  string         `json:"miloRevision"`
	AuthorityID   string         `json:"authorityID"`
	Projects      Projects       `json:"projects"`
	Images        Images         `json:"images"`
	Kubeconfigs   KubeconfigRefs `json:"kubeconfigs"`
}

type Projects struct {
	Provider  Project `json:"provider"`
	ConsumerA Project `json:"consumerA"`
	ConsumerB Project `json:"consumerB"`
}

type Project struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}

type Images struct {
	Manager   string `json:"manager"`
	Simulator string `json:"simulator"`
}

type KubeconfigRefs struct {
	MiloAdmin      string `json:"miloAdmin"`
	Infrastructure string `json:"infrastructure"`
	Provider       string `json:"provider"`
	ConsumerA      string `json:"consumerA"`
	ConsumerB      string `json:"consumerB"`
}

func (c Config) Canonicalized() Config {
	c.Kubeconfigs.MiloAdmin = filepath.Clean(c.Kubeconfigs.MiloAdmin)
	c.Kubeconfigs.Infrastructure = filepath.Clean(c.Kubeconfigs.Infrastructure)
	c.Kubeconfigs.Provider = filepath.Clean(c.Kubeconfigs.Provider)
	c.Kubeconfigs.ConsumerA = filepath.Clean(c.Kubeconfigs.ConsumerA)
	c.Kubeconfigs.ConsumerB = filepath.Clean(c.Kubeconfigs.ConsumerB)
	return c
}

func (c Config) Validate() error {
	c = c.Canonicalized()
	var problems []error
	if c.SchemaVersion != ConfigSchemaVersion {
		problems = append(problems, fmt.Errorf("schemaVersion must be %d", ConfigSchemaVersion))
	}
	if !runID.MatchString(c.RunID) {
		problems = append(problems, errors.New("runID must be a bounded evidence-safe identifier"))
	}
	if !hexRevision.MatchString(c.GitRevision) {
		problems = append(problems, errors.New("gitRevision must be a lowercase 40-character commit"))
	}
	if !hexRevision.MatchString(c.MiloRevision) {
		problems = append(problems, errors.New("miloRevision must be a lowercase 40-character commit"))
	}
	if errs := validation.IsDNS1123Subdomain(c.AuthorityID); len(errs) != 0 {
		problems = append(problems, fmt.Errorf("authorityID is invalid: %s", strings.Join(errs, "; ")))
	}
	projects := []struct {
		role string
		p    Project
	}{{"provider", c.Projects.Provider}, {"consumerA", c.Projects.ConsumerA}, {"consumerB", c.Projects.ConsumerB}}
	seenNames, seenUIDs := map[string]string{}, map[string]string{}
	for _, item := range projects {
		if errs := validation.IsDNS1123Subdomain(item.p.Name); len(errs) != 0 {
			problems = append(problems, fmt.Errorf("%s project name is invalid: %s", item.role, strings.Join(errs, "; ")))
		}
		if !projectUID.MatchString(item.p.UID) {
			problems = append(problems, fmt.Errorf("%s project UID must be an exact lowercase UUID", item.role))
		}
		if other, ok := seenNames[item.p.Name]; ok && item.p.Name != "" {
			problems = append(problems, fmt.Errorf("%s and %s project names must differ", other, item.role))
		}
		if other, ok := seenUIDs[item.p.UID]; ok && item.p.UID != "" {
			problems = append(problems, fmt.Errorf("%s and %s project UIDs must differ", other, item.role))
		}
		seenNames[item.p.Name], seenUIDs[item.p.UID] = item.role, item.role
	}
	for name, value := range map[string]string{"manager": c.Images.Manager, "simulator": c.Images.Simulator} {
		if !imageDigest.MatchString(value) {
			problems = append(problems, fmt.Errorf("%s image must be an exact sha256 digest", name))
		}
	}
	seenKubeconfigs := map[string]string{}
	for name, value := range map[string]string{
		"miloAdmin": c.Kubeconfigs.MiloAdmin, "infrastructure": c.Kubeconfigs.Infrastructure,
		"provider": c.Kubeconfigs.Provider, "consumerA": c.Kubeconfigs.ConsumerA,
		"consumerB": c.Kubeconfigs.ConsumerB,
	} {
		if value == "" || !filepath.IsAbs(value) {
			problems = append(problems, fmt.Errorf("%s kubeconfig must be an explicit absolute path", name))
		}
		if other, ok := seenKubeconfigs[value]; ok && value != "" {
			problems = append(problems, fmt.Errorf("%s and %s must use distinct kubeconfig files", other, name))
		}
		seenKubeconfigs[value] = name
	}
	return errors.Join(problems...)
}

type CredentialFiles struct {
	MiloAdmin      *os.File
	Infrastructure *os.File
	Provider       *os.File
	ConsumerA      *os.File
	ConsumerB      *os.File
}

func (f *CredentialFiles) Close() error {
	if f == nil {
		return nil
	}
	var closeErrors []error
	for _, file := range []*os.File{f.MiloAdmin, f.Infrastructure, f.Provider, f.ConsumerA, f.ConsumerB} {
		if file != nil {
			closeErrors = append(closeErrors, file.Close())
		}
	}
	return errors.Join(closeErrors...)
}

// OpenCredentialFiles is the only future credential-consumption boundary. It
// returns already-open no-follow descriptors, verifies each descriptor again
// after open, and rejects aliases using descriptor identity. Consumers must use
// these descriptors and must not reopen the configured paths.
func OpenCredentialFiles(c Config) (_ *CredentialFiles, returnedErr error) {
	c = c.Canonicalized()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	paths := []struct {
		name   string
		path   string
		assign func(*os.File)
	}{
		{name: "miloAdmin", path: c.Kubeconfigs.MiloAdmin},
		{name: "infrastructure", path: c.Kubeconfigs.Infrastructure},
		{name: "provider", path: c.Kubeconfigs.Provider},
		{name: "consumerA", path: c.Kubeconfigs.ConsumerA},
		{name: "consumerB", path: c.Kubeconfigs.ConsumerB},
	}
	files := &CredentialFiles{}
	paths[0].assign = func(file *os.File) { files.MiloAdmin = file }
	paths[1].assign = func(file *os.File) { files.Infrastructure = file }
	paths[2].assign = func(file *os.File) { files.Provider = file }
	paths[3].assign = func(file *os.File) { files.ConsumerA = file }
	paths[4].assign = func(file *os.File) { files.ConsumerB = file }
	defer func() {
		if returnedErr != nil {
			_ = files.Close()
		}
	}()
	infos := make([]os.FileInfo, len(paths))
	for index, item := range paths {
		if err := rejectSymlinkComponents(item.path); err != nil {
			return nil, fmt.Errorf("%s credential path is unsafe: %w", item.name, err)
		}
		before, err := os.Lstat(item.path)
		if err != nil {
			return nil, fmt.Errorf("%s credential file is unavailable", item.name)
		}
		fd, err := unix.Open(item.path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return nil, fmt.Errorf("%s credential file cannot be opened safely", item.name)
		}
		file := os.NewFile(uintptr(fd), item.path)
		if file == nil {
			_ = unix.Close(fd)
			return nil, fmt.Errorf("%s credential descriptor is invalid", item.name)
		}
		item.assign(file)
		after, err := file.Stat()
		if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
			return nil, fmt.Errorf("%s credential changed during secure open", item.name)
		}
		if after.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("%s credential permissions must be owner-only", item.name)
		}
		infos[index] = after
	}
	for left := range paths {
		for right := 0; right < left; right++ {
			if os.SameFile(infos[left], infos[right]) {
				return nil, fmt.Errorf("%s and %s credential paths alias the same file", paths[right].name, paths[left].name)
			}
		}
	}
	return files, nil
}
