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

const repoFinalizer = "bootstrap.cia.net/cleanup"

type RepoReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=bootstrap.cia.net,resources=repos,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=bootstrap.cia.net,resources=repos/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=bootstrap.cia.net,resources=repos/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete

func (r *RepoReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var repo bootstrapv1alpha1.Repo
	if err := r.Get(ctx, req.NamespacedName, &repo); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !repo.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&repo, repoFinalizer) {
			controllerutil.RemoveFinalizer(&repo, repoFinalizer)
			if err := r.Update(ctx, &repo); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer
	if !controllerutil.ContainsFinalizer(&repo, repoFinalizer) {
		controllerutil.AddFinalizer(&repo, repoFinalizer)
		if err := r.Update(ctx, &repo); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Validate credentials Secret exists
	var credSecret corev1.Secret
	credKey := types.NamespacedName{Name: repo.Spec.CredentialsSecretRef.Name, Namespace: repo.Namespace}
	if err := r.Get(ctx, credKey, &credSecret); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("credentials secret not found, waiting", "secret", credKey.Name)
			return r.setStatus(ctx, &repo, false, "", fmt.Sprintf("credentials secret %q not found", credKey.Name))
		}
		return ctrl.Result{}, err
	}
	if len(credSecret.Data["username"]) == 0 || len(credSecret.Data["password"]) == 0 {
		return r.setStatus(ctx, &repo, false, "", fmt.Sprintf("credentials secret %q missing 'username' or 'password' key", credKey.Name))
	}

	// Reconcile StatefulSet
	if err := r.reconcileStatefulSet(ctx, &repo); err != nil {
		return r.setStatus(ctx, &repo, false, "", fmt.Sprintf("reconcile statefulset: %v", err))
	}

	// Reconcile Service
	if err := r.reconcileService(ctx, &repo); err != nil {
		return r.setStatus(ctx, &repo, false, "", fmt.Sprintf("reconcile service: %v", err))
	}

	url := fmt.Sprintf("http://%s.%s.svc.cluster.local/%s.git", repo.Name, repo.Namespace, repo.Name)
	return r.setStatus(ctx, &repo, true, url, "OK")
}

func (r *RepoReconciler) reconcileStatefulSet(ctx context.Context, repo *bootstrapv1alpha1.Repo) error {
	name := repo.Name
	labels := map[string]string{
		"app.kubernetes.io/name":       "git-repo",
		"app.kubernetes.io/instance":   name,
		"app.kubernetes.io/managed-by": "bootstrap-operator",
	}

	image := repo.Spec.Image
	if image == "" {
		image = "alpine:3.21"
	}

	replicas := int32(1)

	entrypoint := `#!/bin/sh
set -e

apk add --no-cache git lighttpd lighttpd-mod_auth git-daemon fcgiwrap spawn-fcgi apache2-utils

REPO_PATH="/data/${REPO_NAME}.git"
if [ ! -d "$REPO_PATH" ]; then
  git init --bare "$REPO_PATH"
  git -C "$REPO_PATH" config http.receivepack true
fi

git -C "$REPO_PATH" config http.receivepack true

htpasswd -cb /etc/lighttpd/htpasswd "$GIT_USERNAME" "$GIT_PASSWORD"

spawn-fcgi -s /var/run/fcgiwrap.sock -u lighttpd -g lighttpd -- /usr/bin/fcgiwrap

cat > /etc/lighttpd/lighttpd.conf <<CONF
server.document-root = "/data"
server.port = 8080
server.modules = (
  "mod_alias",
  "mod_cgi",
  "mod_auth",
  "mod_setenv",
  "mod_fastcgi"
)

auth.backend = "htpasswd"
auth.backend.htpasswd.userfile = "/etc/lighttpd/htpasswd"
auth.require = ( "/" => (
  "method" => "basic",
  "realm" => "Git",
  "require" => "valid-user"
))

alias.url = ( "/" => "/usr/libexec/git-core/git-http-backend/" )

fastcgi.server = ( ".git/" =>
  (( "socket" => "/var/run/fcgiwrap.sock",
     "check-local" => "disable"
  ))
)

setenv.add-environment = (
  "GIT_PROJECT_ROOT" => "/data",
  "GIT_HTTP_EXPORT_ALL" => ""
)
CONF

exec lighttpd -D -f /etc/lighttpd/lighttpd.conf
`

	desired := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: repo.Namespace,
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
							Name:    "git",
							Image:   image,
							Command: []string{"/bin/sh", "-c", entrypoint},
							Ports: []corev1.ContainerPort{
								{Name: "http", ContainerPort: 8080, Protocol: corev1.ProtocolTCP},
							},
							Env: []corev1.EnvVar{
								{Name: "REPO_NAME", Value: name},
								{
									Name: "GIT_USERNAME",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: repo.Spec.CredentialsSecretRef,
											Key:                  "username",
										},
									},
								},
								{
									Name: "GIT_PASSWORD",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: repo.Spec.CredentialsSecretRef,
											Key:                  "password",
										},
									},
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "data", MountPath: "/data"},
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(8080)},
								},
								InitialDelaySeconds: 10,
								PeriodSeconds:       10,
							},
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(8080)},
								},
								InitialDelaySeconds: 15,
								PeriodSeconds:       20,
							},
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
							Requests: corev1.ResourceList{corev1.ResourceStorage: repo.Spec.Storage.Size},
						},
						StorageClassName: repo.Spec.Storage.StorageClassName,
					},
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(repo, desired, r.Scheme); err != nil {
		return fmt.Errorf("set owner reference: %w", err)
	}

	var existing appsv1.StatefulSet
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: repo.Namespace}, &existing); err != nil {
		if !errors.IsNotFound(err) {
			return err
		}
		return r.Create(ctx, desired)
	}

	existing.Spec.Template = desired.Spec.Template
	existing.Spec.Replicas = desired.Spec.Replicas
	return r.Update(ctx, &existing)
}

func (r *RepoReconciler) reconcileService(ctx context.Context, repo *bootstrapv1alpha1.Repo) error {
	name := repo.Name
	labels := map[string]string{
		"app.kubernetes.io/name":       "git-repo",
		"app.kubernetes.io/instance":   name,
		"app.kubernetes.io/managed-by": "bootstrap-operator",
	}

	desired := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: repo.Namespace},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports: []corev1.ServicePort{
				{Name: "http", Port: 80, TargetPort: intstr.FromInt32(8080), Protocol: corev1.ProtocolTCP},
			},
			Type: corev1.ServiceTypeClusterIP,
		},
	}

	if err := controllerutil.SetControllerReference(repo, desired, r.Scheme); err != nil {
		return fmt.Errorf("set owner reference: %w", err)
	}

	var existing corev1.Service
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: repo.Namespace}, &existing); err != nil {
		if !errors.IsNotFound(err) {
			return err
		}
		return r.Create(ctx, desired)
	}

	existing.Spec.Selector = desired.Spec.Selector
	existing.Spec.Ports = desired.Spec.Ports
	return r.Update(ctx, &existing)
}

func (r *RepoReconciler) setStatus(ctx context.Context, repo *bootstrapv1alpha1.Repo, ready bool, url, message string) (ctrl.Result, error) {
	repo.Status.Ready = ready
	repo.Status.URL = url
	repo.Status.Message = message

	condStatus := metav1.ConditionFalse
	reason := "NotReady"
	if ready {
		condStatus = metav1.ConditionTrue
		reason = "Ready"
	}

	meta.SetStatusCondition(&repo.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: condStatus, Reason: reason, Message: message, LastTransitionTime: metav1.Now(),
	})

	if err := r.Status().Update(ctx, repo); err != nil {
		return ctrl.Result{}, err
	}
	if !ready {
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	return ctrl.Result{}, nil
}

func (r *RepoReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&bootstrapv1alpha1.Repo{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Named("repo").
		Complete(r)
}
