package staleness

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hoseinalirezaee/es-refresh-healer/internal/patcher"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type Config struct {
	DefaultRefreshInterval   time.Duration
	StaleMultiplier          float64
	GracePeriod              time.Duration
	MaxAllowedLag            time.Duration
	AllowZeroRefreshInterval bool
}

type ExternalSecretInfo struct {
	Namespace       string
	Name            string
	Annotations     map[string]string
	RefreshInterval *time.Duration
	RefreshTime     *time.Time
	CreationTime    time.Time
}

type Evaluation struct {
	Stale              bool
	Skipped            bool
	Reason             string
	Lag                time.Duration
	EffectiveInterval  time.Duration
	Threshold          time.Duration
	RecheckAfter       time.Duration
	MissingRefreshTime bool
}

type Evaluator struct {
	cfg Config
}

func NewEvaluator(cfg Config) Evaluator {
	if cfg.DefaultRefreshInterval <= 0 {
		cfg.DefaultRefreshInterval = time.Hour
	}
	if cfg.StaleMultiplier <= 0 {
		cfg.StaleMultiplier = 3
	}
	return Evaluator{cfg: cfg}
}

func (e Evaluator) Evaluate(info ExternalSecretInfo, now time.Time) Evaluation {
	interval := e.cfg.DefaultRefreshInterval
	if info.RefreshInterval != nil {
		interval = *info.RefreshInterval
	}

	if interval <= 0 && !e.cfg.AllowZeroRefreshInterval {
		return Evaluation{
			Skipped:           true,
			Reason:            "non_positive_refresh_interval",
			EffectiveInterval: interval,
		}
	}
	if interval <= 0 {
		interval = e.cfg.DefaultRefreshInterval
	}

	threshold := time.Duration(float64(interval)*e.cfg.StaleMultiplier) + e.cfg.GracePeriod
	if e.cfg.MaxAllowedLag > 0 && threshold > e.cfg.MaxAllowedLag {
		threshold = e.cfg.MaxAllowedLag
	}

	if info.RefreshTime == nil {
		lag := now.Sub(info.CreationTime)
		stale := lag >= threshold
		reason := "bootstrap_grace"
		if stale {
			reason = "missing_refresh_time"
		}
		return Evaluation{
			Stale:              stale,
			Reason:             reason,
			Lag:                lag,
			EffectiveInterval:  interval,
			Threshold:          threshold,
			RecheckAfter:       recheckAfter(threshold, lag),
			MissingRefreshTime: true,
		}
	}

	lag := now.Sub(*info.RefreshTime)
	stale := lag >= threshold
	reason := "fresh"
	if stale {
		reason = "stale_refresh_time"
	}

	return Evaluation{
		Stale:             stale,
		Reason:            reason,
		Lag:               lag,
		EffectiveInterval: interval,
		Threshold:         threshold,
		RecheckAfter:      recheckAfter(threshold, lag),
	}
}

func recheckAfter(threshold, lag time.Duration) time.Duration {
	remaining := threshold - lag
	if remaining < 0 {
		return 0
	}
	return remaining
}

func FromUnstructured(obj *unstructured.Unstructured, defaultInterval time.Duration) (ExternalSecretInfo, error) {
	info := ExternalSecretInfo{
		Namespace:       obj.GetNamespace(),
		Name:            obj.GetName(),
		Annotations:     obj.GetAnnotations(),
		CreationTime:    obj.GetCreationTimestamp().Time,
		RefreshInterval: &defaultInterval,
	}

	if raw, ok, err := unstructured.NestedString(obj.Object, "spec", "refreshInterval"); err != nil {
		return info, fmt.Errorf("read spec.refreshInterval: %w", err)
	} else if ok {
		interval, err := time.ParseDuration(raw)
		if err != nil {
			return info, fmt.Errorf("parse spec.refreshInterval %q: %w", raw, err)
		}
		info.RefreshInterval = &interval
	}

	if raw, ok, err := unstructured.NestedString(obj.Object, "status", "refreshTime"); err != nil {
		return info, fmt.Errorf("read status.refreshTime: %w", err)
	} else if ok && strings.TrimSpace(raw) != "" {
		refreshTime, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return info, fmt.Errorf("parse status.refreshTime %q: %w", raw, err)
		}
		info.RefreshTime = &refreshTime
	}

	return info, nil
}

func AnnotationCooldownExpired(annotations map[string]string, now time.Time, cooldown time.Duration) (bool, time.Duration) {
	if cooldown <= 0 {
		return true, 0
	}

	value := annotations[patcher.LastKickAnnotation]
	if value == "" {
		return true, 0
	}

	unixTS, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return true, 0
	}

	elapsed := now.Sub(time.Unix(unixTS, 0))
	if elapsed >= cooldown {
		return true, elapsed
	}
	return false, elapsed
}
