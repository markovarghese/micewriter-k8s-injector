# micewriter-k8s-injector
> Part of the [mIceWriter Ingestion Ecosystem](../micewriter-hub/README.md)

Kubernetes Mutating Webhook. When a pod is created with the annotation `iceberg-stream.yourcompany.com/inject: "true"`, this webhook automatically patches its spec to add:

| Addition | What it does |
|----------|-------------|
| `micewriter-engine` sidecar container | Runs the Rust caching/flushing engine |
| `emptyDir` volume at `/var/run/app` | Shared UDS socket between app and engine |
| Generic Ephemeral PVC at `/var/lib/rocksdb` | Dedicated high-IOPS storage for RocksDB |
| Volume mount on every existing container and `initContainer` | Gives the app container access to the UDS socket |

## Prerequisites

- `micewriter-local-infra` must be up (`.\run.ps1 up`) — it installs cert-manager and
  the in-cluster registry that this webhook image is pulled from.
- `micewriter-engine` image must be pushed (`.\push.ps1` in that repo) — the injector
  embeds the engine image reference in every mutated pod.

## Deploy

```powershell
# 1. Build the webhook image and push it to the local k3s registry
.\run.ps1 push

# 2. Deploy the Helm chart
.\run.ps1 deploy

# 3. Verify the webhook pod is running
.\run.ps1 deploy   # idempotent — safe to re-run
```

`helm` runs inside a Docker container — no native Helm install needed on the host.

> If `deploy` fails immediately with "no kind Issuer", wait ~10 s and retry. cert-manager
> CRDs can take a moment to be served after the pods become Available.

## Configuration

All engine endpoint values, resources, and failure policies are in `charts/micewriter-k8s-injector/values.yaml`.
Override at deploy time with `--set`:

```bash
helm upgrade --install micewriter-k8s-injector charts/micewriter-k8s-injector \
  --namespace micewriter-system --create-namespace \
  --set engine.minioUrl=http://my-minio:9000 \
  --set engine.nessieUri=http://my-nessie:19120/iceberg/v1 \
  --set engine.resources.limits.cpu=1000m
```

## How It Works

```
kubectl apply → API Server → MutatingWebhookConfiguration
                                     │
                              POST /mutate (HTTPS)
                                     │
                          micewriter-k8s-injector pod
                                     │
                           AdmissionReview.Request
                                     │
                   Check annotation: iceberg-stream.../inject=true?
                                     │  Yes
                           Build JSON Patch (RFC 6902):
                             add /spec/volumes (emptyDir + ephemeral PVC)
                             add /spec/containers/N/volumeMounts (UDS socket)
                             add /spec/containers/- (engine sidecar)
                                     │
                           AdmissionReview.Response → API Server → etcd
```

## File Structure

```
micewriter-k8s-injector/
  go.mod
  main.go                          # HTTP server, flag parsing, env config
  internal/webhook/
    handler.go                     # Decodes AdmissionReview, calls injector
    injector.go                    # JSON Patch builder, idempotency check
  charts/micewriter-k8s-injector/
    Chart.yaml
    values.yaml
    templates/
      namespace.yaml               # micewriter-system namespace
      issuer.yaml                  # cert-manager self-signed Issuer
      certificate.yaml             # TLS cert for the webhook service
      deployment.yaml              # Webhook server deployment
      service.yaml                 # ClusterIP service on port 443
      mutatingwebhook.yaml         # MutatingWebhookConfiguration
  Dockerfile                       # scratch image, ~10MB
  Makefile                         # alternative for Linux/Mac users
  run.ps1                          # Windows entry point (push / deploy / undeploy)
```

## Annotation Reference

Add this annotation to any pod template spec to enable injection:

```yaml
metadata:
  annotations:
    iceberg-stream.yourcompany.com/inject: "true"
```

The injector is idempotent — re-applying a manifest that already has the sidecar is safe, and defining existing matching volume names prevents duplicate volumes from being attached.
