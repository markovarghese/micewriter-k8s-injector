package webhook

import (
	"encoding/json"
	"fmt"
	"log/slog"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	injectAnnotation  = "iceberg-stream.micewriter.io/inject"
	sidecarName       = "micewriter-engine"
	sockVolumeName    = "iceberg-sock"
	rocksdbVolumeName = "rocksdb-cache"
	sockMountPath     = "/var/run/app"
	rocksdbMountPath  = "/var/lib/rocksdb"
)

// InjectorConfig holds the engine image and endpoint env vars that get
// propagated into every injected sidecar container.
type InjectorConfig struct {
	EngineImage         string
	MinioURL            string
	MinioAccessKey      string
	MinioSecretKey      string
	MinioBucket         string
	NessieURI           string
	NessieWarehouse     string
	RocksdbStorageClass string
	RocksdbStorageSize  string
	EngineCpuRequest    string
	EngineMemRequest    string
	EngineCpuLimit      string
	EngineMemLimit      string
	EnableManualFlush   string
}

func (c *InjectorConfig) Validate() error {
	for _, f := range []struct{ name, val string }{
		{"ENGINE_CPU_REQUEST", c.EngineCpuRequest},
		{"ENGINE_MEM_REQUEST", c.EngineMemRequest},
		{"ENGINE_CPU_LIMIT", c.EngineCpuLimit},
		{"ENGINE_MEM_LIMIT", c.EngineMemLimit},
		{"ROCKSDB_STORAGE_SIZE", c.RocksdbStorageSize},
	} {
		if _, err := resource.ParseQuantity(f.val); err != nil {
			return fmt.Errorf("%s=%q: %w", f.name, f.val, err)
		}
	}
	return nil
}

func boolPtr(b bool) *bool { return &b }

// Injector builds the JSON Patch that mutates an incoming Pod spec.
type Injector struct {
	cfg InjectorConfig
	log *slog.Logger
}

func NewInjector(cfg InjectorConfig, log *slog.Logger) *Injector {
	return &Injector{cfg: cfg, log: log}
}

// jsonPatch is one RFC 6902 operation.
type jsonPatch struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
}

// Mutate examines the incoming Pod and — when the inject annotation is present —
// returns a JSONPatch response that adds the engine sidecar, shared volumes, and
// volume mounts. Always returns Allowed: true (failurePolicy: Ignore in the webhook
// handles any unexpected errors gracefully).
func (inj *Injector) Mutate(req *admissionv1.AdmissionRequest) *admissionv1.AdmissionResponse {
	allow := &admissionv1.AdmissionResponse{Allowed: true}

	var pod corev1.Pod
	if err := json.Unmarshal(req.Object.Raw, &pod); err != nil {
		inj.log.Error("failed to decode pod object", "err", err)
		return allow // fail open
	}

	if pod.Annotations[injectAnnotation] != "true" {
		return allow
	}

	// Idempotency guard: skip if sidecar is already present (e.g. re-applied manifest).
	for _, c := range pod.Spec.Containers {
		if c.Name == sidecarName {
			inj.log.Info("sidecar already injected, skipping", "pod", req.Name, "namespace", req.Namespace)
			return allow
		}
	}

	inj.log.Info("injecting micewriter-engine sidecar", "pod", req.Name, "namespace", req.Namespace)

	patches := inj.buildPatches(&pod)

	patchBytes, err := json.Marshal(patches)
	if err != nil {
		inj.log.Error("failed to marshal JSON patch", "err", err)
		return allow
	}

	pt := admissionv1.PatchTypeJSONPatch
	return &admissionv1.AdmissionResponse{
		Allowed:   true,
		Patch:     patchBytes,
		PatchType: &pt,
	}
}

func (inj *Injector) buildPatches(pod *corev1.Pod) []jsonPatch {
	var patches []jsonPatch

	// --- 1. Volumes -----------------------------------------------------------
	sockVol := corev1.Volume{
		Name:         sockVolumeName,
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	}
	rocksdbVol := inj.ephemeralVolume()

	var addSockVol, addRocksdbVol = true, true
	for _, v := range pod.Spec.Volumes {
		if v.Name == sockVolumeName {
			addSockVol = false
		}
		if v.Name == rocksdbVolumeName {
			addRocksdbVol = false
		}
	}

	if len(pod.Spec.Volumes) == 0 {
		var vols []corev1.Volume
		if addSockVol { vols = append(vols, sockVol) }
		if addRocksdbVol { vols = append(vols, rocksdbVol) }
		if len(vols) > 0 {
			patches = append(patches, jsonPatch{
				Op:    "add",
				Path:  "/spec/volumes",
				Value: vols,
			})
		}
	} else {
		if addSockVol {
			patches = append(patches, jsonPatch{Op: "add", Path: "/spec/volumes/-", Value: sockVol})
		}
		if addRocksdbVol {
			patches = append(patches, jsonPatch{Op: "add", Path: "/spec/volumes/-", Value: rocksdbVol})
		}
	}

	// --- 2. Socket volume mount on every existing container -------------------
	// The SDK running in the app container connects to the socket the engine
	// writes at /var/run/app/iceberg.sock. Both sides need the shared emptyDir.
	sockMount := corev1.VolumeMount{Name: sockVolumeName, MountPath: sockMountPath}
	for i, c := range pod.Spec.Containers {
		hasMount := false
		for _, m := range c.VolumeMounts {
			if m.Name == sockVolumeName {
				hasMount = true
				break
			}
		}
		if !hasMount {
			if len(c.VolumeMounts) == 0 {
				patches = append(patches, jsonPatch{
					Op:    "add",
					Path:  fmt.Sprintf("/spec/containers/%d/volumeMounts", i),
					Value: []corev1.VolumeMount{sockMount},
				})
			} else {
				patches = append(patches, jsonPatch{
					Op:    "add",
					Path:  fmt.Sprintf("/spec/containers/%d/volumeMounts/-", i),
					Value: sockMount,
				})
			}
		}
	}

	for i, c := range pod.Spec.InitContainers {
		hasMount := false
		for _, m := range c.VolumeMounts {
			if m.Name == sockVolumeName {
				hasMount = true
				break
			}
		}
		if !hasMount {
			if len(c.VolumeMounts) == 0 {
				patches = append(patches, jsonPatch{
					Op:    "add",
					Path:  fmt.Sprintf("/spec/initContainers/%d/volumeMounts", i),
					Value: []corev1.VolumeMount{sockMount},
				})
			} else {
				patches = append(patches, jsonPatch{
					Op:    "add",
					Path:  fmt.Sprintf("/spec/initContainers/%d/volumeMounts/-", i),
					Value: sockMount,
				})
			}
		}
	}

	// --- 3. Engine sidecar container ------------------------------------------
	patches = append(patches, jsonPatch{
		Op:    "add",
		Path:  "/spec/containers/-",
		Value: inj.engineContainer(),
	})

	return patches
}

func (inj *Injector) engineContainer() corev1.Container {
	return corev1.Container{
		Name:            sidecarName,
		Image:           inj.cfg.EngineImage,
		ImagePullPolicy: corev1.PullAlways,
		Env: []corev1.EnvVar{
			{Name: "MINIO_URL", Value: inj.cfg.MinioURL},
			{Name: "MINIO_ACCESS_KEY", Value: inj.cfg.MinioAccessKey},
			{Name: "MINIO_SECRET_KEY", Value: inj.cfg.MinioSecretKey},
			{Name: "MINIO_BUCKET", Value: inj.cfg.MinioBucket},
			{Name: "NESSIE_URI", Value: inj.cfg.NessieURI},
			{Name: "NESSIE_WAREHOUSE", Value: inj.cfg.NessieWarehouse},
			{Name: "SOCKET_PATH", Value: sockMountPath + "/iceberg.sock"},
			{Name: "ROCKSDB_PATH", Value: rocksdbMountPath},
			{Name: "ENABLE_MANUAL_FLUSH", Value: inj.cfg.EnableManualFlush},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: sockVolumeName, MountPath: sockMountPath},
			{Name: rocksdbVolumeName, MountPath: rocksdbMountPath},
		},
		SecurityContext: &corev1.SecurityContext{
			RunAsNonRoot:             boolPtr(true),
			AllowPrivilegeEscalation: boolPtr(false),
			ReadOnlyRootFilesystem:   boolPtr(true),
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse(inj.cfg.EngineMemRequest),
				corev1.ResourceCPU:    resource.MustParse(inj.cfg.EngineCpuRequest),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse(inj.cfg.EngineMemLimit),
				corev1.ResourceCPU:    resource.MustParse(inj.cfg.EngineCpuLimit),
			},
		},
	}
}

func (inj *Injector) ephemeralVolume() corev1.Volume {
	sc := inj.cfg.RocksdbStorageClass
	return corev1.Volume{
		Name: rocksdbVolumeName,
		VolumeSource: corev1.VolumeSource{
			Ephemeral: &corev1.EphemeralVolumeSource{
				VolumeClaimTemplate: &corev1.PersistentVolumeClaimTemplate{
					ObjectMeta: metav1.ObjectMeta{},
					Spec: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: &sc,
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse(inj.cfg.RocksdbStorageSize),
							},
						},
					},
				},
			},
		},
	}
}
