package devicepower

import (
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

// Worker container contract.
const (
	WorkerCommand = "/fleet-device-power"
	// CredentialMount holds the mounted credential Secret's username,
	// password and tlsPublicKeySHA256 files.
	CredentialMount = "/var/run/fleet/bmc"
	// RecordEnv names the stage record; PodUIDEnv carries the worker Pod's
	// UID as its claim identity.
	RecordEnv = "FLEET_POWER_RECORD"
	PodUIDEnv = "FLEET_POWER_POD_UID"
)

func objectLabels(uid, component string) map[string]string {
	return map[string]string{PartOfLabel: PartOf, ComponentLabel: component, OperationUIDLabel: uid}
}

// record builds the stage record of an accepted operation.
func record(namespace string, in Inputs) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: WorkerName(uidOf(in)), Namespace: namespace, Labels: objectLabels(in.OperationUID, "record"),
			Annotations: map[string]string{OperationNamespace: in.Namespace, OperationName: in.Operation, DeviceNamespace: in.Namespace, DeviceName: in.Device}},
		Data: map[string]string{InputsKey: encode(in), ProgressKey: encode(Progress{Stage: StagePending})},
	}
}

// owned makes a worker object a dependent of its stage record, so deleting
// the record after retention removes all of them.
func owned(rec *corev1.ConfigMap) []metav1.OwnerReference {
	return []metav1.OwnerReference{{APIVersion: "v1", Kind: "ConfigMap", Name: rec.Name, UID: rec.UID}}
}

// workerObjects builds the ServiceAccount, Role, RoleBinding and Job of one
// operation. The Role grants get and update on the stage record and nothing
// else; the Job mounts exactly the Device's credential Secret.
func workerObjects(rec *corev1.ConfigMap, in Inputs, image, secret string, deadline int64) (*corev1.ServiceAccount, *rbacv1.Role, *rbacv1.RoleBinding, *batchv1.Job) {
	name, ns := rec.Name, rec.Namespace
	meta := func(component string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: name, Namespace: ns, Labels: objectLabels(in.OperationUID, component), OwnerReferences: owned(rec),
			Annotations: map[string]string{OperationNamespace: in.Namespace, OperationName: in.Operation, DeviceNamespace: in.Namespace, DeviceName: in.Device}}
	}
	sa := &corev1.ServiceAccount{ObjectMeta: meta("worker")}
	role := &rbacv1.Role{ObjectMeta: meta("worker"), Rules: []rbacv1.PolicyRule{{
		APIGroups: []string{""}, Resources: []string{"configmaps"}, ResourceNames: []string{name}, Verbs: []string{"get", "update"}}}}
	binding := &rbacv1.RoleBinding{ObjectMeta: meta("worker"),
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name},
		Subjects: []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: name, Namespace: ns}}}
	podLabels := objectLabels(in.OperationUID, "worker")
	job := &batchv1.Job{ObjectMeta: meta("worker"), Spec: batchv1.JobSpec{
		// One Pod, never replaced: a second worker would stop at the claim,
		// but none is started on purpose.
		BackoffLimit:          ptr.To[int32](0),
		PodReplacementPolicy:  ptr.To(batchv1.Failed),
		ActiveDeadlineSeconds: ptr.To(deadline),
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: podLabels, Annotations: map[string]string{DeviceNamespace: in.Namespace, DeviceName: in.Device}},
			Spec: corev1.PodSpec{
				ServiceAccountName:           name,
				AutomountServiceAccountToken: ptr.To(true),
				RestartPolicy:                corev1.RestartPolicyNever,
				EnableServiceLinks:           ptr.To(false),
				SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To[int64](65532), RunAsGroup: ptr.To[int64](65532),
					FSGroup: ptr.To[int64](65532), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
				Containers: []corev1.Container{{
					Name:    "worker",
					Image:   image,
					Command: []string{WorkerCommand, "worker"},
					Env: []corev1.EnvVar{{Name: RecordEnv, Value: name}, {Name: "POD_NAMESPACE", Value: ns},
						{Name: PodUIDEnv, ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.uid"}}}},
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("32Mi")},
						Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")}},
					SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true),
						Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
					VolumeMounts: []corev1.VolumeMount{{Name: "credential", MountPath: CredentialMount, ReadOnly: true}},
				}},
				// Optional, so a missing Secret ends the operation as Failed
				// instead of a Pod that never starts.
				Volumes: []corev1.Volume{{Name: "credential", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
					SecretName: secret, Optional: ptr.To(true), DefaultMode: ptr.To[int32](0o440)}}}},
			},
		},
	}}
	return sa, role, binding, job
}
