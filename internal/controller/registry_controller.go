/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	bootstrapv1alpha1 "git.cia.net/n-at-han-k/bootstrap-operator/api/v1alpha1"
)

const registryFinalizer = "bootstrap.cia.net/registry-cleanup"

type RegistryReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=bootstrap.cia.net,resources=registries,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=bootstrap.cia.net,resources=registries/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=bootstrap.cia.net,resources=registries/finalizers,verbs=update

func (r *RegistryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var reg bootstrapv1alpha1.Registry
	if err := r.Get(ctx, req.NamespacedName, &reg); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !reg.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&reg, registryFinalizer) {
			controllerutil.RemoveFinalizer(&reg, registryFinalizer)
			if err := r.Update(ctx, &reg); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer
	if !controllerutil.ContainsFinalizer(&reg, registryFinalizer) {
		controllerutil.AddFinalizer(&reg, registryFinalizer)
		if err := r.Update(ctx, &reg); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Validate credentials Secret exists
	var credSecret corev1.Secret
	credKey := types.NamespacedName{Name: reg.Spec.CredentialsSecretRef.Name, Namespace: reg.Namespace}
	if err := r.Get(ctx, credKey, &credSecret); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("credentials secret not found, waiting", "secret", credKey.Name)
			return r.setStatus(ctx, &reg, false, "", fmt.Sprintf("credentials secret %q not found", credKey.Name))
		}
		return ctrl.Result{}, err
	}
	if len(credSecret.Data["username"]) == 0 || len(credSecret.Data["password"]) == 0 {
		return r.setStatus(ctx, &reg, false, "", fmt.Sprintf("credentials secret %q missing 'username' or 'password' key", credKey.Name))
	}

	// Reconcile StatefulSet
	if err := r.reconcileStatefulSet(ctx, &reg); err != nil {
		return r.setStatus(ctx, &reg, false, "", fmt.Sprintf("reconcile statefulset: %v", err))
	}

	// Reconcile Service
	if err := r.reconcileService(ctx, &reg); err != nil {
		return r.setStatus(ctx, &reg, false, "", fmt.Sprintf("reconcile service: %v", err))
	}

	url := fmt.Sprintf("http://%s.%s.svc.cluster.local", reg.Name, reg.Namespace)
	return r.setStatus(ctx, &reg, true, url, "OK")
}

func (r *RegistryReconciler) reconcileStatefulSet(ctx context.Context, reg *bootstrapv1alpha1.Registry) error {
	name := reg.Name
	labels := map[string]string{
		"app.kubernetes.io/name":       "registry",
		"app.kubernetes.io/instance":   name,
		"app.kubernetes.io/managed-by": "bootstrap-operator",
	}

	image := reg.Spec.Image
	if image == "" {
		image = "registry:2"
	}

	replicas := int32(1)

	// Shell entrypoint that generates htpasswd and a registry config, then starts
	// the Docker Distribution registry with HTTP basic auth.
	entrypoint := `#!/bin/sh
set -e

# Generate htpasswd file
apk add --no-cache apache2-utils
htpasswd -Bbn "$REG_USERNAME" "$REG_PASSWORD" > /etc/registry/htpasswd

# Write registry config
cat > /etc/registry/config.yml <<CONF
version: 0.1
log:
  fields:
    service: registry
storage:
  filesystem:
    rootdirectory: /var/lib/registry
  delete:
    enabled: true
http:
  addr: :5000
  headers:
    X-Content-Type-Options: [nosniff]
auth:
  htpasswd:
    realm: Registry
    path: /etc/registry/htpasswd
CONF

exec /bin/registry serve /etc/registry/config.yml
`

	desired := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: reg.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    &replicas,
			ServiceName: name,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:    "registry",
							Image:   image,
							Command: []string{"/bin/sh", "-c", entrypoint},
							Ports: []corev1.ContainerPort{
								{Name: "http", ContainerPort: 5000, Protocol: corev1.ProtocolTCP},
							},
							Env: []corev1.EnvVar{
								{
									Name: "REG_USERNAME",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: reg.Spec.CredentialsSecretRef,
											Key:                  "username",
										},
									},
								},
								{
									Name: "REG_PASSWORD",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: reg.Spec.CredentialsSecretRef,
											Key:                  "password",
										},
									},
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "data", MountPath: "/var/lib/registry"},
								{Name: "config", MountPath: "/etc/registry"},
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{Path: "/v2/", Port: intstr.FromInt32(5000)},
								},
								InitialDelaySeconds: 5,
								PeriodSeconds:       10,
							},
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{Path: "/v2/", Port: intstr.FromInt32(5000)},
								},
								InitialDelaySeconds: 10,
								PeriodSeconds:       20,
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name:         "config",
							VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
						},
					},
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "data"},
					Spec: corev1.PersistentVolumeClaimSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: reg.Spec.Storage.Size},
						},
						StorageClassName: reg.Spec.Storage.StorageClassName,
					},
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(reg, desired, r.Scheme); err != nil {
		return fmt.Errorf("set owner reference: %w", err)
	}

	var existing appsv1.StatefulSet
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: reg.Namespace}, &existing); err != nil {
		if !errors.IsNotFound(err) {
			return err
		}
		return r.Create(ctx, desired)
	}

	existing.Spec.Template = desired.Spec.Template
	existing.Spec.Replicas = desired.Spec.Replicas
	return r.Update(ctx, &existing)
}

func (r *RegistryReconciler) reconcileService(ctx context.Context, reg *bootstrapv1alpha1.Registry) error {
	name := reg.Name
	labels := map[string]string{
		"app.kubernetes.io/name":       "registry",
		"app.kubernetes.io/instance":   name,
		"app.kubernetes.io/managed-by": "bootstrap-operator",
	}

	desired := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: reg.Namespace},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports: []corev1.ServicePort{
				{Name: "http", Port: 80, TargetPort: intstr.FromInt32(5000), Protocol: corev1.ProtocolTCP},
			},
			Type: corev1.ServiceTypeClusterIP,
		},
	}

	if err := controllerutil.SetControllerReference(reg, desired, r.Scheme); err != nil {
		return fmt.Errorf("set owner reference: %w", err)
	}

	var existing corev1.Service
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: reg.Namespace}, &existing); err != nil {
		if !errors.IsNotFound(err) {
			return err
		}
		return r.Create(ctx, desired)
	}

	existing.Spec.Selector = desired.Spec.Selector
	existing.Spec.Ports = desired.Spec.Ports
	return r.Update(ctx, &existing)
}

func (r *RegistryReconciler) setStatus(ctx context.Context, reg *bootstrapv1alpha1.Registry, ready bool, url, message string) (ctrl.Result, error) {
	reg.Status.Ready = ready
	reg.Status.URL = url
	reg.Status.Message = message

	condStatus := metav1.ConditionFalse
	reason := "NotReady"
	if ready {
		condStatus = metav1.ConditionTrue
		reason = "Ready"
	}

	meta.SetStatusCondition(&reg.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: condStatus, Reason: reason, Message: message, LastTransitionTime: metav1.Now(),
	})

	if err := r.Status().Update(ctx, reg); err != nil {
		return ctrl.Result{}, err
	}
	if !ready {
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	return ctrl.Result{}, nil
}

func (r *RegistryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&bootstrapv1alpha1.Registry{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Named("registry").
		Complete(r)
}
