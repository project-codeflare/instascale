/*
Copyright 2022.

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

package controllers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/project-codeflare/instascale/pkg/config"
	arbv1 "github.com/project-codeflare/multi-cluster-app-dispatcher/pkg/apis/controller/v1beta1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"

	"k8s.io/apimachinery/pkg/labels"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// AppWrapperReconciler reconciles a AppWrapper object
type AppWrapperReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	Config         config.InstaScaleConfiguration
	kubeClient     *kubernetes.Clientset
	ocmClusterID   string
	ocmToken       string
	useMachineSets bool
}

var (
	deletionMessage      string
	maxScaleNodesAllowed int
)

const (
	namespaceToList = "openshift-machine-api"
	minResyncPeriod = 10 * time.Minute
	finalizerName   = "instascale.codeflare.dev/finalizer"
)

// +kubebuilder:rbac:groups=workload.codeflare.dev,resources=appwrappers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=workload.codeflare.dev,resources=appwrappers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=workload.codeflare.dev,resources=appwrappers/finalizers,verbs=update

// +kubebuilder:rbac:groups=apps,resources=machineset,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=machineset/status,verbs=get

// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=list;watch;get
// +kubebuilder:rbac:groups=machine.openshift.io,resources=*,verbs=list;watch;get;create;update;delete;patch
// +kubebuilder:rbac:groups=config.openshift.io,resources=clusterversions,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the AppWrapper object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.11.0/pkg/reconcile
func (r *AppWrapperReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)
	// todo: Move the getOCMClusterID call out of reconcile loop.
	// Only reason we are calling it here is that the client is not able to make
	// calls until it is started, so SetupWithManager is not working.
	if !r.useMachineSets && r.ocmClusterID == "" {
		if err := r.getOCMClusterID(ctx); err != nil {
			return ctrl.Result{Requeue: true, RequeueAfter: timeFiveSeconds}, err
		}
	}
	var appwrapper arbv1.AppWrapper

	if err := r.Get(ctx, req.NamespacedName, &appwrapper); err != nil {
		if apierrors.IsNotFound(err) {
			// ignore not-found errors, since we can get them on delete requests.
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return ctrl.Result{}, err
	}

	// Adds finalizer to the appwrapper if it doesn't exist
	if !controllerutil.ContainsFinalizer(&appwrapper, finalizerName) {
		controllerutil.AddFinalizer(&appwrapper, finalizerName)
		if err := r.Update(ctx, &appwrapper); err != nil {
			return ctrl.Result{RequeueAfter: timeFiveSeconds}, nil
		}
		return ctrl.Result{}, nil
	}

	if !appwrapper.ObjectMeta.DeletionTimestamp.IsZero() || appwrapper.Status.State == arbv1.AppWrapperStateCompleted {
		if err := r.finalizeScalingDownMachines(ctx, &appwrapper); err != nil {
			return ctrl.Result{}, err
		}
		controllerutil.RemoveFinalizer(&appwrapper, finalizerName)
		if err := r.Update(ctx, &appwrapper); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	demandPerInstanceType := r.discoverInstanceTypes(ctx, &appwrapper)
	if ocmSecretRef := r.Config.OCMSecretRef; ocmSecretRef != nil {
		return r.scaleMachinePool(ctx, &appwrapper, demandPerInstanceType)
	} else {
		switch strings.ToLower(r.Config.MachineSetsStrategy) {
		case "reuse":
			return r.reconcileReuseMachineSet(ctx, &appwrapper, demandPerInstanceType)
		case "duplicate":
			return r.reconcileCreateMachineSet(ctx, &appwrapper, demandPerInstanceType)
		}
	}
	return ctrl.Result{}, nil
}

func (r *AppWrapperReconciler) finalizeScalingDownMachines(ctx context.Context, appwrapper *arbv1.AppWrapper) error {
	logger := ctrl.LoggerFrom(ctx)
	if appwrapper.Status.State == arbv1.AppWrapperStateCompleted {
		deletionMessage = "completed"
	} else {
		deletionMessage = "deleted"
	}
	if r.useMachineSets {
		switch strings.ToLower(r.Config.MachineSetsStrategy) {
		case "reuse":
			matchedAw := r.findExactMatch(ctx, appwrapper)
			if matchedAw != nil {
				logger.Info(
					"AppWrapper deleted transferring machines",
					"oldAppWrapper", appwrapper,
					"deletionMessage", deletionMessage,
					"newAppWrapper", matchedAw,
				)
				if err := r.swapNodeLabels(ctx, appwrapper, matchedAw); err != nil {
					return err
				}
			} else {
				logger.Info(
					"Scaling down machines associated with deleted AppWrapper",
					"appWrapper", appwrapper,
					"deletionMessage", deletionMessage,
				)
				if err := r.annotateToDeleteMachine(ctx, appwrapper); err != nil {
					return err
				}
			}
		case "duplicate":
			logger.Info(
				"AppWrapper deleted, scaling down machineset",
				"appWrapper", appwrapper,
				"deletionMessage", deletionMessage,
			)
			if err := r.deleteMachineSet(ctx, appwrapper); err != nil {
				return err
			}
		}
	} else {
		logger.Info(
			"AppWrapper deleted, scaling down machine pool",
			"appWrapper", appwrapper,
			"deletionMessage", deletionMessage,
		)
		if _, err := r.deleteMachinePool(ctx, appwrapper); err != nil {
			return err
		}
	}
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *AppWrapperReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {

	logger := ctrl.LoggerFrom(ctx)
	restConfig := mgr.GetConfig()

	var err error
	r.kubeClient, err = kubernetes.NewForConfig(restConfig)
	if err != nil {
		return err
	}

	maxScaleNodesAllowed = int(r.Config.MaxScaleoutAllowed)
	r.useMachineSets = true
	if ocmSecretRef := r.Config.OCMSecretRef; ocmSecretRef != nil {
		r.useMachineSets = false
		if ocmSecret, err := r.getOCMSecret(ctx, ocmSecretRef); err != nil {
			return fmt.Errorf("error reading OCM Secret from ref %q: %w", ocmSecretRef, err)
		} else if token := ocmSecret.Data["token"]; len(token) > 0 {
			r.ocmToken = string(token)
		} else {
			return fmt.Errorf("token is missing from OCM Secret %q", ocmSecretRef)
		}
		if ok, err := r.machinePoolExists(); err != nil {
			return err
		} else if ok {
			logger.Info("Using machine pools for cluster auto-scaling")
		}
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&arbv1.AppWrapper{}).Named("instascale").
		Complete(r)
}

func (r *AppWrapperReconciler) getOCMSecret(ctx context.Context, secretRef *corev1.SecretReference) (*corev1.Secret, error) {
	return r.kubeClient.CoreV1().Secrets(secretRef.Namespace).Get(ctx, secretRef.Name, metav1.GetOptions{})
}

func (r *AppWrapperReconciler) discoverInstanceTypes(ctx context.Context, aw *arbv1.AppWrapper) map[string]int {
	logger := ctrl.LoggerFrom(ctx)
	demandMapPerInstanceType := make(map[string]int)
	var instanceRequired []string
	for k, v := range aw.Labels {
		if k == "orderedinstance" {
			instanceRequired = strings.Split(v, "_")
		}
	}

	if len(instanceRequired) < 1 {
		logger.Info(
			"AppWrapper cannot be scaled out due to missing orderedinstance label",
			"appWrapper", aw,
		)
		return demandMapPerInstanceType
	}

	for id, genericItem := range aw.Spec.AggrResources.GenericItems {
		for idx, val := range genericItem.CustomPodResources {
			combinedIndex := idx + id
			if combinedIndex < len(instanceRequired) {
				instanceName := instanceRequired[combinedIndex]
				demandMapPerInstanceType[instanceName] = val.Replicas
			}
		}
	}
	return demandMapPerInstanceType
}

func (r *AppWrapperReconciler) findExactMatch(ctx context.Context, aw *arbv1.AppWrapper) *arbv1.AppWrapper {
	logger := ctrl.LoggerFrom(ctx)
	var match *arbv1.AppWrapper = nil
	appwrappers := arbv1.AppWrapperList{}

	labelSelector := labels.SelectorFromSet(labels.Set(map[string]string{
		"orderedinstance": "",
	}))

	listOptions := &client.ListOptions{
		LabelSelector: labelSelector,
	}

	err := r.List(ctx, &appwrappers, listOptions)
	if err != nil {
		logger.Error(err, "Cannot list queued appwrappers, associated machines will be deleted")
		return match
	}
	var existingAcquiredMachineTypes = ""

	for key, value := range aw.Labels {
		if key == "orderedinstance" {
			existingAcquiredMachineTypes = value
		}
	}

	for _, eachAw := range appwrappers.Items {
		if eachAw.Status.State != arbv1.AppWrapperStateEnqueued {
			if eachAw.Labels["orderedinstance"] == existingAcquiredMachineTypes {
				match = &eachAw
				logger.Info(
					"AppWrapper has successfully acquired requested machine types",
					"appWrapper", eachAw,
					"acquiredMachineTypes", existingAcquiredMachineTypes,
				)
				break
			}
		}
	}
	return match

}
