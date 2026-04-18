package staleness

import (
	"testing"
	"time"

	"github.com/hoseinalirezaee/es-refresh-healer/internal/patcher"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestEvaluatorDetectsStaleRefreshTime(t *testing.T) {
	now := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	interval := time.Minute
	refreshTime := now.Add(-4 * time.Minute)

	evaluator := NewEvaluator(Config{
		DefaultRefreshInterval: time.Hour,
		StaleMultiplier:        3,
		GracePeriod:            30 * time.Second,
	})

	result := evaluator.Evaluate(ExternalSecretInfo{
		Name:            "secret",
		Namespace:       "default",
		RefreshInterval: &interval,
		RefreshTime:     &refreshTime,
		CreationTime:    now.Add(-10 * time.Minute),
	}, now)

	if !result.Stale {
		t.Fatalf("expected resource to be stale")
	}
	if result.Reason != "stale_refresh_time" {
		t.Fatalf("expected stale_refresh_time reason, got %q", result.Reason)
	}
}

func TestEvaluatorKeepsFreshResourceUntouched(t *testing.T) {
	now := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	interval := time.Minute
	refreshTime := now.Add(-2 * time.Minute)

	evaluator := NewEvaluator(Config{
		DefaultRefreshInterval: time.Hour,
		StaleMultiplier:        3,
		GracePeriod:            30 * time.Second,
	})

	result := evaluator.Evaluate(ExternalSecretInfo{
		RefreshInterval: &interval,
		RefreshTime:     &refreshTime,
		CreationTime:    now.Add(-10 * time.Minute),
	}, now)

	if result.Stale {
		t.Fatalf("expected resource to be fresh")
	}
	if result.Reason != "fresh" {
		t.Fatalf("expected fresh reason, got %q", result.Reason)
	}
	if result.RecheckAfter != 90*time.Second {
		t.Fatalf("expected recheck after 90s, got %s", result.RecheckAfter)
	}
}

func TestEvaluatorTreatsThresholdAsStale(t *testing.T) {
	now := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	interval := time.Minute
	refreshTime := now.Add(-3*time.Minute - 30*time.Second)

	evaluator := NewEvaluator(Config{
		DefaultRefreshInterval: time.Hour,
		StaleMultiplier:        3,
		GracePeriod:            30 * time.Second,
	})

	result := evaluator.Evaluate(ExternalSecretInfo{
		RefreshInterval: &interval,
		RefreshTime:     &refreshTime,
		CreationTime:    now.Add(-10 * time.Minute),
	}, now)

	if !result.Stale {
		t.Fatalf("expected resource to be stale at threshold")
	}
	if result.RecheckAfter != 0 {
		t.Fatalf("expected no delayed recheck for stale resource, got %s", result.RecheckAfter)
	}
}

func TestEvaluatorUsesBootstrapGraceForMissingRefreshTime(t *testing.T) {
	now := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)

	evaluator := NewEvaluator(Config{
		DefaultRefreshInterval: time.Minute,
		StaleMultiplier:        3,
		GracePeriod:            30 * time.Second,
	})

	young := evaluator.Evaluate(ExternalSecretInfo{CreationTime: now.Add(-2 * time.Minute)}, now)
	if young.Stale {
		t.Fatalf("expected young resource without refreshTime to remain in bootstrap grace")
	}
	if young.Reason != "bootstrap_grace" {
		t.Fatalf("expected bootstrap_grace, got %q", young.Reason)
	}

	old := evaluator.Evaluate(ExternalSecretInfo{CreationTime: now.Add(-5 * time.Minute)}, now)
	if !old.Stale {
		t.Fatalf("expected old resource without refreshTime to be stale")
	}
	if old.Reason != "missing_refresh_time" {
		t.Fatalf("expected missing_refresh_time, got %q", old.Reason)
	}
}

func TestEvaluatorSkipsNonPositiveRefreshInterval(t *testing.T) {
	now := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	interval := time.Duration(0)

	evaluator := NewEvaluator(Config{
		DefaultRefreshInterval: time.Minute,
		StaleMultiplier:        3,
	})

	result := evaluator.Evaluate(ExternalSecretInfo{
		RefreshInterval: &interval,
		CreationTime:    now.Add(-10 * time.Minute),
	}, now)

	if !result.Skipped {
		t.Fatalf("expected zero refresh interval to be skipped")
	}
}

func TestAnnotationCooldownExpired(t *testing.T) {
	now := time.Unix(2000, 0)
	annotations := map[string]string{
		patcher.LastKickAnnotation: "1300",
	}

	allowed, elapsed := AnnotationCooldownExpired(annotations, now, 10*time.Minute)
	if !allowed {
		t.Fatalf("expected cooldown to be expired")
	}
	if elapsed != 700*time.Second {
		t.Fatalf("expected 700s elapsed, got %s", elapsed)
	}

	allowed, _ = AnnotationCooldownExpired(annotations, now, 10*time.Hour)
	if allowed {
		t.Fatalf("expected cooldown to be active")
	}
}

func TestFromUnstructuredParsesRefreshFields(t *testing.T) {
	created := time.Date(2026, 4, 18, 11, 0, 0, 0, time.UTC)
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"name":              "demo",
			"namespace":         "apps",
			"creationTimestamp": created.Format(time.RFC3339),
		},
		"spec": map[string]any{
			"refreshInterval": "15m",
		},
		"status": map[string]any{
			"refreshTime": "2026-04-18T11:30:00Z",
		},
	}}

	info, err := FromUnstructured(obj, time.Hour)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if info.Name != "demo" || info.Namespace != "apps" {
		t.Fatalf("unexpected identity: %s/%s", info.Namespace, info.Name)
	}
	if info.RefreshInterval == nil || *info.RefreshInterval != 15*time.Minute {
		t.Fatalf("expected 15m refresh interval, got %v", info.RefreshInterval)
	}
	if info.RefreshTime == nil || !info.RefreshTime.Equal(time.Date(2026, 4, 18, 11, 30, 0, 0, time.UTC)) {
		t.Fatalf("unexpected refresh time: %v", info.RefreshTime)
	}
}
