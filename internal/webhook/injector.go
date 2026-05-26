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
	injectAnnotation  = "iceberg-stream.yourcompany.com/inject"
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
}

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

	if len(pod.Spec.Volumes) == 0 {
		// Create the volumes array from scratch.
		patches = append(patches, jsonPatch{
			Op:    "add",
			Path:  "/spec/volumes",
			Value: []corev1.Volume{sockVol, rocksdbVol},
		})
	} else {
		patches = append(patches,
			jsonPatch{Op: "add", Path: "/spec/volumes/-", Value: sockVol},
			jsonPatch{Op: "add", Path: "/spec/volumes/-", Value: rocksdbVol},
		)
	}

	// --- 2. Socket volume mount on every existing container -------------------
	// The SDK running in the app container connects to the socket the engine
	// writes at /var/run/app/iceberg.sock. Both sides need the shared emptyDir.
	sockMount := corev1.VolumeMount{Name: sockVolumeName, MountPath: sockMountPath}
	for i, c := range pod.Spec.Containers {
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
		ImagePullPolicy: corev1.PullIfNotPresent,
		Env: []corev1.EnvVar{
			{Name: "MINIO_URL", Value: inj.cfg.MinioURL},
			{Name: "MINIO_ACCESS_KEY", Value: inj.cfg.MinioAccessKey},
			{Name: "MINIO_SECRET_KEY", Value: inj.cfg.MinioSecretKey},
			{Name: "MINIO_BUCKET", Value: inj.cfg.MinioBucket},
			{Name: "NESSIE_URI", Value: inj.cfg.NessieURI},
			{Name: "NESSIE_WAREHOUSE", Value: inj.cfg.NessieWarehouse},
			{Name: "SOCKET_PATH", Value: sockMountPath + "/iceberg.sock"},
			{Name: "ROCKSDB_PATH", Value: rocksdbMountPath},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: sockVolumeName, MountPath: sockMountPath},
			{Name: rocksdbVolumeName, MountPath: rocksdbMountPath},
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("128Mi"),
				corev1.ResourceCPU:    resource.MustParse("100m"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("512Mi"),
				corev1.ResourceCPU:    resource.MustParse("500m"),
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
