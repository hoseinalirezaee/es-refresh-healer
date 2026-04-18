package controller

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/hoseinalirezaee/es-refresh-healer/internal/config"
	"github.com/hoseinalirezaee/es-refresh-healer/internal/patcher"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestHandlePatchesStaleExternalSecret(t *testing.T) {
	obj := testExternalSecret("apps", "stale", time.Now().Add(-5*time.Minute), nil)
	ctrl, dynamicClient := testController(t, false, obj)

	if err := ctrl.Handle(context.Background(), obj, "test"); err != nil {
		t.Fatalf("handle stale ExternalSecret: %v", err)
	}

	got := getExternalSecret(t, dynamicClient, "apps", "stale")
	annotations := got.GetAnnotations()
	if annotations[patcher.LastKickAnnotation] == "" {
		t.Fatalf("expected last-kick annotation to be patched")
	}
	if annotations[patcher.LastReasonAnnotation] != patcher.ReasonStaleRefresh {
		t.Fatalf("expected last-reason annotation, got %q", annotations[patcher.LastReasonAnnotation])
	}
}

func TestHandleLeavesFreshExternalSecretUntouched(t *testing.T) {
	obj := testExternalSecret("apps", "fresh", time.Now().Add(-30*time.Second), nil)
	ctrl, dynamicClient := testController(t, false, obj)

	if err := ctrl.Handle(context.Background(), obj, "test"); err != nil {
		t.Fatalf("handle fresh ExternalSecret: %v", err)
	}

	got := getExternalSecret(t, dynamicClient, "apps", "fresh")
	if got.GetAnnotations()[patcher.LastKickAnnotation] != "" {
		t.Fatalf("fresh ExternalSecret should not be patched")
	}
}

func TestHandleDryRunDoesNotPatch(t *testing.T) {
	obj := testExternalSecret("apps", "dry-run", time.Now().Add(-5*time.Minute), nil)
	ctrl, dynamicClient := testController(t, true, obj)

	if err := ctrl.Handle(context.Background(), obj, "test"); err != nil {
		t.Fatalf("handle dry-run ExternalSecret: %v", err)
	}

	got := getExternalSecret(t, dynamicClient, "apps", "dry-run")
	if got.GetAnnotations()[patcher.LastKickAnnotation] != "" {
		t.Fatalf("dry-run should not patch annotations")
	}
}

func TestHandleRespectsAnnotationCooldown(t *testing.T) {
	annotations := map[string]string{
		patcher.LastKickAnnotation: strconv.FormatInt(time.Now().Unix(), 10),
	}
	obj := testExternalSecret("apps", "cooldown", time.Now().Add(-5*time.Minute), annotations)
	ctrl, dynamicClient := testController(t, false, obj)

	if err := ctrl.Handle(context.Background(), obj, "test"); err != nil {
		t.Fatalf("handle cooldown ExternalSecret: %v", err)
	}

	got := getExternalSecret(t, dynamicClient, "apps", "cooldown")
	if got.GetAnnotations()[patcher.LastReasonAnnotation] != "" {
		t.Fatalf("cooldown should prevent patching last-reason annotation")
	}
}

func testController(t *testing.T, dryRun bool, objects ...runtime.Object) (*Controller, *dynamicfake.FakeDynamicClient) {
	t.Helper()

	dynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), objects...)
	ctrl, err := New(config.Config{
		ScanInterval:           time.Minute,
		DefaultRefreshInterval: time.Hour,
		StaleMultiplier:        1,
		GracePeriod:            0,
		Cooldown:               10 * time.Minute,
		MaxPatchesPerMinute:    100,
		DryRun:                 dryRun,
		LogLevel:               "debug",
		MetricsAddr:            ":0",
		ExternalSecretVersion:  "v1",
	}, dynamicClient, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new controller: %v", err)
	}

	return ctrl, dynamicClient
}

func testExternalSecret(namespace, name string, refreshTime time.Time, annotations map[string]string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "external-secrets.io/v1",
		"kind":       "ExternalSecret",
		"metadata": map[string]any{
			"name":              name,
			"namespace":         namespace,
			"creationTimestamp": time.Now().Add(-10 * time.Minute).Format(time.RFC3339),
		},
		"spec": map[string]any{
			"refreshInterval": "1m",
		},
		"status": map[string]any{
			"refreshTime": refreshTime.Format(time.RFC3339Nano),
		},
	}}
	obj.SetAnnotations(annotations)
	return obj
}

func getExternalSecret(t *testing.T, client *dynamicfake.FakeDynamicClient, namespace, name string) *unstructured.Unstructured {
	t.Helper()

	gvr := schema.GroupVersionResource{
		Group:    "external-secrets.io",
		Version:  "v1",
		Resource: "externalsecrets",
	}
	got, err := client.Resource(gvr).Namespace(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get ExternalSecret: %v", err)
	}
	return got
}
