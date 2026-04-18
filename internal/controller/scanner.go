package controller

import (
	"context"
	"log/slog"
	"time"

	"github.com/hoseinalirezaee/es-refresh-healer/internal/metrics"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

type Scanner struct {
	controller *Controller
	log        *slog.Logger
}

func NewScanner(controller *Controller) *Scanner {
	return &Scanner{
		controller: controller,
		log:        controller.log.With("component", "scanner"),
	}
}

func (s *Scanner) Run(ctx context.Context) {
	ticker := time.NewTicker(s.controller.cfg.ScanInterval)
	defer ticker.Stop()

	s.scan(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.scan(ctx)
		}
	}
}

func (s *Scanner) scan(ctx context.Context) {
	namespaces := s.controller.cfg.WatchNamespaces
	if len(namespaces) == 0 {
		namespaces = []string{metav1.NamespaceAll}
	}

	for _, namespace := range namespaces {
		list, err := s.controller.dynamic.Resource(s.controller.gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			metrics.ScanTotal.WithLabelValues("error").Inc()
			s.log.Warn("scan failed", "namespace", namespace, "error", err)
			continue
		}

		for i := range list.Items {
			item := list.Items[i]
			if err := s.controller.Handle(ctx, &item, "scan"); err != nil {
				s.log.Warn("scan handling failed", "namespace", item.GetNamespace(), "name", item.GetName(), "error", err)
				if key, keyErr := cache.MetaNamespaceKeyFunc(&item); keyErr == nil {
					s.controller.queue.AddRateLimited(key)
				}
			}
		}
		metrics.ScanTotal.WithLabelValues("success").Inc()
	}
}
