package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const APIVersion = "photo-bridge/v1alpha1"

var (
	jobNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	remotePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)
)

type Config struct {
	APIVersion string `yaml:"apiVersion" json:"apiVersion"`
	Jobs       []Job  `yaml:"jobs" json:"jobs"`
}

type Job struct {
	Name        string   `yaml:"name" json:"name"`
	Operation   string   `yaml:"operation" json:"operation"`
	Source      Endpoint `yaml:"source" json:"source"`
	Destination Endpoint `yaml:"destination" json:"destination"`
	Policy      Policy   `yaml:"policy" json:"policy"`
}

type Endpoint struct {
	Driver   string    `yaml:"driver" json:"driver"`
	Path     string    `yaml:"path" json:"path"`
	Remote   string    `yaml:"remote,omitempty" json:"remote,omitempty"`
	Selector *Selector `yaml:"selector,omitempty" json:"selector,omitempty"`
}

type Selector struct {
	Kind      string `yaml:"kind" json:"kind"`
	SettleFor string `yaml:"settleFor,omitempty" json:"settleFor,omitempty"`
}

type Policy struct {
	Manifest     string `yaml:"manifest" json:"manifest"`
	Verification string `yaml:"verification" json:"verification"`
	Transfers    int    `yaml:"transfers" json:"transfers"`
	Retries      int    `yaml:"retries" json:"retries"`
}

func Load(filePath string) (*Config, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open configuration: %w", err)
	}
	defer f.Close()

	return Decode(f)
}

func Decode(r io.Reader) (*Config, error) {
	decoder := yaml.NewDecoder(r)
	decoder.KnownFields(true)

	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode configuration: %w", err)
	}

	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("decode configuration: multiple YAML documents are not supported")
		}
		return nil, fmt.Errorf("decode configuration trailer: %w", err)
	}

	if err := cfg.NormalizeAndValidate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) NormalizeAndValidate() error {
	if c.APIVersion != APIVersion {
		return fmt.Errorf("apiVersion must be %q", APIVersion)
	}
	if len(c.Jobs) == 0 {
		return errors.New("jobs must contain at least one job")
	}

	seen := make(map[string]struct{}, len(c.Jobs))
	for i := range c.Jobs {
		job := &c.Jobs[i]
		if err := job.normalizeAndValidate(); err != nil {
			return fmt.Errorf("job %d: %w", i+1, err)
		}
		if _, ok := seen[job.Name]; ok {
			return fmt.Errorf("job %q is duplicated", job.Name)
		}
		seen[job.Name] = struct{}{}
	}
	return nil
}

func (c *Config) FindJob(name string) (Job, error) {
	for _, job := range c.Jobs {
		if job.Name == name {
			return job, nil
		}
	}
	return Job{}, fmt.Errorf("job %q was not found", name)
}

func (j *Job) normalizeAndValidate() error {
	if !jobNamePattern.MatchString(j.Name) {
		return errors.New("name must start with a lowercase letter and contain only lowercase letters, digits, and hyphens")
	}
	if j.Operation == "" {
		j.Operation = "copy"
	}
	if j.Operation != "copy" {
		return fmt.Errorf("operation %q is not supported; v0.1 supports only copy", j.Operation)
	}
	if err := j.Source.normalizeAndValidate("source"); err != nil {
		return err
	}
	if err := j.Destination.normalizeAndValidate("destination"); err != nil {
		return err
	}
	if err := j.Policy.normalizeAndValidate(); err != nil {
		return err
	}
	return nil
}

func (e *Endpoint) normalizeAndValidate(role string) error {
	e.Driver = strings.ToLower(strings.TrimSpace(e.Driver))
	switch e.Driver {
	case "filesystem":
		if e.Remote != "" {
			return fmt.Errorf("%s filesystem endpoint must not set remote", role)
		}
		if e.Path == "" {
			return fmt.Errorf("%s filesystem endpoint requires path", role)
		}
		if !filepath.IsAbs(e.Path) {
			return fmt.Errorf("%s filesystem path must be absolute", role)
		}
		e.Path = filepath.Clean(e.Path)
	case "rclone":
		if !remotePattern.MatchString(e.Remote) {
			return fmt.Errorf("%s rclone endpoint requires a simple remote name", role)
		}
		cleaned := path.Clean(strings.TrimPrefix(e.Path, "/"))
		if cleaned == "." {
			cleaned = ""
		}
		if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
			return fmt.Errorf("%s rclone path must not escape the remote root", role)
		}
		e.Path = cleaned
	default:
		return fmt.Errorf("%s driver must be filesystem or rclone", role)
	}
	if e.Selector != nil {
		if role != "source" {
			return errors.New("destination must not set selector")
		}
		if e.Selector.Kind != "google-takeout-latest" {
			return errors.New("source selector.kind must be google-takeout-latest")
		}
		if e.Selector.SettleFor == "" {
			e.Selector.SettleFor = "2h"
		}
		duration, err := time.ParseDuration(e.Selector.SettleFor)
		if err != nil {
			return fmt.Errorf("source selector.settleFor: %w", err)
		}
		if duration < 5*time.Minute || duration > 7*24*time.Hour {
			return errors.New("source selector.settleFor must be between 5m and 168h")
		}
	}
	return nil
}

func (e Endpoint) SettleDuration() time.Duration {
	if e.Selector == nil || e.Selector.SettleFor == "" {
		return 0
	}
	duration, _ := time.ParseDuration(e.Selector.SettleFor)
	return duration
}

func (p *Policy) normalizeAndValidate() error {
	if p.Manifest == "" {
		p.Manifest = "sha256"
	}
	switch p.Manifest {
	case "sha256", "auto", "metadata":
	default:
		return errors.New("policy.manifest must be sha256, auto, or metadata")
	}

	if p.Verification == "" {
		p.Verification = "auto"
	}
	switch p.Verification {
	case "auto", "checksum", "size":
	default:
		return errors.New("policy.verification must be auto, checksum, or size")
	}

	if p.Transfers == 0 {
		p.Transfers = 8
	}
	if p.Transfers < 1 || p.Transfers > 64 {
		return errors.New("policy.transfers must be between 1 and 64")
	}

	if p.Retries == 0 {
		p.Retries = 3
	}
	if p.Retries < 1 || p.Retries > 20 {
		return errors.New("policy.retries must be between 1 and 20")
	}
	return nil
}

func (e Endpoint) RcloneSpec() string {
	if e.Driver == "filesystem" {
		return e.Path
	}
	if e.Path == "" {
		return e.Remote + ":"
	}
	return e.Remote + ":" + e.Path
}

func (e Endpoint) UsesRcloneConfig() bool {
	return e.Driver == "rclone"
}
