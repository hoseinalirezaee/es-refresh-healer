package patcher

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

const (
	LastKickAnnotation   = "healer.external-secrets.io/last-kick"
	LastReasonAnnotation = "healer.external-secrets.io/last-reason"
	ReasonStaleRefresh   = "stale-refresh"
)

type AnnotationPatcher struct {
	resource dynamic.ResourceInterface
}

func New(resource dynamic.ResourceInterface) *AnnotationPatcher {
	return &AnnotationPatcher{resource: resource}
}

func BuildPatch(unixTS int64, reason string) ([]byte, error) {
	patch := map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]string{
				LastKickAnnotation:   strconv.FormatInt(unixTS, 10),
				LastReasonAnnotation: reason,
			},
		},
	}

	out, err := json.Marshal(patch)
	if err != nil {
		return nil, fmt.Errorf("marshal annotation patch: %w", err)
	}
	return out, nil
}

func (p *AnnotationPatcher) Patch(ctx context.Context, name string, unixTS int64, reason string) error {
	patch, err := BuildPatch(unixTS, reason)
	if err != nil {
		return err
	}
	_, err = p.resource.Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("patch ExternalSecret %s annotations: %w", name, err)
	}
	return nil
}
