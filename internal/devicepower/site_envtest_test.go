package devicepower

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
)

var siteRoot = filepath.Join("..", "..", "config", "base", "device-power")

// TestSitePoliciesAdmitOnlyTheWorkerShape applies the shipped dispatcher RBAC
// and site admission policies to a disposable API server, then acts as the
// dispatcher's and a worker's ServiceAccounts.
func TestSitePoliciesAdmitOnlyTheWorkerShape(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS required for disposable API server validation")
	}
	environment := &envtest.Environment{}
	config, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Error(err)
		}
	})
	scheme, err := NewScheme()
	if err != nil {
		t.Fatal(err)
	}
	if err := admissionv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	admin, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, ns := range []*corev1.Namespace{{ObjectMeta: metav1.ObjectMeta{Name: siteNS, Labels: map[string]string{"fleet.datumapis.com/device-power-site": "true"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "elsewhere"}}} {
		if err := admin.Create(ctx, ns); err != nil {
			t.Fatal(err)
		}
	}
	var objs []client.Object
	for _, sa := range decodeFile[corev1.ServiceAccount](t, filepath.Join(siteRoot, "rbac.yaml"))[:1] {
		objs = append(objs, &sa)
	}
	role := decodeFile[rbacv1.Role](t, filepath.Join(siteRoot, "rbac.yaml"))[1]
	binding := decodeFile[rbacv1.RoleBinding](t, filepath.Join(siteRoot, "rbac.yaml"))[2]
	params := decodeFile[corev1.ConfigMap](t, filepath.Join(siteRoot, "worker-policy-params.yaml"))[0]
	params.Data["workerImage"] = workerImage
	objs = append(objs, &role, &binding, &params)
	for _, obj := range objs {
		obj.SetNamespace(siteNS)
	}
	policies := decodeFile[admissionv1.ValidatingAdmissionPolicy](t, filepath.Join(siteRoot, "worker-policy.yaml"))
	bindings := decodeFile[admissionv1.ValidatingAdmissionPolicyBinding](t, filepath.Join(siteRoot, "worker-policy.yaml"))
	objs = append(objs, &policies[0], &bindings[1], &policies[2], &bindings[3])
	for _, obj := range objs {
		obj.GetObjectKind().SetGroupVersionKind(obj.GetObjectKind().GroupVersionKind())
		if err := admin.Create(ctx, obj); err != nil {
			t.Fatalf("create %s: %v", obj.GetName(), err)
		}
	}
	as := func(user string) client.Client {
		c := rest.CopyConfig(config)
		c.Impersonate = rest.ImpersonationConfig{UserName: user}
		out, err := client.New(c, client.Options{Scheme: scheme})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	dispatcher := as("system:serviceaccount:" + siteNS + ":fleet-device-power-dispatcher")
	op := operation("op", "device-a", inventory.PowerForceRestart)
	in := Inputs{OperationUID: string(op.UID), Project: project, Namespace: deviceNS, Operation: op.Name, Device: "device-a", Endpoint: endpoint,
		SystemUUID: uuidA, PublicKeySHA256: keyA, Action: op.Spec.Action, CapacityCheckedAt: metav1.NewTime(t0)}
	rec := record(siteNS, in)
	// The policies take a moment to be enforced; until then a forbidden
	// shape could slip through, so wait for one denial first.
	waitDenied(t, func() error {
		bad := record(siteNS, in)
		bad.Name = "not-a-worker"
		err := dispatcher.Create(ctx, bad)
		if err == nil {
			_ = admin.Delete(ctx, bad)
		}
		return err
	})
	if err := dispatcher.Create(ctx, rec); err != nil {
		t.Fatal(err)
	}
	sa, workerRole, workerBinding, job := workerObjects(rec, in, workerImage, "bmc.default.device-a", 600)
	for _, obj := range []client.Object{sa, workerRole, workerBinding, job} {
		if err := dispatcher.Create(ctx, obj); err != nil {
			t.Fatalf("the worker shape was refused: %s: %v", obj.GetName(), err)
		}
	}
	lease := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: LockName(project, deviceNS, "device-a"), Namespace: siteNS, Labels: objectLabels(in.OperationUID, "lock")},
		Spec: coordinationv1.LeaseSpec{HolderIdentity: ptr.To(in.OperationUID)}}
	if err := dispatcher.Create(ctx, lease); err != nil {
		t.Fatal(err)
	}
	// Each deviation from the worker shape is denied.
	clone := func(name string) (*corev1.ServiceAccount, *rbacv1.Role, *rbacv1.RoleBinding, *batchv1.Job) {
		r := rec.DeepCopy()
		r.Name = name
		return workerObjects(r, in, workerImage, "bmc.default.device-a", 600)
	}
	for name, mutate := range map[string]func() client.Object{
		"another image": func() client.Object {
			_, _, _, j := clone("pwr-image")
			j.Spec.Template.Spec.Containers[0].Image = "example.invalid/other:1"
			return j
		},
		"another Device's credential": func() client.Object {
			_, _, _, j := clone("pwr-secret")
			j.Spec.Template.Spec.Volumes[0].Secret.SecretName = "bmc.default.device-b"
			return j
		},
		"a second volume": func() client.Object {
			_, _, _, j := clone("pwr-volume")
			j.Spec.Template.Spec.Volumes = append(j.Spec.Template.Spec.Volumes, corev1.Volume{Name: "extra", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "fleet-bmc-discovery-milo-kubeconfig"}}})
			return j
		},
		"a secret in the environment": func() client.Object {
			_, _, _, j := clone("pwr-env")
			j.Spec.Template.Spec.Containers[0].Env = append(j.Spec.Template.Spec.Containers[0].Env, corev1.EnvVar{Name: "X", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "bmc.default.device-b"}, Key: "password"}}})
			return j
		},
		"privileged": func() client.Object {
			_, _, _, j := clone("pwr-priv")
			j.Spec.Template.Spec.Containers[0].SecurityContext.Privileged = ptr.To(true)
			return j
		},
		"host network": func() client.Object {
			_, _, _, j := clone("pwr-host")
			j.Spec.Template.Spec.HostNetwork = true
			return j
		},
		"retries": func() client.Object {
			_, _, _, j := clone("pwr-retry")
			j.Spec.BackoffLimit = ptr.To[int32](3)
			return j
		},
		"the dispatcher's identity": func() client.Object {
			_, _, _, j := clone("pwr-identity")
			j.Spec.Template.Spec.ServiceAccountName = "fleet-device-power-dispatcher"
			return j
		},
		"another command": func() client.Object {
			_, _, _, j := clone("pwr-command")
			j.Spec.Template.Spec.Containers[0].Command = []string{"/fleet-device-power", "dispatcher"}
			return j
		},
		"a Role reading Secrets": func() client.Object {
			_, r, _, _ := clone("pwr-role")
			r.Rules = append(r.Rules, rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get"}})
			return r
		},
		"a Role on another record": func() client.Object {
			_, r, _, _ := clone("pwr-role2")
			r.Rules[0].ResourceNames = []string{rec.Name}
			return r
		},
		"a binding to another identity": func() client.Object {
			_, r, b, _ := clone("pwr-bind")
			// The Role exists, so only the policy can deny the binding.
			if err := dispatcher.Create(ctx, r); err != nil {
				t.Fatal(err)
			}
			b.Subjects[0].Name = "fleet-device-power-dispatcher"
			return b
		},
		"a non-worker object": func() client.Object {
			return &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "fleet-bmc-discovery-2", Namespace: siteNS, Labels: map[string]string{PartOfLabel: PartOf}}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := dispatcher.Create(ctx, mutate()); !apierrors.IsForbidden(err) && !apierrors.IsInvalid(err) {
				t.Fatalf("admitted: %v", err)
			}
		})
	}
	// RBAC: the dispatcher cannot read Secrets or create Pods.
	if err := dispatcher.Get(ctx, client.ObjectKey{Namespace: siteNS, Name: "bmc.default.device-a"}, &corev1.Secret{}); !apierrors.IsForbidden(err) {
		t.Fatalf("the dispatcher read a Secret: %v", err)
	}
	if err := dispatcher.Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pwr-pod", Namespace: siteNS},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: workerImage}}}}); !apierrors.IsForbidden(err) {
		t.Fatalf("the dispatcher created a Pod: %v", err)
	}
	// The dispatcher changes a record's progress, not its inputs.
	changed := rec.DeepCopy()
	changed.Data[InputsKey] = encode(Inputs{OperationUID: in.OperationUID, Endpoint: "https://192.0.2.99:443"})
	if err := dispatcher.Update(ctx, changed); !apierrors.IsForbidden(err) && !apierrors.IsInvalid(err) {
		t.Fatalf("the dispatcher changed inputs: %v", err)
	}
	// A worker updates its own record's progress only.
	worker := as("system:serviceaccount:" + siteNS + ":" + rec.Name)
	var mine corev1.ConfigMap
	if err := worker.Get(ctx, client.ObjectKeyFromObject(rec), &mine); err != nil {
		t.Fatal(err)
	}
	setProgress(&mine, Progress{Stage: StageClaimed, ClaimedBy: "pod"})
	if err := worker.Update(ctx, &mine); err != nil {
		t.Fatalf("the worker could not claim: %v", err)
	}
	mine.Data[InputsKey] = encode(Inputs{OperationUID: in.OperationUID, Endpoint: "https://192.0.2.99:443"})
	if err := worker.Update(ctx, &mine); !apierrors.IsForbidden(err) && !apierrors.IsInvalid(err) {
		t.Fatalf("the worker changed its inputs: %v", err)
	}
	for _, key := range []client.ObjectKey{{Namespace: siteNS, Name: "bmc.default.device-a"}, {Namespace: siteNS, Name: "fleet-device-power-worker-policy"}} {
		var target client.Object = &corev1.ConfigMap{}
		if key.Name[:4] == "bmc." {
			target = &corev1.Secret{}
		}
		if err := worker.Get(ctx, key, target); !apierrors.IsForbidden(err) {
			t.Fatalf("the worker read %s: %v", key.Name, err)
		}
	}
	// Writes in other namespaces, which hold no policy parameter, are not
	// touched by the policies: system leader election keeps working.
	other := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: "kube-controller-manager", Namespace: "elsewhere"}}
	if err := admin.Create(ctx, other); err != nil {
		t.Fatalf("a Lease outside the site namespace was denied: %v", err)
	}
	other.Spec.HolderIdentity = ptr.To("controller")
	if err := admin.Update(ctx, other); err != nil {
		t.Fatalf("a Lease update outside the site namespace was denied: %v", err)
	}
	// Deletes are limited to records and locks.
	if err := dispatcher.Delete(ctx, lease); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Delete(ctx, &params); !apierrors.IsForbidden(err) && !apierrors.IsInvalid(err) {
		t.Fatalf("the dispatcher deleted the policy parameter: %v", err)
	}
}

func waitDenied(t *testing.T, try func() error) {
	t.Helper()
	var err error
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if err = try(); apierrors.IsForbidden(err) || apierrors.IsInvalid(err) {
			return
		}
	}
	t.Fatalf("policy not enforced: %v", err)
}
