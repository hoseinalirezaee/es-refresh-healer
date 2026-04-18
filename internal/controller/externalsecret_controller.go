package controller

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/hoseinalirezaee/es-refresh-healer/internal/config"
	"github.com/hoseinalirezaee/es-refresh-healer/internal/metrics"
	"github.com/hoseinalirezaee/es-refresh-healer/internal/patcher"
	"github.com/hoseinalirezaee/es-refresh-healer/internal/ratelimit"
	"github.com/hoseinalirezaee/es-refresh-healer/internal/staleness"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

const (
	controllerName = "es-refresh-healer"
	queueName      = "externalsecrets"
)

type Controller struct {
	cfg       config.Config
	gvr       schema.GroupVersionResource
	gvk       schema.GroupVersionKind
	dynamic   dynamic.Interface
	informers []cache.SharedIndexInformer
	queue     workqueue.RateLimitingInterface
	evaluator staleness.Evaluator
	limiter   *ratelimit.Limiter
	cooldown  *ratelimit.Cooldown
	log       *slog.Logger
}

func New(
	cfg config.Config,
	dynamicClient dynamic.Interface,
	log *slog.Logger,
) (*Controller, error) {
	gvr := schema.GroupVersionResource{
		Group:    "external-secrets.io",
		Version:  cfg.ExternalSecretVersion,
		Resource: "externalsecrets",
	}
	gvk := schema.GroupVersionKind{
		Group:   gvr.Group,
		Version: gvr.Version,
		Kind:    "ExternalSecret",
	}

	c := &Controller{
		cfg:     cfg,
		gvr:     gvr,
		gvk:     gvk,
		dynamic: dynamicClient,
		queue:   workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), queueName),
		evaluator: staleness.NewEvaluator(staleness.Config{
			DefaultRefreshInterval:   cfg.DefaultRefreshInterval,
			StaleMultiplier:          cfg.StaleMultiplier,
			GracePeriod:              cfg.GracePeriod,
			MaxAllowedLag:            cfg.MaxAllowedLag,
			AllowZeroRefreshInterval: cfg.AllowZeroRefreshInterval,
		}),
		limiter:  ratelimit.New(cfg.MaxPatchesPerMinute),
		cooldown: ratelimit.NewCooldown(cfg.Cooldown),
		log:      log.With("controller", controllerName),
	}

	c.informers = c.buildInformers()
	return c, nil
}

func (c *Controller) Run(ctx context.Context, workers int) error {
	defer c.queue.ShutDown()

	for _, informer := range c.informers {
		go informer.Run(ctx.Done())
	}

	for _, informer := range c.informers {
		if ok := cache.WaitForCacheSync(ctx.Done(), informer.HasSynced); !ok {
			return fmt.Errorf("cache sync failed")
		}
	}

	for i := 0; i < workers; i++ {
		go wait.UntilWithContext(ctx, c.runWorker, time.Second)
	}

	scanner := NewScanner(c)
	go scanner.Run(ctx)

	<-ctx.Done()
	return nil
}

func (c *Controller) buildInformers() []cache.SharedIndexInformer {
	namespaces := c.cfg.WatchNamespaces
	if len(namespaces) == 0 {
		namespaces = []string{metav1.NamespaceAll}
	}

	informers := make([]cache.SharedIndexInformer, 0, len(namespaces))
	for _, namespace := range namespaces {
		factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(c.dynamic, 0, namespace, nil)
		informer := factory.ForResource(c.gvr).Informer()
		if _, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) {
				c.enqueue(obj)
			},
			UpdateFunc: func(_, newObj any) {
				c.enqueue(newObj)
			},
		}); err != nil {
			c.log.Error("failed to register informer event handler", "namespace", namespace, "error", err)
		}
		informers = append(informers, informer)
	}
	return informers
}

func (c *Controller) enqueue(obj any) {
	externalSecret, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return
	}
	if !c.namespaceAllowed(externalSecret.GetNamespace()) {
		return
	}

	key, err := cache.MetaNamespaceKeyFunc(externalSecret)
	if err != nil {
		c.log.Warn("failed to build queue key", "error", err)
		return
	}
	c.queue.Add(key)
}

func (c *Controller) runWorker(ctx context.Context) {
	for c.processNextItem(ctx) {
	}
}

func (c *Controller) processNextItem(ctx context.Context) bool {
	item, shutdown := c.queue.Get()
	if shutdown {
		return false
	}
	defer c.queue.Done(item)

	key, ok := item.(string)
	if !ok {
		c.queue.Forget(item)
		return true
	}

	if err := c.reconcile(ctx, key); err != nil {
		c.log.Warn("reconcile failed", "key", key, "error", err)
		c.queue.AddRateLimited(key)
		return true
	}

	c.queue.Forget(item)
	return true
}

func (c *Controller) reconcile(ctx context.Context, key string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}
	if !c.namespaceAllowed(namespace) {
		return nil
	}

	obj, err := c.dynamic.Resource(c.gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("get ExternalSecret %s: %w", key, err)
	}
	return c.Handle(ctx, obj, "watch")
}

func (c *Controller) Handle(ctx context.Context, obj *unstructured.Unstructured, source string) error {
	if !c.namespaceAllowed(obj.GetNamespace()) {
		return nil
	}

	metrics.ExternalSecretsSeenTotal.WithLabelValues(source).Inc()

	now := time.Now()
	info, err := staleness.FromUnstructured(obj, c.cfg.DefaultRefreshInterval)
	if err != nil {
		c.log.Warn("failed to evaluate ExternalSecret", "namespace", obj.GetNamespace(), "name", obj.GetName(), "error", err)
		return nil
	}

	evaluation := c.evaluator.Evaluate(info, now)
	metrics.ObserveLag(evaluation.Lag)

	attrs := []any{
		"source", source,
		"namespace", info.Namespace,
		"name", info.Name,
		"lag", evaluation.Lag.String(),
		"interval", evaluation.EffectiveInterval.String(),
		"threshold", evaluation.Threshold.String(),
		"stale", evaluation.Stale,
		"reason", evaluation.Reason,
	}

	if evaluation.Skipped {
		c.log.Debug("skipped ExternalSecret", attrs...)
		return nil
	}
	if !evaluation.Stale {
		c.log.Debug("ExternalSecret is fresh", attrs...)
		return nil
	}

	metrics.ExternalSecretsStaleTotal.WithLabelValues(evaluation.Reason).Inc()
	c.log.Info("stale ExternalSecret detected", attrs...)

	key := fmt.Sprintf("%s/%s", info.Namespace, info.Name)
	if allowed, elapsed := staleness.AnnotationCooldownExpired(info.Annotations, now, c.cfg.Cooldown); !allowed {
		metrics.ExternalSecretCooldownSkippedTotal.Inc()
		c.log.Debug("cooldown annotation still active", "namespace", info.Namespace, "name", info.Name, "elapsed", elapsed.String(), "cooldown", c.cfg.Cooldown.String())
		return nil
	}
	if !c.cooldown.Allow(key, now) {
		metrics.ExternalSecretCooldownSkippedTotal.Inc()
		c.log.Debug("in-memory cooldown still active", "namespace", info.Namespace, "name", info.Name, "cooldown", c.cfg.Cooldown.String())
		return nil
	}
	if !c.limiter.Allow() {
		metrics.ExternalSecretRateLimitedTotal.Inc()
		c.log.Warn("global patch rate limit reached", "namespace", info.Namespace, "name", info.Name, "maxPatchesPerMinute", c.cfg.MaxPatchesPerMinute)
		return nil
	}

	if c.cfg.DryRun {
		metrics.ExternalSecretsPatchedTotal.WithLabelValues(metrics.BoolLabel(true)).Inc()
		c.cooldown.Mark(key, now)
		c.log.Info("dry-run would patch ExternalSecret", "namespace", info.Namespace, "name", info.Name)
		return nil
	}

	resource := c.dynamic.Resource(c.gvr).Namespace(info.Namespace)
	if err := patcher.New(resource).Patch(ctx, info.Name, now.Unix(), patcher.ReasonStaleRefresh); err != nil {
		metrics.ExternalSecretPatchErrorsTotal.WithLabelValues("api_error").Inc()
		return err
	}

	c.cooldown.Mark(key, now)
	metrics.ExternalSecretsPatchedTotal.WithLabelValues(metrics.BoolLabel(false)).Inc()
	c.log.Info("patched ExternalSecret kick annotation", "namespace", info.Namespace, "name", info.Name)
	return nil
}

func (c *Controller) namespaceAllowed(namespace string) bool {
	if len(c.cfg.WatchNamespaces) > 0 {
		found := false
		for _, allowed := range c.cfg.WatchNamespaces {
			if namespace == allowed {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	for _, denied := range c.cfg.DenyNamespaces {
		if namespace == denied {
			return false
		}
	}
	return true
}
