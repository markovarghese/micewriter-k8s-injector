package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/markovarghese/micewriter-k8s-injector/internal/webhook"
)

func main() {
	port     := flag.String("port", "8443", "HTTPS port to listen on")
	certFile := flag.String("tls-cert", "/tls/tls.crt", "Path to TLS certificate file")
	keyFile  := flag.String("tls-key", "/tls/tls.key", "Path to TLS private key file")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg := webhook.InjectorConfig{
		EngineImage:         getEnv("ENGINE_IMAGE", "micewriter-engine:latest"),
		MinioURL:            getEnv("MINIO_URL", ""),
		MinioAccessKey:      getEnv("MINIO_ACCESS_KEY", ""),
		MinioSecretKey:      getEnv("MINIO_SECRET_KEY", ""),
		MinioBucket:         getEnv("MINIO_BUCKET", "iceberg"),
		NessieURI:           getEnv("NESSIE_URI", ""),
		NessieWarehouse:     getEnv("NESSIE_WAREHOUSE", "s3://iceberg"),
		RocksdbStorageClass: getEnv("ROCKSDB_STORAGE_CLASS", "local-path"),
		RocksdbStorageSize:  getEnv("ROCKSDB_STORAGE_SIZE", "10Gi"),
		EngineCpuRequest:    getEnv("ENGINE_CPU_REQUEST", "100m"),
		EngineMemRequest:    getEnv("ENGINE_MEM_REQUEST", "128Mi"),
		EngineCpuLimit:      getEnv("ENGINE_CPU_LIMIT", "500m"),
		EngineMemLimit:      getEnv("ENGINE_MEM_LIMIT", "512Mi"),
		EnableManualFlush:   getEnv("ENABLE_MANUAL_FLUSH", "false"),
	}

	if err := cfg.Validate(); err != nil {
		log.Error("invalid configuration", "err", err)
		os.Exit(1)
	}

	injector := webhook.NewInjector(cfg, log)

	mux := http.NewServeMux()
	mux.Handle("/mutate", webhook.NewHandler(injector, log))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := &http.Server{
		Addr:    ":" + *port,
		Handler: mux,
	}

	go func() {
		log.Info("micewriter-k8s-injector starting", "port", *port)
		if err := srv.ListenAndServeTLS(*certFile, *keyFile); err != nil && err != http.ErrServerClosed {
			log.Error("server exited", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	log.Info("shutting down server...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Error("server shutdown failed", "err", err)
	} else {
		log.Info("server gracefully stopped")
	}
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}
