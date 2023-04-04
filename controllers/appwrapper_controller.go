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
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/openshift-online/ocm-sdk-go"
	cmv1 "github.com/openshift-online/ocm-sdk-go/clustersmgmt/v1"
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
	Scheme             *runtime.Scheme
	ConfigmapNamespace string
}

//var nodeCache []string
var scaledAppwrapper []string
var reuse bool = true
var machineset bool = true

const (
	namespaceToList = "openshift-machine-api"
	minResyncPeriod = 10 * time.Minute
	kubeconfig      = ""
)

var maxScaleNodesAllowed int
var msLister v1beta1.MachineSetLister
var machineLister v1beta1.MachineLister
var msInformerHasSynced bool
var machineClient mapiclientset.Interface
var queueJobLister v1.AppWrapperLister

//var arbclients *clientset.Clientset
var kubeClient *kubernetes.Clientset

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

	// TODO(user): your logic here
	var appwrapper arbv1.AppWrapper
	if err := r.Get(ctx, req.NamespacedName, &appwrapper); err != nil {
		if apierrors.IsNotFound(err) {
			//ignore not-found errors, since we can get them on delete requests.
			return ctrl.Result{}, nil
		}
		klog.Error(err, "unable to fetch appwrapper")
	}

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
	factory := machineinformersv1beta1.NewSharedInformerFactoryWithOptions(machineClient, resyncPeriod()(), machineinformersv1beta1.WithNamespace(""))
	informer := factory.Machine().V1beta1().MachineSets().Informer()
	msLister = factory.Machine().V1beta1().MachineSets().Lister()
	machineLister = factory.Machine().V1beta1().Machines().Lister()
	instascaleConfigs, err := kubeClient.CoreV1().ConfigMaps(r.ConfigmapNamespace).Get(ctx, "instascale-config", metav1.GetOptions{})
	if err != nil {
		klog.Infof("Config map named instascale-config is not available in namespace %v", r.ConfigmapNamespace)
	}

	for key, value := range instascaleConfigs.Data {
		if key == "maxScaleoutAllowed" {
			if maxScaleNodesAllowed, err = strconv.Atoi(value); err != nil {
				klog.Infof("Error converting %v to int. Setting maxScaleNodesAllowed to 3", maxScaleNodesAllowed)
				maxScaleNodesAllowed = 3
			}
		}
	}
	klog.Infof("Got config map named %v from namespace %v that configures max nodes in cluster to value %v", instascaleConfigs.Name, instascaleConfigs.Namespace, maxScaleNodesAllowed)

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
			if machineset {
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
	for _, genericItem := range aw.Spec.AggrResources.GenericItems {
		for idx, val := range genericItem.CustomPodResources {
			instanceName := instanceRequired[idx]
			klog.Infof("Got instance name %v", instanceName)
			demandMapPerInstanceType[instanceName] = val.Replicas
			instanceRequired = append(instanceRequired[:idx], instanceRequired[idx+1:]...)
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
			if machineset {
				scaleMachineSet(aw, userRequestedInstanceType, replicas)
			} else {
				scaleMachinepool(aw, userRequestedInstanceType, replicas)
			}
		}
		klog.Infof("Completed Scaling for %v", aw.Name)
		scaledAppwrapper = append(scaledAppwrapper, aw.Name)
	}

}

func scaleMachinepool(aw *arbv1.AppWrapper, userRequestedInstanceType string, replicas int) {
	logger, err := sdk.NewGoLoggerBuilder().
		Debug(true).
		Build()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Can't build logger: %v\n", err)
		os.Exit(1)
	}

	// Create the connection, and remember to close it:
	token := ""
	connection, err := sdk.NewConnectionBuilder().
		Logger(logger).
		Tokens(token).
		Build()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Can't build connection: %v\n", err)
		os.Exit(1)
	}
	defer connection.Close()
	m := make(map[string]string)
	m[aw.Name] = aw.Name
	createMachinePool, _ := cmv1.NewMachinePool().ID(aw.Name).InstanceType(userRequestedInstanceType).Replicas(replicas).Labels(m).Build()
	klog.Infof("Create machinepool with instance type %v and name %v", userRequestedInstanceType, createMachinePool)

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
		if machineset {
			if reuse {
				matchedAw := findExactMatch(aw)
				if matchedAw != nil {
					klog.Infof("Appwrapper %s deleted, swapping machines to %s", aw.Name, matchedAw.Name)
					swapNodeLabels(aw, matchedAw)
				} else {
					klog.Infof("Appwrapper %s deleted, scaling down machines", aw.Name)
					scaleDown((aw))
				}
			} else {
				klog.Infof("Appwrapper deleted scale-down machineset: %s ", aw.Name)
				scaleDown(aw)
			}
		} else {
			deleteMachinepool(aw)
		}
	}

}

func deleteMachinepool(aw *arbv1.AppWrapper) {
	clusterID := ""
	token := ""
	logger, err := sdk.NewGoLoggerBuilder().
		Debug(true).
		Build()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Can't build logger: %v\n", err)
		os.Exit(1)
	}
	connection, err := sdk.NewConnectionBuilder().
		Logger(logger).
		Tokens(token).
		Build()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Can't build connection: %v\n", err)
		os.Exit(1)
	}
	defer connection.Close()
	machinePoolsConnection := connection.ClustersMgmt().V1().Clusters().Cluster(clusterID).MachinePools().List()

	machinePoolsListResponse, _ := machinePoolsConnection.Send()
	machinePoolsList := machinePoolsListResponse.Items()
	machinePoolsList.Range(func(index int, item *cmv1.MachinePool) bool {
		fmt.Println(item.GetID())
		id, _ := item.GetID()
		//As a test update every replicas to 2
		//Need to run this code to actual API to make it run
		if aw.Name == id {
			targetMachinePool, err := connection.ClustersMgmt().V1().Clusters().Cluster("cluster-id").MachinePools().MachinePool(aw.Name).Delete().SendContext(context.Background())
			if err != nil {
				klog.Infof("Error deleting target machinepool %v", targetMachinePool)
			}
		}
		return true
	})
}

func scaleDown(aw *arbv1.AppWrapper) {
	if reuse {
		annotateToDeleteMachine(aw)
	} else {
		deleteMachineSet(aw)
	}

	//make a seperate slice
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
