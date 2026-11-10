package deploy

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/codeplo/codeplo/internal/policy"
)

// ResourceConfig is resources.config_json and the OpenAPI ResourceConfig. release_command, rollout and
// published_ports are the three keys this phase added to the contract (api/openapi.yaml).
type ResourceConfig struct {
	Image struct {
		Mode       string  `json:"mode"`
		Ref        *string `json:"ref"`
		Dockerfile *string `json:"dockerfile"`
		Context    *string `json:"context"`
	} `json:"image"`
	Port        int `json:"port"`
	Healthcheck struct {
		Path         string `json:"path"`
		IntervalS    int    `json:"interval_s"`
		Retries      int    `json:"retries"`
		StartPeriodS int    `json:"start_period_s"`
		TimeoutS     int    `json:"timeout_s"`
	} `json:"healthcheck"`
	Limits struct {
		MemoryMB  int     `json:"memory_mb"`
		CPUs      float64 `json:"cpus"`
		PidsLimit int     `json:"pids_limit"`
	} `json:"limits"`
	Isolation struct {
		CapAdd  []string `json:"cap_add"`
		User    *string  `json:"user"`
		Trusted bool     `json:"trusted"`
	} `json:"isolation"`
	Restart      string `json:"restart"`
	RetainImages int    `json:"retain_images"`

	// ReleaseCommand runs once in a one-off container from the new image before the switch (migrations). Failure
	// aborts the deployment. Application rollback is not database rollback.
	ReleaseCommand []string `json:"release_command,omitempty"`
	// Rollout is "start_first" (default, blue/green) or "stop_first" (very small hosts: stops the old container
	// before starting the new one, trading the safe rollback window for memory).
	Rollout string `json:"rollout,omitempty"`
	// PublishedPorts is an explicit, owner-only, audit-flagged opt-in. Ports bind to 127.0.0.1 only.
	PublishedPorts []int `json:"published_ports,omitempty"`

	// Build is the Render/Koyeb/Vercel-style command plan. Empty fields mean auto-detect from the repo
	// (package.json, requirements.txt, go.mod, …) or use an existing Dockerfile.
	Build *BuildSettings `json:"build,omitempty"`
}

// BuildSettings are optional overrides for host builds without a hand-written Dockerfile.
type BuildSettings struct {
	InstallCommand *string `json:"install_command,omitempty"`
	BuildCommand   *string `json:"build_command,omitempty"`
	StartCommand   *string `json:"start_command,omitempty"`
	TestCommand    *string `json:"test_command,omitempty"`
	Runtime        *string `json:"runtime,omitempty"` // e.g. node:22-alpine, python:3.12-slim
}

// DefaultConfig is what a new resource gets for every key the request leaves out.
func DefaultConfig() ResourceConfig {
	var c ResourceConfig
	c.Image.Mode = "pull"
	c.Port = 3000
	c.Healthcheck.Path = "/"
	c.Healthcheck.IntervalS, c.Healthcheck.Retries, c.Healthcheck.StartPeriodS, c.Healthcheck.TimeoutS = 5, 12, 5, 3
	c.Limits.MemoryMB, c.Limits.CPUs, c.Limits.PidsLimit = 256, 0.5, 512
	c.Isolation.CapAdd = []string{}
	c.Restart, c.RetainImages = "unless-stopped", 5
	return c
}

// ParseConfig decodes config_json, filling unset keys from DefaultConfig so older rows keep working.
func ParseConfig(raw string) (ResourceConfig, error) {
	c := DefaultConfig()
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return c, err
	}
	if c.Isolation.CapAdd == nil {
		c.Isolation.CapAdd = []string{}
	}
	return c, nil
}

// imageRefRe is the shape of a pullable image reference (same expression as the UI).
var imageRefRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]*(:[A-Za-z0-9._-]+)?(@sha256:[a-f0-9]{64})?$`)

// ValidImageRef reports whether ref is a well-formed image reference.
func ValidImageRef(ref string) bool { return len(ref) <= 255 && imageRefRe.MatchString(ref) }

// Validate mirrors the UI's validateConfig field for field (same messages, same bounds), so the API never
// accepts what the form rejects. hostMemMB bounds memory_mb; owner gates trusted and published ports.
func (c ResourceConfig) Validate(hostMemMB int, owner bool) map[string]string {
	f := map[string]string{}
	if c.Image.Mode == "pull" {
		if c.Image.Ref == nil || !ValidImageRef(*c.Image.Ref) {
			f["config.image.ref"] = "Enter an image reference such as ghcr.io/org/app:tag"
		}
	} else if c.Image.Mode == "build" {
		if d := c.Image.Dockerfile; d != nil && (strings.HasPrefix(*d, "/") || strings.Contains(*d, "..")) {
			f["config.image.dockerfile"] = "Dockerfile path must stay inside the build context"
		}
		if d := c.Image.Context; d != nil && (strings.HasPrefix(*d, "/") || strings.Contains(*d, "..")) {
			f["config.image.context"] = "Build context must stay inside the repository"
		}
	} else {
		f["config.image.mode"] = "Mode must be pull or build"
	}
	if c.Port < 1 || c.Port > 65535 {
		f["config.port"] = "Port must be 1-65535"
	}
	if !strings.HasPrefix(c.Healthcheck.Path, "/") {
		f["config.healthcheck.path"] = `Health path must start with "/"`
	}
	if c.Healthcheck.Retries < 1 || c.Healthcheck.Retries > 60 {
		f["config.healthcheck.retries"] = "Retries must be 1-60"
	}
	if c.Healthcheck.IntervalS < 1 || c.Healthcheck.IntervalS > 300 {
		f["config.healthcheck.interval_s"] = "Interval must be 1-300 seconds"
	}
	if c.Healthcheck.TimeoutS < 1 || c.Healthcheck.TimeoutS > 60 {
		f["config.healthcheck.timeout_s"] = "Timeout must be 1-60 seconds"
	}
	if c.Healthcheck.StartPeriodS < 0 || c.Healthcheck.StartPeriodS > 600 {
		f["config.healthcheck.start_period_s"] = "Start period must be 0-600 seconds"
	}
	if hostMemMB <= 0 {
		hostMemMB = 1 << 20
	}
	if c.Limits.MemoryMB < 16 || c.Limits.MemoryMB > hostMemMB {
		f["config.limits.memory_mb"] = fmt.Sprintf("Memory must be 16-%d MB", hostMemMB)
	}
	if !(c.Limits.CPUs >= 0.1 && c.Limits.CPUs <= 4) {
		f["config.limits.cpus"] = "CPU must be 0.1-4"
	}
	if c.Limits.PidsLimit < 16 || c.Limits.PidsLimit > 4096 {
		f["config.limits.pids_limit"] = "PIDs limit must be 16-4096"
	}
	var bad []string
	allow := map[string]bool{}
	for _, a := range policy.DefaultCapAllowlist {
		allow[a] = true
	}
	for _, cp := range c.Isolation.CapAdd {
		if !allow[strings.ToUpper(strings.TrimPrefix(strings.ToUpper(cp), "CAP_"))] {
			bad = append(bad, cp)
		}
	}
	if len(bad) > 0 {
		f["config.isolation.cap_add"] = fmt.Sprintf("Not allowed: %s. Allowed additions: %s", strings.Join(bad, ", "), strings.Join(policy.DefaultCapAllowlist, ", "))
	}
	if c.Isolation.Trusted && !owner {
		f["config.isolation.trusted"] = "Only an owner can mark a resource trusted"
	}
	if c.Isolation.User != nil && !userRe.MatchString(*c.Isolation.User) {
		f["config.isolation.user"] = "Use a user name or numeric uid[:gid]"
	}
	if c.RetainImages < 1 || c.RetainImages > 20 {
		f["config.retain_images"] = "Retain 1-20 images"
	}
	switch c.Restart {
	case "unless-stopped", "always", "on-failure", "no":
	default:
		f["config.restart"] = "Restart must be unless-stopped, always, on-failure or no"
	}
	if c.Rollout != "" && c.Rollout != "start_first" && c.Rollout != "stop_first" {
		f["config.rollout"] = "Rollout must be start_first or stop_first"
	}
	if len(c.ReleaseCommand) > 32 {
		f["config.release_command"] = "Release command has too many arguments"
	}
	for _, a := range c.ReleaseCommand {
		if strings.ContainsRune(a, 0) || len(a) > 1024 {
			f["config.release_command"] = "Release command arguments must be plain text under 1024 characters"
		}
	}
	if len(c.PublishedPorts) > 0 && !owner {
		f["config.published_ports"] = "Only an owner can publish ports (audit-flagged)"
	}
	for _, p := range c.PublishedPorts {
		if p < 1 || p > 65535 {
			f["config.published_ports"] = "Published ports must be 1-65535"
		}
	}
	return f
}

var userRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,63}(:[A-Za-z0-9_][A-Za-z0-9_.-]{0,63})?$`)
