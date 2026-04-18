package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/hoseinalirezaee/es-refresh-healer/internal/config"
	"github.com/hoseinalirezaee/es-refresh-healer/internal/controller"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/client-go/util/homedir"
)

func main() {
	cfg, err := config.Parse(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "configuration error: %v\n", err)
		os.Exit(2)
	}

	log := newLogger(cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go serveMetrics(ctx, cfg.MetricsAddr, log)

	restConfig, err := buildKubernetesConfig(cfg.Kubeconfig)
	if err != nil {
		log.Error("failed to build Kubernetes config", "error", err)
		os.Exit(1)
	}

	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		log.Error("failed to create dynamic Kubernetes client", "error", err)
		os.Exit(1)
	}
	kubeClient, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		log.Error("failed to create Kubernetes client", "error", err)
		os.Exit(1)
	}

	ctrl, err := controller.New(cfg, dynamicClient, log)
	if err != nil {
		log.Error("failed to create controller", "error", err)
		os.Exit(1)
	}

	if !cfg.LeaderElect {
		log.Info("starting without leader election")
		if err := ctrl.Run(ctx, 2); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("controller stopped", "error", err)
			os.Exit(1)
		}
		return
	}

	runWithLeaderElection(ctx, cfg, kubeClient, ctrl, log)
}

func runWithLeaderElection(
	ctx context.Context,
	cfg config.Config,
	kubeClient kubernetes.Interface,
	ctrl *controller.Controller,
	log *slog.Logger,
) {
	identity := leaderIdentity()
	lock, err := resourcelock.New(
		resourcelock.LeasesResourceLock,
		cfg.LeaderElectionNamespace,
		"es-refresh-healer",
		kubeClient.CoreV1(),
		kubeClient.CoordinationV1(),
		resourcelock.ResourceLockConfig{Identity: identity},
	)
	if err != nil {
		log.Error("failed to create leader election lock", "error", err)
		os.Exit(1)
	}

	leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
		Lock:            lock,
		ReleaseOnCancel: true,
		LeaseDuration:   15 * time.Second,
		RenewDeadline:   10 * time.Second,
		RetryPeriod:     2 * time.Second,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {
				log.Info("leader acquired", "identity", identity)
				if err := ctrl.Run(ctx, 2); err != nil && !errors.Is(err, context.Canceled) {
					log.Error("controller stopped", "error", err)
				}
			},
			OnStoppedLeading: func() {
				log.Error("leader election lost", "identity", identity)
			},
			OnNewLeader: func(current string) {
				if current != identity {
					log.Info("new leader observed", "identity", current)
				}
			},
		},
	})
}

func serveMetrics(ctx context.Context, addr string, log *slog.Logger) {
	if strings.TrimSpace(addr) == "" {
		return
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	log.Info("metrics server listening", "addr", addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("metrics server failed", "error", err)
	}
}

func buildKubernetesConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}

	inCluster, err := rest.InClusterConfig()
	if err == nil {
		return inCluster, nil
	}

	home := homedir.HomeDir()
	if home == "" {
		return nil, err
	}

	local := filepath.Join(home, ".kube", "config")
	if _, statErr := os.Stat(local); statErr != nil {
		return nil, err
	}
	return clientcmd.BuildConfigFromFlags("", local)
}

func leaderIdentity() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}

	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return host
	}
	return host + "-" + hex.EncodeToString(random)
}

func newLogger(level string) *slog.Logger {
	var slogLevel slog.Level
	switch strings.ToLower(level) {
	case "debug":
		slogLevel = slog.LevelDebug
	case "warn", "warning":
		slogLevel = slog.LevelWarn
	case "error":
		slogLevel = slog.LevelError
	default:
		slogLevel = slog.LevelInfo
	}

	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slogLevel}))
}
