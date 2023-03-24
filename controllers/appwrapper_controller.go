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
	"strconv"

	"os"
	"strings"
	"time"

	mapiclientset "github.com/openshift/client-go/machine/clientset/versioned"
	machineinformersv1beta1 "github.com/openshift/client-go/machine/informers/externalversions"
	"github.com/openshift/client-go/machine/listers/machine/v1beta1"
	arbv1 "github.com/project-codeflare/multi-cluster-app-dispatcher/pkg/apis/controller/v1beta1"
	"github.com/project-codeflare/multi-cluster-app-dispatcher/pkg/client/clientset/controller-versioned/clients"
	arbinformers "github.com/project-codeflare/multi-cluster-app-dispatcher/pkg/client/informers/controller-externalversion"
	v1 "github.com/project-codeflare/multi-cluster-app-dispatcher/pkg/client/listers/controller/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
	Scheme *runtime.Scheme
}

//var nodeCache []string
var scaledAppwrapper []string
var reuse bool = true

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
	//klog.Infof("The appwrapper name is: %v", appwrapper.Name)

	kubeconfig := os.Getenv("KUBECONFIG")
	//kubeconfig := "/Users/abhishekmalvankar/aws/ocp-sched-test-v2/auth/kubeconfig"
	cb, err := NewClientBuilder(kubeconfig)
	if err != nil {
		klog.Fatalf("Error creating clients: %v", err)
	}
	restConfig, err := getRestConfig(kubeconfig)
	if err != nil {
		klog.Info("Failed to get rest config")
	}
	//arbclients = clientset.NewForConfigOrDie(restConfig)
	machineClient = cb.MachineClientOrDie("machine-shared-informer")
	kubeClient, _ = kubernetes.NewForConfig(restConfig)
	factory := machineinformersv1beta1.NewSharedInformerFactoryWithOptions(machineClient, resyncPeriod()(), machineinformersv1beta1.WithNamespace(""))
	informer := factory.Machine().V1beta1().MachineSets().Informer()
	msLister = factory.Machine().V1beta1().MachineSets().Lister()
	machineLister = factory.Machine().V1beta1().Machines().Lister()
	nodesTobeadded, err := kubeClient.CoreV1().ConfigMaps("kube-system").Get(ctx, "instascale-config", metav1.GetOptions{})
	if err != nil {
		klog.Infof("Config map named instascale-config is not available in namespace kube-system")
	}
	for _, v := range nodesTobeadded.Data {
		if maxScaleNodesAllowed, err = strconv.Atoi(v); err != nil {
			klog.Infof("Error configuring value of configmap %v using value  3", maxScaleNodesAllowed)
			maxScaleNodesAllowed = 3
		}
	}
	klog.Infof("Got config map named: %v that configures max nodes in cluster to value %v", nodesTobeadded.Name, maxScaleNodesAllowed)
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

func canScale(demandPerInstanceType map[string]int) bool {
	//control plane can include any number of nodes
	//we count how many additional nodes can be added to the cluster
	var totalNodesAddedByMachinesets int32 = 0
	allMachineSet, err := msLister.MachineSets("").List(labels.Everything())
	if err != nil {
		klog.Infof("Error listing a machineset, %v", err)
	}
	for _, aMachine := range allMachineSet {
		totalNodesAddedByMachinesets += *aMachine.Spec.Replicas
	}
	for _, count := range demandPerInstanceType {
		totalNodesAddedByMachinesets += int32(count)
	}
	klog.Infof("The nodes allowed: %v and total nodes in cluster after node scale-out %v", maxScaleNodesAllowed, totalNodesAddedByMachinesets)
	return totalNodesAddedByMachinesets <= int32(maxScaleNodesAllowed)
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
			if canScale(demandPerInstanceType) {
				scaleUp(aw, demandPerInstanceType)
			} else {
				klog.Infof("Cannot scale up replicas max replicas allowed is %v", maxScaleNodesAllowed)
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
			if canScale(demandPerInstanceType) {
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

func scaleUp(aw *arbv1.AppWrapper, demandMapPerInstanceType map[string]int) {
	if msInformerHasSynced {
		//Assumption is made that the cluster has machineset configure that AW needs
		for userRequestedInstanceType := range demandMapPerInstanceType {
			//TODO: get unique machineset
			replicas := demandMapPerInstanceType[userRequestedInstanceType]
			scaleMachineSet(aw, userRequestedInstanceType, replicas)
		}
		klog.Infof("Completed Scaling for %v", aw.Name)
		scaledAppwrapper = append(scaledAppwrapper, aw.Name)
	}

}

func scaleMachineSet(aw *arbv1.AppWrapper, userRequestedInstanceType string, replicas int) {
	allMachineSet, err := msLister.MachineSets("").List(labels.Everything())
	if err != nil {
		klog.Infof("Error listing a machineset, %v", err)
	}
	for _, aMachineSet := range allMachineSet {
		providerConfig, err := ProviderSpecFromRawExtension(aMachineSet.Spec.Template.Spec.ProviderSpec.Value)
		if err != nil {
			klog.Infof("Error retrieving provider config %v", err)
		}
		if userRequestedInstanceType == providerConfig.InstanceType {

			if reuse {
				copyOfaMachineSet := aMachineSet.DeepCopy()
				additionalReplicas := int32(replicas)
				existingReplicas := *copyOfaMachineSet.Spec.Replicas
				totalReplicas := additionalReplicas + existingReplicas
				copyOfaMachineSet.Spec.Replicas = &totalReplicas

				allExistingMachines, errm := machineClient.MachineV1beta1().Machines(namespaceToList).List(context.Background(), metav1.ListOptions{})
				if errm != nil {
					klog.Infof("Error creating machineset: %v", errm)
				}
				var existingMachinesOwned []string
				for _, machine := range allExistingMachines.Items {
					for k, v := range machine.Labels {
						//klog.Infof("looking for key %v", k)
						if k == "machine.openshift.io/cluster-api-machineset" {
							//klog.Infof("looking for value %v", v)
							machinePhase := *machine.Status.Phase
							if v == copyOfaMachineSet.Name && !contains(existingMachinesOwned, v) && (machinePhase != "Deleting") {
								existingMachinesOwned = append(existingMachinesOwned, machine.Name)
							}
						}
					}
				}
				klog.Infof("Existing machines owned are %v", existingMachinesOwned)

				//copyOfaMachineSet.ResourceVersion = ""
				ms, err := machineClient.MachineV1beta1().MachineSets(namespaceToList).Update(context.Background(), copyOfaMachineSet, metav1.UpdateOptions{})
				if err != nil {
					klog.Infof("Error creating machineset %v", err)
					return
				}
				//wait until all replicas are available
				for (totalReplicas - ms.Status.AvailableReplicas) != 0 {
					//TODO: user can delete appwrapper work on triggering scale-down
					klog.Infof("REUSE: waiting for machines to be in state Ready. replicas needed: %v and replicas available: %v", totalReplicas, ms.Status.AvailableReplicas)
					time.Sleep(1 * time.Minute)
					ms, _ = machineClient.MachineV1beta1().MachineSets(namespaceToList).Get(context.Background(), copyOfaMachineSet.Name, metav1.GetOptions{})
					klog.Infof("REUSE: Querying machineset %v to get updated replicas", ms.Name)
				}
				addedMachines, errm := machineClient.MachineV1beta1().Machines(namespaceToList).List(context.Background(), metav1.ListOptions{})
				if errm != nil {
					klog.Infof("Error creating machineset: %v", errm)
				}
				for _, machine := range addedMachines.Items {
					for k, v := range machine.Labels {
						if k == "machine.openshift.io/cluster-api-machineset" {
							if !contains(existingMachinesOwned, machine.Name) && v == copyOfaMachineSet.Name {
								nodeName := machine.Status.NodeRef.Name
								addLabelToMachine(aw, machine.Name)
								addLabelToNode(aw, nodeName)
							}
						}
					}
				}
				return
			} else {
				copyOfaMachineSet := aMachineSet.DeepCopy()
				replicas := int32(replicas)
				copyOfaMachineSet.Spec.Replicas = &replicas
				copyOfaMachineSet.ResourceVersion = ""
				copyOfaMachineSet.Spec.Template.Spec.Taints = []corev1.Taint{{Key: aw.Name, Value: "value1", Effect: "PreferNoSchedule"}}
				copyOfaMachineSet.Name = aw.Name + "-" + userRequestedInstanceType
				copyOfaMachineSet.Spec.Template.Labels = map[string]string{
					aw.Name: aw.Name,
				}
				workerLabels := map[string]string{
					aw.Name: aw.Name,
				}
				copyOfaMachineSet.Spec.Selector = metav1.LabelSelector{
					MatchLabels: workerLabels,
				}
				copyOfaMachineSet.Labels["aw"] = aw.Name
				ms, err := machineClient.MachineV1beta1().MachineSets(namespaceToList).Create(context.Background(), copyOfaMachineSet, metav1.CreateOptions{})
				if err != nil {
					klog.Infof("Error creating machineset %v", err)
					return
				}
				//wait until all replicas are available
				for (replicas - ms.Status.AvailableReplicas) != 0 {
					//TODO: user can delete appwrapper work on triggering scale-down
					klog.Infof("waiting for machines to be in state Ready. replicas needed: %v and replicas available: %v", replicas, ms.Status.AvailableReplicas)
					time.Sleep(1 * time.Minute)
					ms, _ = machineClient.MachineV1beta1().MachineSets(namespaceToList).Get(context.Background(), copyOfaMachineSet.Name, metav1.GetOptions{})
					klog.Infof("Querying machineset %v to get updated replicas", ms.Name)
				}
				klog.Infof("Machines are available. replicas needed: %v and replicas available: %v", replicas, ms.Status.AvailableReplicas)
				allMachines, errm := machineClient.MachineV1beta1().Machines(namespaceToList).List(context.Background(), metav1.ListOptions{})
				if errm != nil {
					klog.Infof("Error creating machineset: %v", errm)
				}
				//map machines to machinesets?
				//for non-reuse case labels can be added directly to machineset: https://github.com/openshift/machine-api-operator/issues/1077
				for idx := range allMachines.Items {
					machine := &allMachines.Items[idx]
					for k, _ := range machine.Labels {
						if k == aw.Name {
							nodeName := machine.Status.NodeRef.Name
							labelPatch := fmt.Sprintf(`[{"op":"add","path":"/metadata/labels/%s","value":"%s" }]`, aw.Name, aw.Name)
							ms, err := kubeClient.CoreV1().Nodes().Patch(context.Background(), nodeName, types.JSONPatchType, []byte(labelPatch), metav1.PatchOptions{})
							if len(ms.Labels) > 0 && err == nil {
								klog.Infof("label added to node %v, for scaling %v", nodeName, copyOfaMachineSet.Name)
							}
						}
					}
				}
				return
			}

		}
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

func removeLabelFromMachine(aw *arbv1.AppWrapper, machineName string) {
	labelPatch := fmt.Sprintf(`[{"op":"remove","path":"/metadata/labels/%s","value":"%s" }]`, aw.Name, aw.Name)
	_, err := machineClient.MachineV1beta1().Machines(namespaceToList).Patch(context.Background(), machineName, types.JSONPatchType, []byte(labelPatch), metav1.PatchOptions{})
	if err != nil {
		klog.Infof("deleted label to machines %v", err)
	}
}

func removeLabelFromNode(aw *arbv1.AppWrapper, nodeName string) {
	labelPatch := fmt.Sprintf(`[{"op":"remove","path":"/metadata/labels/%s","value":"%s" }]`, aw.Name, aw.Name)
	_, err := kubeClient.CoreV1().Nodes().Patch(context.Background(), nodeName, types.JSONPatchType, []byte(labelPatch), metav1.PatchOptions{})
	if err != nil {
		klog.Infof("Error deleted label to machines %v", err)
	}
}

func addLabelToMachine(aw *arbv1.AppWrapper, machineName string) {
	labelPatch := fmt.Sprintf(`[{"op":"add","path":"/metadata/labels/%s","value":"%s" }]`, aw.Name, aw.Name)
	_, err := machineClient.MachineV1beta1().Machines(namespaceToList).Patch(context.Background(), machineName, types.JSONPatchType, []byte(labelPatch), metav1.PatchOptions{})
	if err != nil {
		klog.Infof("Error adding label to machines %v", err)
	}
}

func addLabelToNode(aw *arbv1.AppWrapper, nodeName string) {
	labelPatch := fmt.Sprintf(`[{"op":"add","path":"/metadata/labels/%s","value":"%s" }]`, aw.Name, aw.Name)
	_, err := kubeClient.CoreV1().Nodes().Patch(context.Background(), nodeName, types.JSONPatchType, []byte(labelPatch), metav1.PatchOptions{})
	if err != nil {
		klog.Infof("Error adding label to machine %v", err)
	}
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

// add logic to swap out labels with new appwrapper label
func swapNodeLabels(oldAw *arbv1.AppWrapper, newAw *arbv1.AppWrapper) {
	allMachines, errm := machineClient.MachineV1beta1().Machines(namespaceToList).List(context.Background(), metav1.ListOptions{})
	if errm != nil {
		klog.Infof("Error creating machineset: %v", errm)
	}
	//klog.Infof("Got all machines %v", allMachines)
	for idx := range allMachines.Items {
		machine := &allMachines.Items[idx]
		for k, _ := range machine.Labels {
			if k == oldAw.Name {
				nodeName := machine.Status.NodeRef.Name
				klog.Infof("removing label from node %v that belonged to appwrapper %v and adding node to new appwrapper %v", nodeName, oldAw.Name, newAw.Name)
				removeLabelFromMachine(oldAw, machine.Name)
				removeLabelFromNode(oldAw, nodeName)
				addLabelToMachine(newAw, machine.Name)
				addLabelToNode(newAw, nodeName)
			}
		}
	}
}

func onDelete(obj interface{}) {
	aw, ok := obj.(*arbv1.AppWrapper)
	if ok {
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

	}

}

func deleteMachineSet(aw *arbv1.AppWrapper) {
	var err error
	allMachineSet, _ := msLister.MachineSets("").List(labels.Everything())
	for _, aMachineSet := range allMachineSet {
		//klog.Infof("%v.%v", aMachineSet.Name, aw.Name)
		if strings.Contains(aMachineSet.Name, aw.Name) {
			//klog.Infof("Deleting machineset named %v", aw.Name)
			err = machineClient.MachineV1beta1().MachineSets(namespaceToList).Delete(context.Background(), aMachineSet.Name, metav1.DeleteOptions{})
			if err == nil {
				klog.Infof("Delete successful")
			}
		}
	}
}

func annotateToDeleteMachine(aw *arbv1.AppWrapper) {
	nodes, _ := kubeClient.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})

	for _, node := range nodes.Items {
		for k, _ := range node.Labels {
			//get nodes that have AW name as key
			if k == aw.Name {
				klog.Infof("Filtered node name is %v", aw.Name)
				for k, v := range node.Annotations {
					if k == "machine.openshift.io/machine" {
						machineName := strings.Split(v, "/")
						klog.Infof("The machine name to be annotated %v", machineName[1])
						allMachines, _ := machineClient.MachineV1beta1().Machines(namespaceToList).List(context.Background(), metav1.ListOptions{})
						for _, aMachine := range allMachines.Items {
							//remove index hardcoding
							updateMachine := aMachine.DeepCopy()
							if aMachine.Name == machineName[1] {
								updateMachine.Annotations["machine.openshift.io/cluster-api-delete-machine"] = "true"
								machine, err := machineClient.MachineV1beta1().Machines(namespaceToList).Update(context.Background(), updateMachine, metav1.UpdateOptions{})
								if err == nil {
									klog.Infof("update successful")
								}
								var updateMachineset string = ""
								for k, v := range machine.Labels {
									if k == "machine.openshift.io/cluster-api-machineset" {
										updateMachineset = v
									}
									klog.Infof("Machineset to update is %v", updateMachineset)
								}
								if updateMachineset != "" {
									allMachineSet, err := msLister.MachineSets("").List(labels.Everything())
									if err != nil {
										klog.Infof("Machineset retrieval error")
									}
									for _, aMachineSet := range allMachineSet {
										if aMachineSet.Name == updateMachineset {
											klog.Infof("Existing machineset replicas %v", aMachineSet.Spec.Replicas)
											//scale down is harded coded to 1??
											newReplicas := *aMachineSet.Spec.Replicas - int32(1)
											updateMsReplicas := aMachineSet.DeepCopy()
											updateMsReplicas.Spec.Replicas = &newReplicas
											_, err = machineClient.MachineV1beta1().MachineSets(namespaceToList).Update(context.Background(), updateMsReplicas, metav1.UpdateOptions{})
											if err == nil {
												klog.Infof("Replica update successful")
											}
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}
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
