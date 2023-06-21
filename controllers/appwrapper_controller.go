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
	"os"
	"strconv"
	"strings"
	"time"

	mapiclientset "github.com/openshift/client-go/machine/clientset/versioned"
	machineinformersv1beta1 "github.com/openshift/client-go/machine/informers/externalversions"
	"github.com/openshift/client-go/machine/listers/machine/v1beta1"
	arbv1 "github.com/project-codeflare/multi-cluster-app-dispatcher/pkg/apis/controller/v1beta1"
	"github.com/project-codeflare/multi-cluster-app-dispatcher/pkg/client/clientset/controller-versioned/clients"
	arbinformers "github.com/project-codeflare/multi-cluster-app-dispatcher/pkg/client/informers/controller-externalversion"
	v1 "github.com/project-codeflare/multi-cluster-app-dispatcher/pkg/client/listers/controller/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// AppWrapperReconciler reconciles a AppWrapper object
type AppWrapperReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	ConfigsNamespace string
}

var (
	scaledAppwrapper []string
	reuse            = true
	ocmClusterID     string
	ocmToken         string
	useMachineSets   bool

	maxScaleNodesAllowed int
	msLister             v1beta1.MachineSetLister
	machineLister        v1beta1.MachineLister
	msInformerHasSynced  bool
	machineClient        mapiclientset.Interface
	queueJobLister       v1.AppWrapperLister
	kubeClient           *kubernetes.Clientset
)

const (
	namespaceToList   = "openshift-machine-api"
	minResyncPeriod   = 10 * time.Minute
	pullSecretKey     = ".dockerconfigjson"
	pullSecretAuthKey = "cloud.openshift.com"
)

//+kubebuilder:rbac:groups=instascale.ibm.com.instascale.ibm.com,resources=appwrappers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=instascale.ibm.com.instascale.ibm.com,resources=appwrappers/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=instascale.ibm.com.instascale.ibm.com,resources=appwrappers/finalizers,verbs=update

//+kubebuilder:rbac:groups=apps,resources=machineset,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=apps,resources=machineset/status,verbs=get

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

	var appwrapper arbv1.AppWrapper
	if err := r.Get(ctx, req.NamespacedName, &appwrapper); err != nil {
		if apierrors.IsNotFound(err) {
			//ignore not-found errors, since we can get them on delete requests.
			return ctrl.Result{}, nil
		}
		klog.Error(err, "unable to fetch appwrapper")
	}

	factory := machineinformersv1beta1.NewSharedInformerFactoryWithOptions(machineClient, resyncPeriod()(), machineinformersv1beta1.WithNamespace(""))
	informer := factory.Machine().V1beta1().MachineSets().Informer()
	msLister = factory.Machine().V1beta1().MachineSets().Lister()
	machineLister = factory.Machine().V1beta1().Machines().Lister()

	// todo: Move the getOCMClusterID call out of reconcile loop.
	// Only reason we are calling it here is that the client is not able to make
	// calls until it is started, so SetupWithManager is not working.
	if !useMachineSets && ocmClusterID == "" {
		getOCMClusterID(r)
	}

	stopper := make(chan struct{})
	defer close(stopper)
	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    onAdd,
		UpdateFunc: onUpdate,
		DeleteFunc: onDelete,
	})
	go informer.Run(stopper)
	if !cache.WaitForCacheSync(stopper, informer.HasSynced) {
		klog.Info("Wait for cache to sync")
	}
	//TODO: do we need dual sync??
	msInformerHasSynced = informer.HasSynced()
	addAppwrappersThatNeedScaling()
	<-stopper
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *AppWrapperReconciler) SetupWithManager(mgr ctrl.Manager) error {

	kubeconfig := os.Getenv("KUBECONFIG")
	cb, err := NewClientBuilder(kubeconfig)
	if err != nil {
		klog.Fatalf("Error creating clients: %v", err)
	}
	restConfig, err := getRestConfig(kubeconfig)
	if err != nil {
		klog.Info("Failed to get rest config")
	}

	machineClient = cb.MachineClientOrDie("machine-shared-informer")
	kubeClient, _ = kubernetes.NewForConfig(restConfig)
	instascaleConfigmap, err := kubeClient.CoreV1().ConfigMaps(r.ConfigsNamespace).Get(context.Background(), "instascale-config", metav1.GetOptions{})
	if err != nil {
		klog.Infof("Config map named instascale-config is not available in namespace %v", r.ConfigsNamespace)
	}

	if maxScaleNodesAllowed, err = strconv.Atoi(instascaleConfigmap.Data["maxScaleoutAllowed"]); err != nil {
		klog.Infof("Error converting %v to int. Setting maxScaleNodesAllowed to 3", maxScaleNodesAllowed)
		maxScaleNodesAllowed = 3
	}
	klog.Infof("Got config map named %v from namespace %v that configures max nodes in cluster to value %v", instascaleConfigmap.Name, instascaleConfigmap.Namespace, maxScaleNodesAllowed)

	useMachineSets = true
	useMachinePools, err := strconv.ParseBool(instascaleConfigmap.Data["useMachinePools"])
	if err != nil {
		klog.Infof("Error converting %v to bool. Defaulting to using Machine Sets", useMachineSets)
	} else {
		useMachineSets = !useMachinePools
		klog.Infof("Setting useMachineSets to %v", useMachineSets)
	}

	if !useMachineSets {
		instascaleOCMSecret, err := kubeClient.CoreV1().Secrets(r.ConfigsNamespace).Get(context.Background(), "instascale-ocm-secret", metav1.GetOptions{})
		if err != nil {
			klog.Errorf("Error getting instascale-ocm-secret from namespace %v - Error :  %v", r.ConfigsNamespace, err)
		}
		ocmToken = string(instascaleOCMSecret.Data["token"])
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&arbv1.AppWrapper{}).
		Complete(r)
}

func addAppwrappersThatNeedScaling() {
	kubeconfig := os.Getenv("KUBECONFIG")
	restConfig, err := getRestConfig(kubeconfig)
	if err != nil {
		klog.Fatalf("Error getting config: %v", err)
	}
	awJobClient, _, err := clients.NewClient(restConfig)
	if err != nil {
		klog.Fatalf("Error creating client: %v", err)
	}
	queueJobInformer := arbinformers.NewSharedInformerFactory(awJobClient, 0).AppWrapper().AppWrappers()
	queueJobInformer.Informer().AddEventHandler(
		cache.FilteringResourceEventHandler{
			FilterFunc: func(obj interface{}) bool {
				switch t := obj.(type) {
				case *arbv1.AppWrapper:
					klog.V(10).Infof("[getDispatchedAppWrappers] Filtered name=%s/%s",
						t.Namespace, t.Name)
					return true
				default:
					return false
				}
			},
			Handler: cache.ResourceEventHandlerFuncs{
				AddFunc:    onAdd,
				UpdateFunc: onUpdate,
				DeleteFunc: onDelete,
			},
		})
	queueJobLister = queueJobInformer.Lister()
	queueJobSynced := queueJobInformer.Informer().HasSynced
	stopCh := make(chan struct{})
	defer close(stopCh)
	go queueJobInformer.Informer().Run(stopCh)
	cache.WaitForCacheSync(stopCh, queueJobSynced)
	<-stopCh
}

// onAdd is the function executed when the kubernetes informer notified the
// presence of a new kubernetes node in the cluster
func onAdd(obj interface{}) {
	aw, ok := obj.(*arbv1.AppWrapper)
	if ok {
		klog.Infof("Found Appwrapper named %s that has status %v", aw.Name, aw.Status.State)
		if aw.Status.State == arbv1.AppWrapperStateEnqueued || aw.Status.State == "" {
			//scaledAppwrapper = append(scaledAppwrapper, aw.Name)
			demandPerInstanceType := discoverInstanceTypes(aw)
			//TODO: simplify the looping
			if useMachineSets {
				if canScaleMachineset(demandPerInstanceType) {
					scaleUp(aw, demandPerInstanceType)
				} else {
					klog.Infof("Cannot scale up replicas max replicas allowed is %v", maxScaleNodesAllowed)
				}
			} else {
				if canScaleMachinepool(demandPerInstanceType) {
					scaleUp(aw, demandPerInstanceType)
				} else {
					klog.Infof("Cannot scale up replicas max replicas allowed is %v", maxScaleNodesAllowed)
				}
			}

		}

	}

}

func onUpdate(old, new interface{}) {
	aw, ok := new.(*arbv1.AppWrapper)
	if ok {
		status := aw.Status.State
		if status == "Completed" {
			klog.Info("Job completed, deleting resources owned")
			deleteMachineSet(aw)
		}
		if contains(scaledAppwrapper, aw.Name) {
			return
		}
		pending, aw := IsAwPending()
		if pending {
			demandPerInstanceType := discoverInstanceTypes(aw)
			if canScaleMachineset(demandPerInstanceType) {
				scaleUp(aw, demandPerInstanceType)
			} else {
				klog.Infof("Cannot scale up replicas max replicas allowed is %v", maxScaleNodesAllowed)
			}
		}

	}

}

func discoverInstanceTypes(aw *arbv1.AppWrapper) map[string]int {
	demandMapPerInstanceType := make(map[string]int)
	var instanceRequired []string
	for k, v := range aw.Labels {
		if k == "orderedinstance" {
			instanceRequired = strings.Split(v, "_")
		}
	}
	klog.Infof("The instanceRequired array: %v", instanceRequired)
	for id, genericItem := range aw.Spec.AggrResources.GenericItems {
		for idx, val := range genericItem.CustomPodResources {
			instanceName := instanceRequired[idx+id]
			klog.Infof("Got instance name %v", instanceName)
			demandMapPerInstanceType[instanceName] = val.Replicas

		}
	}
	return demandMapPerInstanceType
}

func canScaleMachinepool(demandPerInstanceType map[string]int) bool {
	return true
}

func scaleUp(aw *arbv1.AppWrapper, demandMapPerInstanceType map[string]int) {
	if msInformerHasSynced {
		//Assumption is made that the cluster has machineset configure that AW needs
		for userRequestedInstanceType := range demandMapPerInstanceType {
			//TODO: get unique machineset
			replicas := demandMapPerInstanceType[userRequestedInstanceType]

			if useMachineSets {
				scaleMachineSet(aw, userRequestedInstanceType, replicas)
			} else {
				scaleMachinePool(aw, userRequestedInstanceType, replicas)
			}
		}
		klog.Infof("Completed Scaling for %v", aw.Name)
		scaledAppwrapper = append(scaledAppwrapper, aw.Name)
	}

}

func IsAwPending() (false bool, aw *arbv1.AppWrapper) {
	queuedJobs, err := queueJobLister.AppWrappers("").List(labels.Everything())
	if err != nil {
		klog.Fatalf("Error listing: %v", err)
	}
	for _, aw := range queuedJobs {
		//skip
		if contains(scaledAppwrapper, aw.Name) {
			continue
		}
		status := aw.Status.State
		allconditions := aw.Status.Conditions
		for _, condition := range allconditions {
			if status == "Pending" && strings.Contains(condition.Message, "Insufficient") {
				klog.Infof("Pending AppWrapper %v needs scaling", aw.Name)
				return true, aw
			}
		}
	}
	return false, nil
}

// add logic to check for matching pending AppWrappers
func findExactMatch(aw *arbv1.AppWrapper) *arbv1.AppWrapper {
	var match *arbv1.AppWrapper = nil
	allAw, err := queueJobLister.List(labels.Everything())
	if err != nil {
		klog.Error("Cannot list queued appwrappers, associated machines will be deleted")
		return match
	}
	var existingAcquiredMachineTypes = ""

	for key, value := range aw.Labels {
		if key == "orderedinstance" {
			existingAcquiredMachineTypes = value
		}
	}

	for _, eachAw := range allAw {
		if eachAw.Status.State != "Pending" {
			continue
		}
		for k, v := range eachAw.Labels {
			if k == "orderedinstance" {
				if v == existingAcquiredMachineTypes {
					match = eachAw
					klog.Infof("Found exact match, %v appwrapper has acquire machinetypes %v", eachAw.Name, existingAcquiredMachineTypes)
				}
			}
		}
	}
	return match

}

func onDelete(obj interface{}) {
	aw, ok := obj.(*arbv1.AppWrapper)
	if ok {
		if useMachineSets {
			if reuse {
				matchedAw := findExactMatch(aw)
				if matchedAw != nil {
					klog.Infof("Appwrapper %s deleted, swapping machines to %s", aw.Name, matchedAw.Name)
					swapNodeLabels(aw, matchedAw)
				} else {
					klog.Infof("Appwrapper %s deleted, scaling down machines", aw.Name)

					scaleDown(aw)
				}
			} else {
				klog.Infof("Appwrapper deleted scale-down machineset: %s ", aw.Name)
				scaleDown(aw)
			}
		} else {
			deleteMachinePool(aw)
		}
	}
}

func scaleDown(aw *arbv1.AppWrapper) {
	if reuse {
		annotateToDeleteMachine(aw)
	} else {
		deleteMachineSet(aw)
	}

	//make a separate slice
	for idx := range scaledAppwrapper {
		if scaledAppwrapper[idx] == aw.Name {
			scaledAppwrapper[idx] = ""
		}
	}

	pending, aw := IsAwPending()
	if pending {
		demandPerInstanceType := discoverInstanceTypes(aw)
		scaleUp(aw, demandPerInstanceType)
	}
}
