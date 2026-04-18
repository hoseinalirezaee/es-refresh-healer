package config

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ScanInterval             time.Duration
	DefaultRefreshInterval   time.Duration
	StaleMultiplier          float64
	GracePeriod              time.Duration
	Cooldown                 time.Duration
	MaxPatchesPerMinute      int
	DryRun                   bool
	WatchNamespaces          []string
	DenyNamespaces           []string
	LogLevel                 string
	MetricsAddr              string
	LeaderElect              bool
	LeaderElectionNamespace  string
	Kubeconfig               string
	ExternalSecretVersion    string
	AllowZeroRefreshInterval bool
	MaxAllowedLag            time.Duration
}

func Parse(args []string) (Config, error) {
	cfg := Config{
		ScanInterval:             durationEnv("SCAN_INTERVAL", 60*time.Second),
		DefaultRefreshInterval:   durationEnv("DEFAULT_REFRESH_INTERVAL", time.Hour),
		StaleMultiplier:          floatEnv("STALE_MULTIPLIER", 3),
		GracePeriod:              secondsEnv("GRACE_SECONDS", 30),
		Cooldown:                 secondsEnv("COOLDOWN_SECONDS", 600),
		MaxPatchesPerMinute:      intEnv("MAX_PATCHES_PER_MINUTE", 20),
		DryRun:                   boolEnv("DRY_RUN", false),
		WatchNamespaces:          listEnv("WATCH_NAMESPACES"),
		DenyNamespaces:           listEnv("DENY_NAMESPACES"),
		LogLevel:                 stringEnv("LOG_LEVEL", "info"),
		MetricsAddr:              stringEnv("METRICS_ADDR", ":8080"),
		LeaderElect:              boolEnv("LEADER_ELECT", true),
		LeaderElectionNamespace:  stringEnv("LEADER_ELECTION_NAMESPACE", stringEnv("POD_NAMESPACE", "default")),
		Kubeconfig:               stringEnv("KUBECONFIG", ""),
		ExternalSecretVersion:    stringEnv("EXTERNALSECRET_VERSION", "v1"),
		AllowZeroRefreshInterval: boolEnv("ALLOW_ZERO_REFRESH_INTERVAL", false),
		MaxAllowedLag:            secondsEnv("MAX_ALLOWED_LAG_SECONDS", 0),
	}

	fs := flag.NewFlagSet("es-refresh-healer", flag.ContinueOnError)
	fs.Var(durationFlag{target: &cfg.ScanInterval}, "scan-interval", "full ExternalSecret scan interval; accepts Go durations or a bare second value")
	fs.Var(durationFlag{target: &cfg.DefaultRefreshInterval}, "default-refresh-interval", "fallback refresh interval when spec.refreshInterval is missing; accepts Go durations or a bare second value")
	fs.Float64Var(&cfg.StaleMultiplier, "stale-multiplier", cfg.StaleMultiplier, "refresh interval multiplier used to decide staleness")
	fs.Var(durationFlag{target: &cfg.GracePeriod}, "grace-seconds", "extra grace duration; accepts Go durations or a bare second value")
	fs.Var(durationFlag{target: &cfg.Cooldown}, "cooldown-seconds", "minimum time between patches for the same ExternalSecret; accepts Go durations or a bare second value")
	fs.IntVar(&cfg.MaxPatchesPerMinute, "max-patches-per-minute", cfg.MaxPatchesPerMinute, "global patch token bucket limit; <=0 disables the limit")
	fs.BoolVar(&cfg.DryRun, "dry-run", cfg.DryRun, "log stale resources without patching")
	fs.Func("watch-namespaces", "comma-separated namespace allowlist; empty watches all namespaces", func(value string) error {
		cfg.WatchNamespaces = splitList(value)
		return nil
	})
	fs.Func("deny-namespaces", "comma-separated namespace denylist", func(value string) error {
		cfg.DenyNamespaces = splitList(value)
		return nil
	})
	fs.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "log level: debug, info, warn, error")
	fs.StringVar(&cfg.MetricsAddr, "metrics-addr", cfg.MetricsAddr, "metrics HTTP listen address")
	fs.BoolVar(&cfg.LeaderElect, "leader-elect", cfg.LeaderElect, "use Kubernetes Lease leader election")
	fs.StringVar(&cfg.LeaderElectionNamespace, "leader-election-namespace", cfg.LeaderElectionNamespace, "namespace for the leader election Lease")
	fs.StringVar(&cfg.Kubeconfig, "kubeconfig", cfg.Kubeconfig, "path to kubeconfig for out-of-cluster execution")
	fs.StringVar(&cfg.ExternalSecretVersion, "externalsecret-version", cfg.ExternalSecretVersion, "ExternalSecret API version")
	fs.BoolVar(&cfg.AllowZeroRefreshInterval, "allow-zero-refresh-interval", cfg.AllowZeroRefreshInterval, "evaluate resources with non-positive refresh intervals using the fallback interval")
	fs.Var(durationFlag{target: &cfg.MaxAllowedLag}, "max-allowed-lag-seconds", "hard maximum allowed lag; accepts Go durations or a bare second value; 0 disables")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	return cfg, cfg.Validate()
}

func (c Config) Validate() error {
	if c.ScanInterval <= 0 {
		return fmt.Errorf("scan interval must be positive")
	}
	if c.DefaultRefreshInterval <= 0 {
		return fmt.Errorf("default refresh interval must be positive")
	}
	if c.StaleMultiplier <= 0 {
		return fmt.Errorf("stale multiplier must be positive")
	}
	if c.GracePeriod < 0 {
		return fmt.Errorf("grace period cannot be negative")
	}
	if c.Cooldown < 0 {
		return fmt.Errorf("cooldown cannot be negative")
	}
	if c.ExternalSecretVersion == "" {
		return fmt.Errorf("external secret version cannot be empty")
	}
	return nil
}

func stringEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func intEnv(key string, fallback int) int {
	if value := os.Getenv(key); value != "" {
		parsed, err := strconv.Atoi(value)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func floatEnv(key string, fallback float64) float64 {
	if value := os.Getenv(key); value != "" {
		parsed, err := strconv.ParseFloat(value, 64)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func boolEnv(key string, fallback bool) bool {
	if value := os.Getenv(key); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func durationEnv(key string, fallback time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		parsed, err := parseDuration(value)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func secondsEnv(key string, fallbackSeconds int) time.Duration {
	if value := os.Getenv(key); value != "" {
		parsed, err := parseDuration(value)
		if err == nil {
			return parsed
		}
	}
	return time.Duration(fallbackSeconds) * time.Second
}

func listEnv(key string) []string {
	return splitList(os.Getenv(key))
}

func splitList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}

	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func parseDuration(value string) (time.Duration, error) {
	if value == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		return time.Duration(seconds) * time.Second, nil
	}
	return time.ParseDuration(value)
}

type durationFlag struct {
	target *time.Duration
}

func (d durationFlag) Set(value string) error {
	parsed, err := parseDuration(value)
	if err != nil {
		return err
	}
	*d.target = parsed
	return nil
}

func (d durationFlag) String() string {
	if d.target == nil {
		return ""
	}
	return d.target.String()
}
