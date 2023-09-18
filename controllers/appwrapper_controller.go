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
	"k8s.io/klog"

	"k8s.io/apimachinery/pkg/labels"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// AppWrapperReconciler reconciles a AppWrapper object
type AppWrapperReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	Config     config.InstaScaleConfiguration
	kubeClient *kubernetes.Clientset
}

var (
	reuse                = true
	ocmClusterID         string
	ocmToken             string
	useMachineSets       bool
	maxScaleNodesAllowed int
)

const (
	namespaceToList   = "openshift-machine-api"
	minResyncPeriod   = 10 * time.Minute
	pullSecretKey     = ".dockerconfigjson"
	pullSecretAuthKey = "cloud.openshift.com"
	finalizerName     = "instascale.codeflare.dev/finalizer"
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
	if !useMachineSets && ocmClusterID == "" {
		r.getOCMClusterID(ctx)
	}
	var appwrapper arbv1.AppWrapper

	if err := r.Get(ctx, req.NamespacedName, &appwrapper); err != nil {
		if apierrors.IsNotFound(err) {
			// ignore not-found errors, since we can get them on delete requests.
			return ctrl.Result{}, nil
		}
		klog.Error(err, "unable to fetch appwrapper")
	}

	// Adds finalizer to the appwrapper if it doesn't exist
	if !controllerutil.ContainsFinalizer(&appwrapper, finalizerName) {
		controllerutil.AddFinalizer(&appwrapper, finalizerName)
		if err := r.Update(ctx, &appwrapper); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	if !appwrapper.ObjectMeta.DeletionTimestamp.IsZero() {
		if err := r.finalizeScalingDownMachines(ctx, &appwrapper); err != nil {
			return ctrl.Result{}, err
		}
		controllerutil.RemoveFinalizer(&appwrapper, finalizerName)
		if err := r.Update(ctx, &appwrapper); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	demandPerInstanceType := r.discoverInstanceTypes(&appwrapper)
	//for userRequestedInstanceType := range demandPerInstanceType {
	if useMachineSets {
		if reuse {
			res, err := r.reconcileReuseMachineSet(ctx, &appwrapper, demandPerInstanceType)
			if err != nil {
				klog.Infof("Error reconciling MachineSet: %s", err)
			}
			return res, err
		} else {
			res, err := r.reconcileCreateMachineSet(ctx, &appwrapper, demandPerInstanceType)
			if err != nil {
				klog.Infof("Error reconciling MachineSet: %s", err)
			}
			return res, err
		}
	} else {
		res, err := r.scaleMachinePool(ctx, &appwrapper, demandPerInstanceType)
		if err != nil {
			klog.Infof("Error reconciling MachinePool: %s", err)
		}
		return res, err
	}
}

func (r *AppWrapperReconciler) finalizeScalingDownMachines(ctx context.Context, appwrapper *arbv1.AppWrapper) error {
	if useMachineSets {
		if reuse {
			matchedAw := r.findExactMatch(ctx, appwrapper)
			if matchedAw != nil {
				klog.Infof("Appwrapper %s deleted, swapping machines to %s", appwrapper.Name, matchedAw.Name)
				r.swapNodeLabels(ctx, appwrapper, matchedAw)
			} else {
				klog.Infof("Appwrapper %s deleted, scaling down machines", appwrapper.Name)

				r.annotateToDeleteMachine(ctx, appwrapper)
			}
		} else {
			klog.Infof("Appwrapper deleted scale-down machineset: %s ", appwrapper.Name)
			r.deleteMachineSet(ctx, appwrapper)
		}
	} else {
		r.deleteMachinePool(ctx, appwrapper)
	}
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *AppWrapperReconciler) SetupWithManager(mgr ctrl.Manager) error {

	restConfig := mgr.GetConfig()

	var err error
	r.kubeClient, err = kubernetes.NewForConfig(restConfig)
	if err != nil {
		return err
	}

	maxScaleNodesAllowed = int(r.Config.MaxScaleoutAllowed)

	useMachineSets = true
	if ocmSecretRef := r.Config.OCMSecretRef; ocmSecretRef != nil {
		if ocmSecret, err := r.getOCMSecret(ocmSecretRef); err != nil {
			return fmt.Errorf("error reading OCM Secret from ref %q: %w", ocmSecretRef, err)
		} else if token := ocmSecret.Data["token"]; len(token) > 0 {
			ocmToken = string(token)
		} else {
			return fmt.Errorf("token is missing from OCM Secret %q", ocmSecretRef)
		}
		if ok, err := machinePoolExists(); err != nil {
			return err
		} else if ok {
			useMachineSets = false
			klog.Info("Using machine pools for cluster auto-scaling")
		}
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&arbv1.AppWrapper{}).
		Complete(r)
}

func (r *AppWrapperReconciler) getOCMSecret(secretRef *corev1.SecretReference) (*corev1.Secret, error) {
	return r.kubeClient.CoreV1().Secrets(secretRef.Namespace).Get(context.Background(), secretRef.Name, metav1.GetOptions{})
}

func (r *AppWrapperReconciler) ocmSecretExists(namespace string) bool {
	instascaleOCMSecret, err := r.kubeClient.CoreV1().Secrets(namespace).Get(context.Background(), "instascale-ocm-secret", metav1.GetOptions{})
	if err != nil {
		klog.Errorf("Error getting instascale-ocm-secret from namespace %v: %v", namespace, err)
		klog.Infof("If you are looking to use OCM, ensure that the 'instascale-ocm-secret' secret is available on the cluster within namespace %v", namespace)
		klog.Infof("Setting useMachineSets to %v.", useMachineSets)
		return false
	}

	ocmToken = string(instascaleOCMSecret.Data["token"])
	return true
}

func (r *AppWrapperReconciler) discoverInstanceTypes(aw *arbv1.AppWrapper) map[string]int {
	demandMapPerInstanceType := make(map[string]int)
	var instanceRequired []string
	for k, v := range aw.Labels {
		if k == "orderedinstance" {
			instanceRequired = strings.Split(v, "_")
		}
	}

	if len(instanceRequired) < 1 {
		klog.Infof("Found AW %s that cannot be scaled due to missing orderedinstance label", aw.ObjectMeta.Name)
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

func canScaleMachinepool(demandPerInstanceType map[string]int) bool {
	return true
}

func (r *AppWrapperReconciler) findExactMatch(ctx context.Context, aw *arbv1.AppWrapper) *arbv1.AppWrapper {
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
		klog.Error("Cannot list queued appwrappers, associated machines will be deleted")
		return match
	}
	var existingAcquiredMachineTypes = ""

	for _, eachAw := range appwrappers.Items {
		if eachAw.Status.State != "Pending" {
			continue
		}
		match = &eachAw
		klog.Infof("Found exact match, %v appwrapper has acquired machinetypes %v", eachAw.Name, existingAcquiredMachineTypes)
	}
	return match

}

func (r *AppWrapperReconciler) scaleDown(ctx context.Context, aw *arbv1.AppWrapper) {
	if reuse {
		r.annotateToDeleteMachine(ctx, aw)
	} else {
		r.deleteMachineSet(ctx, aw)
	}
}
