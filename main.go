package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"

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
	}

	injector := webhook.NewInjector(cfg, log)

	mux := http.NewServeMux()
	mux.Handle("/mutate", webhook.NewHandler(injector, log))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	log.Info("micewriter-k8s-injector starting", "port", *port)
	if err := http.ListenAndServeTLS(":"+*port, *certFile, *keyFile, mux); err != nil {
		log.Error("server exited", "err", err)
		os.Exit(1)
	}
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}
