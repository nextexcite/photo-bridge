package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// APIVersion is intentionally a hard cut. v1alpha1 configuration is rejected.
const APIVersion = "photo-bridge/v1alpha2"

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
	Integrity        IntegrityPolicy `yaml:"integrity" json:"integrity"`
	Transfer         TransferPolicy  `yaml:"transfer" json:"transfer"`
	Limits           LimitsPolicy    `yaml:"limits" json:"limits"`
	ProgressInterval string          `yaml:"progressInterval" json:"progressInterval"`
	Retention        RetentionPolicy `yaml:"retention" json:"retention"`
}

type IntegrityPolicy struct {
	Manifest     string `yaml:"manifest" json:"manifest"`
	Verification string `yaml:"verification" json:"verification"`
	AllowEmpty   bool   `yaml:"allowEmpty" json:"allowEmpty"`
}

type TransferPolicy struct {
	Transfers       int    `yaml:"transfers" json:"transfers"`
	Checkers        int    `yaml:"checkers" json:"checkers"`
	BufferSize      string `yaml:"bufferSize" json:"bufferSize"`
	MaxBufferMemory string `yaml:"maxBufferMemory" json:"maxBufferMemory"`
}

type LimitsPolicy struct {
	MaxDuration string `yaml:"maxDuration" json:"maxDuration"`
	MaxFiles    int64  `yaml:"maxFiles" json:"maxFiles"`
	MaxBytes    int64  `yaml:"maxBytes" json:"maxBytes"`
}

type RetentionPolicy struct {
	Mode    string `yaml:"mode" json:"mode"`
	MaxAge  string `yaml:"maxAge" json:"maxAge"`
	MinRuns int    `yaml:"minRuns" json:"minRuns"`
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
		return fmt.Errorf("apiVersion must be %q; v1alpha1 is not supported", APIVersion)
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
		return fmt.Errorf("operation %q is not supported; photo-bridge supports only copy", j.Operation)
	}
	if err := j.Source.normalizeAndValidate("source"); err != nil {
		return err
	}
	if err := j.Destination.normalizeAndValidate("destination"); err != nil {
		return err
	}
	return j.Policy.normalizeAndValidate()
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
	if e.Selector == nil {
		return nil
	}
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
	return nil
}

func (e Endpoint) SettleDuration() time.Duration {
	if e.Selector == nil {
		return 0
	}
	d, _ := time.ParseDuration(e.Selector.SettleFor)
	return d
}

func (p *Policy) normalizeAndValidate() error {
	if p.Integrity.Manifest == "" {
		p.Integrity.Manifest = "auto"
	}
	if p.Integrity.Manifest != "auto" && p.Integrity.Manifest != "sha256" && p.Integrity.Manifest != "metadata" {
		return errors.New("policy.integrity.manifest must be auto, sha256, or metadata")
	}
	if p.Integrity.Verification == "" {
		p.Integrity.Verification = "auto"
	}
	switch p.Integrity.Verification {
	case "auto", "checksum", "size", "download":
	default:
		return errors.New("policy.integrity.verification must be auto, checksum, size, or download")
	}
	if p.Transfer.Transfers == 0 {
		p.Transfer.Transfers = 8
	}
	if p.Transfer.Transfers < 1 || p.Transfer.Transfers > 64 {
		return errors.New("policy.transfer.transfers must be between 1 and 64")
	}
	if p.Transfer.Checkers == 0 {
		p.Transfer.Checkers = 8
	}
	if p.Transfer.Checkers < 1 || p.Transfer.Checkers > 64 {
		return errors.New("policy.transfer.checkers must be between 1 and 64")
	}
	if p.Transfer.BufferSize == "" {
		p.Transfer.BufferSize = "16MiB"
	}
	bufferSize, err := parseBytes(p.Transfer.BufferSize)
	if err != nil {
		return fmt.Errorf("policy.transfer.bufferSize: %w", err)
	}
	if bufferSize == 0 {
		return errors.New("policy.transfer.bufferSize must be greater than zero")
	}
	if p.Transfer.MaxBufferMemory == "" {
		p.Transfer.MaxBufferMemory = "256MiB"
	}
	maxBufferMemory, err := parseBytes(p.Transfer.MaxBufferMemory)
	if err != nil {
		return fmt.Errorf("policy.transfer.maxBufferMemory: %w", err)
	}
	if maxBufferMemory == 0 {
		return errors.New("policy.transfer.maxBufferMemory must be greater than zero")
	}
	if p.Limits.MaxDuration == "" {
		p.Limits.MaxDuration = "0s"
	}
	if duration, err := time.ParseDuration(p.Limits.MaxDuration); err != nil || duration < 0 {
		return errors.New("policy.limits.maxDuration must be a non-negative duration")
	}
	if p.Limits.MaxFiles < 0 || p.Limits.MaxBytes < 0 {
		return errors.New("policy.limits maxFiles and maxBytes must be non-negative")
	}
	if p.ProgressInterval == "" {
		p.ProgressInterval = "5s"
	}
	if interval, err := time.ParseDuration(p.ProgressInterval); err != nil || interval < time.Second || interval > time.Minute {
		return errors.New("policy.progressInterval must be between 1s and 1m")
	}
	if p.Retention.Mode == "" {
		p.Retention.Mode = "automatic"
	}
	if p.Retention.Mode != "automatic" && p.Retention.Mode != "forever" {
		return errors.New("policy.retention.mode must be automatic or forever")
	}
	if p.Retention.MaxAge == "" {
		p.Retention.MaxAge = "720h"
	}
	if duration, err := time.ParseDuration(p.Retention.MaxAge); err != nil || duration < 0 {
		return errors.New("policy.retention.maxAge must be a non-negative duration")
	}
	if p.Retention.MinRuns == 0 {
		p.Retention.MinRuns = 5
	}
	if p.Retention.MinRuns < 1 || p.Retention.MinRuns > 1000 {
		return errors.New("policy.retention.minRuns must be between 1 and 1000")
	}
	return nil
}

func parseBytes(value string) (int64, error) {
	value = strings.TrimSpace(value)
	units := []struct {
		suffix     string
		multiplier int64
	}{{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}, {"TiB", 1 << 40}, {"B", 1}}
	for _, unit := range units {
		if strings.HasSuffix(value, unit.suffix) {
			integer := strings.TrimSpace(strings.TrimSuffix(value, unit.suffix))
			if integer == "" || !regexp.MustCompile(`^[0-9]+$`).MatchString(integer) {
				return 0, errors.New("must be a non-negative whole-byte value such as 16MiB")
			}
			n, err := strconv.ParseInt(integer, 10, 64)
			if err != nil || n < 0 {
				return 0, errors.New("must be a non-negative whole-byte value such as 16MiB")
			}
			if n > (1<<63-1)/unit.multiplier {
				return 0, errors.New("is too large")
			}
			return n * unit.multiplier, nil
		}
	}
	return 0, errors.New("must use B, KiB, MiB, GiB, or TiB")
}

// BufferSizeBytes and MaxBufferMemoryBytes expose validated normalized policy
// values for plan/reporting calculations. The maximum is a rclone buffer cap,
// not a process RSS guarantee.
func (p Policy) BufferSizeBytes() int64 { value, _ := parseBytes(p.Transfer.BufferSize); return value }
func (p Policy) MaxBufferMemoryBytes() int64 {
	value, _ := parseBytes(p.Transfer.MaxBufferMemory)
	return value
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
func (e Endpoint) UsesRcloneConfig() bool { return e.Driver == "rclone" }
func (p Policy) MaxDuration() time.Duration {
	d, _ := time.ParseDuration(p.Limits.MaxDuration)
	return d
}
func (p Policy) ProgressEvery() time.Duration {
	d, _ := time.ParseDuration(p.ProgressInterval)
	return d
}
func (p Policy) RetentionAge() time.Duration {
	d, _ := time.ParseDuration(p.Retention.MaxAge)
	return d
}
