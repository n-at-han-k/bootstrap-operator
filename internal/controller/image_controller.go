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

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	bootstrapv1alpha1 "git.cia.net/n-at-han-k/bootstrap-operator/api/v1alpha1"
)

const imageFinalizer = "bootstrap.cia.net/image-cleanup"

type ImageReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=bootstrap.cia.net,resources=images,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=bootstrap.cia.net,resources=images/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=bootstrap.cia.net,resources=images/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete

func (r *ImageReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var img bootstrapv1alpha1.Image
	if err := r.Get(ctx, req.NamespacedName, &img); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !img.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&img, imageFinalizer) {
			controllerutil.RemoveFinalizer(&img, imageFinalizer)
			if err := r.Update(ctx, &img); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer
	if !controllerutil.ContainsFinalizer(&img, imageFinalizer) {
		controllerutil.AddFinalizer(&img, imageFinalizer)
		if err := r.Update(ctx, &img); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Check Repo is Ready
	var repo bootstrapv1alpha1.Repo
	repoKey := types.NamespacedName{Name: img.Spec.RepoRef.Name, Namespace: img.Namespace}
	if err := r.Get(ctx, repoKey, &repo); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("repo not found, waiting", "repo", repoKey.Name)
			return r.setStatus(ctx, &img, false, "", fmt.Sprintf("repo %q not found", repoKey.Name))
		}
		return ctrl.Result{}, err
	}
	if !repo.Status.Ready {
		return r.setStatus(ctx, &img, false, "", fmt.Sprintf("waiting for repo %q to be ready", repoKey.Name))
	}

	// Check Registry is Ready
	var reg bootstrapv1alpha1.Registry
	regKey := types.NamespacedName{Name: img.Spec.RegistryRef.Name, Namespace: img.Namespace}
	if err := r.Get(ctx, regKey, &reg); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("registry not found, waiting", "registry", regKey.Name)
			return r.setStatus(ctx, &img, false, "", fmt.Sprintf("registry %q not found", regKey.Name))
		}
		return ctrl.Result{}, err
	}
	if !reg.Status.Ready {
		return r.setStatus(ctx, &img, false, "", fmt.Sprintf("waiting for registry %q to be ready", regKey.Name))
	}

	// Check if build Job already exists
	jobName := fmt.Sprintf("image-build-%s", img.Name)
	var job batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Name: jobName, Namespace: img.Namespace}, &job); err != nil {
		if !errors.IsNotFound(err) {
			return ctrl.Result{}, err
		}

		// Create the build Job
		if err := r.createBuildJob(ctx, &img, &repo, &reg, jobName); err != nil {
			return r.setStatus(ctx, &img, false, "", fmt.Sprintf("create build job: %v", err))
		}

		return r.setStatusWithJob(ctx, &img, false, jobName, "build job created")
	}

	// Job exists — check its status
	if job.Status.Succeeded > 0 {
		return r.setStatusWithJob(ctx, &img, true, jobName, "build succeeded")
	}
	if job.Status.Failed > 0 {
		msg := "build job failed"
		if len(job.Status.Conditions) > 0 {
			msg = job.Status.Conditions[0].Message
		}
		return r.setStatusWithJob(ctx, &img, false, jobName, msg)
	}

	// Still running
	return r.setStatusWithJob(ctx, &img, false, jobName, "build job running")
}

func (r *ImageReconciler) createBuildJob(ctx context.Context, img *bootstrapv1alpha1.Image, repo *bootstrapv1alpha1.Repo, reg *bootstrapv1alpha1.Registry, jobName string) error {
	branch := img.Spec.Branch
	if branch == "" {
		branch = "main"
	}

	// The clone URL includes credentials via env vars interpolated in the shell script.
	// repo.Status.URL is like http://<svc>.<ns>.svc.cluster.local/<name>.git
	// reg.Status.URL is like http://<svc>.<ns>.svc.cluster.local
	destination := fmt.Sprintf("%s/%s", reg.Status.URL, img.Spec.Destination)

	buildScript := fmt.Sprintf(`#!/bin/sh
set -e

# Enable flakes
mkdir -p /root/.config/nix
echo "experimental-features = nix-command flakes" > /root/.config/nix/nix.conf

# Build the image
cd /workspace
nix build .#%s --no-link --print-out-paths > /tmp/result-path
RESULT=$(cat /tmp/result-path)

# Push to registry
skopeo copy --dest-tls-verify=false \
  --dest-creds "$REG_USERNAME:$REG_PASSWORD" \
  "docker-archive:$RESULT" \
  "docker://%s"
`, img.Spec.FlakeOutput, destination)

	backoffLimit := int32(3)
	ttl := int32(600)

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: img.Namespace,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyOnFailure,
					InitContainers: []corev1.Container{
						{
							Name:    "clone",
							Image:   "alpine/git:latest",
							Command: []string{"/bin/sh", "-c", fmt.Sprintf(`git clone --depth 1 --branch %s http://$GIT_USERNAME:$GIT_PASSWORD@%s.%s.svc.cluster.local/%s.git /workspace`, branch, repo.Name, repo.Namespace, repo.Name)},
							Env: []corev1.EnvVar{
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
								{Name: "workspace", MountPath: "/workspace"},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:    "build-push",
							Image:   "nixos/nix:latest",
							Command: []string{"/bin/sh", "-c", buildScript},
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
								{Name: "workspace", MountPath: "/workspace"},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("500m"),
									corev1.ResourceMemory: resource.MustParse("2Gi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceMemory: resource.MustParse("4Gi"),
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "workspace",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{
									SizeLimit: resourcePtr(resource.MustParse("5Gi")),
								},
							},
						},
					},
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(img, job, r.Scheme); err != nil {
		return fmt.Errorf("set owner reference: %w", err)
	}

	return r.Create(ctx, job)
}

func resourcePtr(q resource.Quantity) *resource.Quantity {
	return &q
}

func (r *ImageReconciler) setStatus(ctx context.Context, img *bootstrapv1alpha1.Image, built bool, jobName, message string) (ctrl.Result, error) {
	return r.setStatusWithJob(ctx, img, built, jobName, message)
}

func (r *ImageReconciler) setStatusWithJob(ctx context.Context, img *bootstrapv1alpha1.Image, built bool, jobName, message string) (ctrl.Result, error) {
	img.Status.Built = built
	img.Status.JobName = jobName
	img.Status.Message = message

	condStatus := metav1.ConditionFalse
	reason := "NotBuilt"
	if built {
		condStatus = metav1.ConditionTrue
		reason = "Built"
	}

	meta.SetStatusCondition(&img.Status.Conditions, metav1.Condition{
		Type: "Built", Status: condStatus, Reason: reason, Message: message, LastTransitionTime: metav1.Now(),
	})

	if err := r.Status().Update(ctx, img); err != nil {
		return ctrl.Result{}, err
	}
	if !built {
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	return ctrl.Result{}, nil
}

func (r *ImageReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&bootstrapv1alpha1.Image{}).
		Owns(&batchv1.Job{}).
		Named("image").
		Complete(r)
}
