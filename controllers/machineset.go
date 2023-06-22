package controllers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"strconv"

	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	arbv1 "github.com/project-codeflare/multi-cluster-app-dispatcher/pkg/apis/controller/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	isScaled = false
)

func (r *AppWrapperReconciler) checkExistingMachineSet(ctx context.Context, machineSetName string) bool {
	// Set up the Object key with the requested app wrapper name and requested type
	key := client.ObjectKey{
		Name:      machineSetName,
		Namespace: namespaceToList,
	}

	machineSet := &machinev1beta1.MachineSet{}
	err := r.Get(ctx, key, machineSet)
	if err != nil {
		// Check if the error is due to the MachineSet not existing
		if client.IgnoreNotFound(err) != nil {
			// return error if it there is a different error for not getting the MachineSet
			klog.Infof("Error getting MachineSet: %s", err)
		}
		// MachineSet does not exist
		return false
	}
	// The MachineSet exists
	return true
}

func (r *AppWrapperReconciler) reconcileCreateMachineSet(ctx context.Context, aw *arbv1.AppWrapper, demandMapPerInstanceType map[string]int) (ctrl.Result, error) {

	allMachineSet := machinev1beta1.MachineSetList{}
	err := r.List(ctx, &allMachineSet)
	if err != nil {
		klog.Infof("Error listing a machineset, %v", err)
	}

	for userRequestedInstanceType := range demandMapPerInstanceType {
		for _, aMachineSet := range allMachineSet.Items {
			providerConfig, err := ProviderSpecFromRawExtension(aMachineSet.Spec.Template.Spec.ProviderSpec.Value)
			if err != nil {
				klog.Infof("Error retrieving provider config %v", err)
			}
			if userRequestedInstanceType == providerConfig.InstanceType {
				labelSelector := labels.SelectorFromSet(labels.Set(map[string]string{
					aw.Name: aw.Name,
				}))
				copyOfaMachineSet := aMachineSet.DeepCopy()
				replicas := int32(demandMapPerInstanceType[userRequestedInstanceType])
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
				copyOfaMachineSet.Labels["instascale.codeflare.dev/aw"] = aw.Name

				if err := r.Create(ctx, copyOfaMachineSet); err != nil {
					klog.Infof("Error creating machineset %v", err)
				} else {
					return ctrl.Result{Requeue: true, RequeueAfter: 5 * time.Second}, nil
				}

				//wait until all replicas are available
				if (replicas - copyOfaMachineSet.Status.AvailableReplicas) != 0 {
					klog.Infof("waiting for machines to be in state Ready. replicas needed: %v and replicas available: %v", replicas, copyOfaMachineSet.Status.AvailableReplicas)
					klog.Infof("Querying machineset %v to get updated replicas", copyOfaMachineSet.Name)
					return ctrl.Result{Requeue: true, RequeueAfter: 30 * time.Second}, nil
				}
				klog.Infof("Machines are available. replicas needed: %v and replicas available: %v", replicas, copyOfaMachineSet.Status.AvailableReplicas)
				allMachines := machinev1beta1.MachineList{}
				err = r.List(ctx, &allMachines)
				if err != nil {
					klog.Infof("Error listing machines: %v", err)
				}
				//map machines to machinesets?
				//for non-reuse case labels can be added directly to machineset: https://github.com/openshift/machine-api-operator/issues/1077
				listOptions := &client.ListOptions{
					LabelSelector: labelSelector,
				}
				err = r.List(ctx, &allMachines, listOptions)
				if err != nil {
					klog.Infof("Error listing machines: %v", err)
				}
				for idx := range allMachines.Items {
					machine := &allMachines.Items[idx]
					nodeName := machine.Status.NodeRef.Name
					labelPatch := fmt.Sprintf(`[{"op":"add","path":"/metadata/labels/%s","value":"%s" }]`, aw.Name, aw.Name)
					ms, err := r.kubeClient.CoreV1().Nodes().Patch(context.Background(), nodeName, types.JSONPatchType, []byte(labelPatch), metav1.PatchOptions{})
					if err != nil {
						klog.Infof("Error patching label to Node: %v", err)
					}
					if len(ms.Labels) > 0 && err == nil {
						klog.Infof("label added to node %v, for scaling %v", nodeName, copyOfaMachineSet.Name)
					}
				}
			}
		}
	}

	return ctrl.Result{}, nil
}

func (r *AppWrapperReconciler) filterAwMachines(ctx context.Context, aw *arbv1.AppWrapper, userRequestedInstanceType string) []string {
	labelSelector := labels.SelectorFromSet(labels.Set(map[string]string{
		aw.Name:                              aw.Name,
		"machine.openshift.io/instance-type": userRequestedInstanceType,
	}))
	listOptions := &client.ListOptions{
		LabelSelector: labelSelector,
	}

	machinesWithAwLabel := machinev1beta1.MachineList{}
	err := r.List(ctx, &machinesWithAwLabel, listOptions)
	if err != nil {
		klog.Infof("Error listing machines: %s", err)
	}
	var runningAwMachines []string
	for _, machine := range machinesWithAwLabel.Items {
		machinePhase := machine.Status.Phase
		if *machinePhase == "Running" {
			runningAwMachines = append(runningAwMachines, machine.Name)
		}
	}
	return runningAwMachines
}

func (r *AppWrapperReconciler) filterAllRunningAwMachines(ctx context.Context, aw *arbv1.AppWrapper) []string {
	labelSelector := labels.SelectorFromSet(labels.Set(map[string]string{
		aw.Name: aw.Name,
	}))
	listOptions := &client.ListOptions{
		LabelSelector: labelSelector,
	}
	machinesWithAwLabel := machinev1beta1.MachineList{}
	var allRunningAwMachines []string
	err := r.List(ctx, &machinesWithAwLabel, listOptions)
	if err != nil {
		klog.Infof("Error listing machines: %s", err)
	}

	for _, machine := range machinesWithAwLabel.Items {
		machinePhase := machine.Status.Phase
		if *machinePhase != "Deleting" {
			allRunningAwMachines = append(allRunningAwMachines, machine.Name)
		}
	}
	return allRunningAwMachines
}

func (r *AppWrapperReconciler) canScaleMachineset(ctx context.Context, aw *arbv1.AppWrapper, demandPerInstanceType map[string]int) bool {
	//control plane can include any number of nodes
	//we count how many additional nodes can be added to the cluster

	allRunningAwMachines := r.filterAllRunningAwMachines(ctx, aw)

	var totalNodesAddedByMachinesets int32 = 0

	totalNodesAddedByMachinesets += int32(len(allRunningAwMachines))

	for _, count := range demandPerInstanceType {
		totalNodesAddedByMachinesets += int32(count)
	}
	if totalNodesAddedByMachinesets >= int32(maxScaleNodesAllowed) {
		klog.Infof("Scaling not possible: The nodes allowed %v and total nodes in cluster after node scale-out %v", maxScaleNodesAllowed, totalNodesAddedByMachinesets)
	}
	return totalNodesAddedByMachinesets <= int32(maxScaleNodesAllowed)
}

func (r *AppWrapperReconciler) reconcileReuseMachineSet(ctx context.Context, aw *arbv1.AppWrapper, demandMapPerInstanceType map[string]int) (ctrl.Result, error) {
	var runningAwMachines []string

	// Gather a list of all MachineSets
	allMachineSets := machinev1beta1.MachineSetList{}
	if err := r.List(ctx, &allMachineSets); err != nil {
		klog.Infof("Error listing MachineSets: %s", err)
	}

	for userRequestedInstanceType := range demandMapPerInstanceType {
		// Iterate over each MachineSet in the allMachineSets list
		for _, aMachineSet := range allMachineSets.Items {
			// Get the MachineSet's instance type
			providerConfig, err := ProviderSpecFromRawExtension(aMachineSet.Spec.Template.Spec.ProviderSpec.Value)
			if err != nil {
				klog.Infof("Error retrieving provider config %v", err)
			}
			// Label selector for gettings machines of requested type and app wrapper label
			labelSelector := labels.SelectorFromSet(labels.Set(map[string]string{
				aw.Name:                              aw.Name,
				"machine.openshift.io/instance-type": userRequestedInstanceType,
			}))
			listOptions := &client.ListOptions{
				LabelSelector: labelSelector,
			}
			// Compare this MachineSet's instance type with the user requested instance type
			if userRequestedInstanceType == providerConfig.InstanceType {
				// Get a list of running machines with the AppWrapper label
				runningAwMachines = r.filterAwMachines(ctx, aw, userRequestedInstanceType)
				// Calculate what the new total number of replicas should be
				copyOfaMachineSet := aMachineSet.DeepCopy()
				replicas := demandMapPerInstanceType[userRequestedInstanceType]
				existingReplicas := int32(*copyOfaMachineSet.Spec.Replicas) - int32(len(runningAwMachines))
				totalReplicas := int32(replicas) + existingReplicas
				copyOfaMachineSet.Spec.Replicas = &totalReplicas

				// Check if MachineSet has replica label already and set isScaled = true
				for k, v := range aMachineSet.Labels {
					if k == fmt.Sprintf("instascale.codeflare.dev/%s", aw.Name) {
						if v == strconv.FormatInt(int64(replicas), 10) {
							isScaled = true
						}
					}
				}

				// Label Selector which will list machines that belong to the specific Machine Set
				instanceLabelSelector := labels.SelectorFromSet(labels.Set(map[string]string{
					"machine.openshift.io/instance-type":          userRequestedInstanceType,
					"machine.openshift.io/cluster-api-machineset": aMachineSet.Name,
				}))
				instanceListOptions := &client.ListOptions{
					LabelSelector: instanceLabelSelector,
				}

				// List all machines currently in the specific MachineSet
				allExistingMachinesInMs := machinev1beta1.MachineList{}
				if err := r.List(ctx, &allExistingMachinesInMs, instanceListOptions); err != nil {
					klog.Infof("Error listing Machines: %s", err)
				}

				/*
				   A for loop that will list machines with the AppWrapper label and apply the label
				   if the count of machines with labels does not match the number of requested replicas
				*/
				machinesWithAwLabel := machinev1beta1.MachineList{}
				if len(allExistingMachinesInMs.Items) > 0 {
					for _, machine := range allExistingMachinesInMs.Items {
						err := r.List(ctx, &machinesWithAwLabel, listOptions)
						if err != nil {
							klog.Infof("Error listing machines: %v", err)
						}
						machinePhase := machine.Status.Phase

						if len(machinesWithAwLabel.Items) != replicas && (*machinePhase == "Running") {
							nodeName := machine.Status.NodeRef.Name
							r.addLabelToMachine(ctx, aw, machine.Name)
							r.addLabelToNode(ctx, aw, nodeName)
						}
					}
				}

				// Get a list of running machines with the AppWrapper label
				runningAwMachines = r.filterAwMachines(ctx, aw, userRequestedInstanceType)
				canScaleMachineSets := r.canScaleMachineset(ctx, aw, demandMapPerInstanceType)
				if (!isScaled || len(runningAwMachines) != replicas) && canScaleMachineSets {
					// A loop to get the names of existing machines in the MachineSet
					var existingMachinesOwned []string
					for _, machine := range allExistingMachinesInMs.Items {
						machinePhase := machine.Status.Phase
						if !contains(existingMachinesOwned, machine.Name) && (*machinePhase != "Deleting") {
							existingMachinesOwned = append(existingMachinesOwned, machine.Name)
						}
					}
					klog.Infof("Existing machines owned in %s are %v", copyOfaMachineSet.Name, existingMachinesOwned)

					// Apply the requested replica count label to the MachineSet
					copyOfaMachineSet.Labels[fmt.Sprintf("instascale.codeflare.dev/%s", aw.Name)] = strconv.FormatInt(int64(replicas), 10)

					// If the MachineSet's requested replica count label is not equal to the requested replica count then the machineSet must be updated
					if aMachineSet.Labels[fmt.Sprintf("instascale.codeflare.dev/%s", aw.Name)] != strconv.FormatInt(int64(replicas), 10) {
						if strconv.FormatInt(int64(replicas), 10) < aMachineSet.Labels[fmt.Sprintf("instascale.codeflare.dev/%s", aw.Name)] {
							r.removeMachinesBasedOnReplicas(ctx, aw, userRequestedInstanceType, int(replicas))
						} else {
							klog.Infof("The instanceRequired array: %v", userRequestedInstanceType)
							if err := r.Update(ctx, copyOfaMachineSet); err != nil {
								klog.Infof("Error updating MachineSet: %s", err)
							}
							klog.Infof("Updated MachineSet: %s", copyOfaMachineSet.Name)
						}
						return ctrl.Result{Requeue: true}, nil
					}
					/*
						A conditional statement which will make sure that if the number of requested replicas != to number of scaled machines
						a message is printed to the console and the Reconcile method is called after 30 seconds
					*/
					if replicas != len(runningAwMachines) {
						klog.Infof("REUSE: waiting for machines to be in state Ready. replicas needed: %v and replicas available: %v", replicas, len(runningAwMachines))
						return ctrl.Result{Requeue: true, RequeueAfter: 30 * time.Second}, nil
					}
				}
			}
		}
		continue
	}
	return ctrl.Result{}, nil
}

// This function exists for scenarios where the user has altered the number of replicas requested on the AppWrapper
func (r *AppWrapperReconciler) removeMachinesBasedOnReplicas(ctx context.Context, aw *arbv1.AppWrapper, userRequestedInstanceType string, replicas int) (ctrl.Result, error) {
	// We get a list of Nodes with the AppWrapper name and correct instance type
	labelSelector := labels.SelectorFromSet(labels.Set(map[string]string{
		aw.Name:                            aw.Name,
		"node.kubernetes.io/instance-type": userRequestedInstanceType,
	}))
	listOptions := &metav1.ListOptions{
		LabelSelector: labelSelector.String(),
	}
	nodes, _ := r.kubeClient.CoreV1().Nodes().List(context.TODO(), *listOptions)

	// We get the number of running labeled machines
	numberOfmachines := len(r.filterAwMachines(ctx, aw, userRequestedInstanceType))
	// A "while" loop that compares the number of replicas to the number of labeled machines
	for replicas < numberOfmachines {
		for _, node := range nodes.Items {
			if replicas == numberOfmachines {
				break
			}
			klog.Infof("Filtered Node name: %s", node.Name)
			// We get the machine annotation from the node in order to get the machine's name
			for k, v := range node.Annotations {
				if k == "machine.openshift.io/machine" {
					machineName := strings.Split(v, "/")
					klog.Infof("The machine name to be annotated %v", machineName[1])

					// Get the specific machine that owns this node
					nodeMachine := &machinev1beta1.Machine{}
					if err := r.Get(ctx, types.NamespacedName{Name: machineName[1], Namespace: namespaceToList}, nodeMachine); err != nil {
						klog.Infof("Error getting machine: %s", err)
					}

					updateMachine := nodeMachine.DeepCopy()
					updateMachine.Annotations["machine.openshift.io/cluster-api-delete-machine"] = "true"
					if err := r.Update(ctx, updateMachine); err != nil {
						klog.Infof("Error updating Machine: %s", err)
					}
					klog.Infof("Successfully updated %s", updateMachine.Name)

					// Get the MachineSet name of the specific Machine
					var updateMachinesetName string = ""
					for k, v := range updateMachine.Labels {
						if k == "machine.openshift.io/cluster-api-machineset" {
							updateMachinesetName = v
							klog.Infof("Machineset to update is %v", updateMachinesetName)
						}
					}

					if updateMachinesetName != "" {
						allMachineSet := machinev1beta1.MachineSetList{}
						if err := r.List(ctx, &allMachineSet); err != nil {
							klog.Infof("Error listing MachineSets: %s", err)
						}
						for _, aMachineSet := range allMachineSet.Items {
							if aMachineSet.Name == updateMachinesetName {
								klog.Infof("Existing machineset replicas %v", *aMachineSet.Spec.Replicas)
								// Scale down 1 replica per loop iteration
								newReplicas := *aMachineSet.Spec.Replicas - int32(1)
								updateMsReplicas := aMachineSet.DeepCopy()
								updateMsReplicas.Spec.Replicas = &newReplicas
								// Update the label to reflect new number of replicas
								updateMsReplicas.Labels[fmt.Sprintf("instascale.codeflare.dev/%s", aw.Name)] = strconv.FormatInt(int64(replicas), 10)

								if err := r.Update(ctx, updateMsReplicas); err != nil {
									klog.Infof("Error updating MachineSet: %s", err)
								}
								klog.Infof("Successfully removed node and updated MachineSet")
								numberOfmachines = numberOfmachines - 1

								return ctrl.Result{Requeue: true}, nil
							}
						}
					}
				}
			}
		}
		break
	}
	return ctrl.Result{}, nil
}

func (r *AppWrapperReconciler) annotateToDeleteMachine(ctx context.Context, aw *arbv1.AppWrapper) {
	nodes, _ := r.kubeClient.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})

	for _, node := range nodes.Items {
		for k := range node.Labels {
			//get nodes that have AW name as key
			if k == aw.Name {
				klog.Infof("Filtered node name is %v", aw.Name)
				for k, v := range node.Annotations {
					if k == "machine.openshift.io/machine" {
						machineName := strings.Split(v, "/")
						klog.Infof("The machine name to be annotated %v", machineName[1])
						allMachines := machinev1beta1.MachineList{}
						errm := r.List(ctx, &allMachines)
						if errm != nil {
							klog.Infof("Error listing machines: %v", errm)
						}
						for _, aMachine := range allMachines.Items {
							//remove index hardcoding
							updateMachine := aMachine.DeepCopy()
							if aMachine.Name == machineName[1] {
								updateMachine.Annotations["machine.openshift.io/cluster-api-delete-machine"] = "true"
								err := r.Update(ctx, updateMachine)
								if err == nil {
									klog.Infof("update successful")
								}
								var updateMachineset string = ""
								for k, v := range updateMachine.Labels {
									if k == "machine.openshift.io/cluster-api-machineset" {
										updateMachineset = v
										klog.Infof("Machineset to update is %v", updateMachineset)
									}
								}
								if updateMachineset != "" {
									allMachineSet := machinev1beta1.MachineSetList{}
									err := r.List(ctx, &allMachineSet)
									if err != nil {
										klog.Infof("Machineset retrieval error")
									}
									for _, aMachineSet := range allMachineSet.Items {
										if aMachineSet.Name == updateMachineset {
											klog.Infof("Existing machineset replicas %v", aMachineSet.Spec.Replicas)
											//scale down is harded coded to 1??
											newReplicas := *aMachineSet.Spec.Replicas - int32(1)
											updateMsReplicas := aMachineSet.DeepCopy()
											updateMsReplicas.Spec.Replicas = &newReplicas
											err := r.Update(ctx, updateMsReplicas)
											if err == nil {
												klog.Infof("Replica update successful")
											}
											r.removeMachineSetLabel(ctx, aw, aMachineSet.Name)
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

func (r *AppWrapperReconciler) addLabelToMachine(ctx context.Context, aw *arbv1.AppWrapper, machineName string) {
	labelPatch := fmt.Sprintf(`[{"op":"add","path":"/metadata/labels/%s","value":"%s" }]`, aw.Name, aw.Name)
	patchBytes := []byte(labelPatch)

	// Retrieve the machine object
	machine := &machinev1beta1.Machine{}
	err := r.Get(ctx, types.NamespacedName{Name: machineName, Namespace: namespaceToList}, machine)
	if err != nil {
		klog.Infof("Error retrieving machine: %v", err)
		return
	}

	// Apply the patch to add the label
	patch := client.RawPatch(types.JSONPatchType, patchBytes)
	err = r.Patch(ctx, machine, patch)
	if err != nil {
		klog.Infof("Error adding label to machine: %v", err)
	}
}

func (r *AppWrapperReconciler) addLabelToNode(ctx context.Context, aw *arbv1.AppWrapper, nodeName string) {
	labelPatch := fmt.Sprintf(`[{"op":"add","path":"/metadata/labels/%s","value":"%s" }]`, aw.Name, aw.Name)
	_, err := r.kubeClient.CoreV1().Nodes().Patch(ctx, nodeName, types.JSONPatchType, []byte(labelPatch), metav1.PatchOptions{})
	if err != nil {
		klog.Infof("Error adding label to machine %v", err)
	}
}

func (r *AppWrapperReconciler) removeLabelFromMachine(ctx context.Context, aw *arbv1.AppWrapper, machineName string) {
	labelPatch := fmt.Sprintf(`[{"op":"remove","path":"/metadata/labels/%s","value":"%s" }]`, aw.Name, aw.Name)
	patchBytes := []byte(labelPatch)
	// Retrieve the machine object
	machine := &machinev1beta1.Machine{}
	err := r.Get(ctx, types.NamespacedName{Name: machineName, Namespace: namespaceToList}, machine)
	if err != nil {
		klog.Infof("Error retrieving machine: %v", err)
		return
	}

	// Apply the patch to remove the label
	patch := client.RawPatch(types.JSONPatchType, patchBytes)
	err = r.Patch(ctx, machine, patch)
	if err != nil {
		klog.Infof("Error removing label from machine: %v", err)
	}
}

func (r *AppWrapperReconciler) removeLabelFromNode(aw *arbv1.AppWrapper, nodeName string) {
	labelPatch := fmt.Sprintf(`[{"op":"remove","path":"/metadata/labels/%s","value":"%s" }]`, aw.Name, aw.Name)
	_, err := r.kubeClient.CoreV1().Nodes().Patch(context.Background(), nodeName, types.JSONPatchType, []byte(labelPatch), metav1.PatchOptions{})
	if err != nil {
		klog.Infof("Error deleted label to machines %v", err)
	}
}

// add logic to swap out labels with new appwrapper label
func (r *AppWrapperReconciler) swapNodeLabels(ctx context.Context, oldAw *arbv1.AppWrapper, newAw *arbv1.AppWrapper) {
	allMachines := machinev1beta1.MachineList{}
	errm := r.List(ctx, &allMachines)
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
				r.removeLabelFromMachine(ctx, oldAw, machine.Name)
				r.removeLabelFromNode(oldAw, nodeName)
				r.addLabelToMachine(ctx, newAw, machine.Name)
				r.addLabelToNode(ctx, newAw, nodeName)
			}
		}
	}
}

func (r *AppWrapperReconciler) removeMachineSetLabel(ctx context.Context, aw *arbv1.AppWrapper, machineSetName string) {
	// Retrieve the machineSet object
	machineSet := &machinev1beta1.MachineSet{}
	err := r.Get(ctx, types.NamespacedName{Name: machineSetName, Namespace: namespaceToList}, machineSet)
	if err != nil {
		klog.Infof("Error retrieving MachineSet: %v", err)
		return
	}

	labelPatch := fmt.Sprintf(`[{"op":"remove","path":"/metadata/labels/instascale.codeflare.dev~1%s" }]`, aw.Name)
	patchBytes := []byte(labelPatch)

	// Apply the patch to remove the label
	patch := client.RawPatch(types.JSONPatchType, patchBytes)
	err = r.Patch(ctx, machineSet, patch)
	if err != nil {
		klog.Infof("Error removing label from MachineSet: %v", err)
	}
}

func (r *AppWrapperReconciler) deleteMachineSet(ctx context.Context, aw *arbv1.AppWrapper) {
	allMachineSet := machinev1beta1.MachineSetList{}
	err := r.List(ctx, &allMachineSet)
	if err != nil {
		klog.Infof("Error listing MachineSets: %v", allMachineSet)
	}
	for _, aMachineSet := range allMachineSet.Items {
		if strings.Contains(aMachineSet.Name, aw.Name) {
			klog.Infof("Deleting machineset named %v", aw.Name)
			err := r.Delete(ctx, &aMachineSet)
			if err != nil {
				klog.Infof("Failed to delete machine set: %v", err)
			}
			klog.Infof("Deleted MachineSet: %v", aw.Name)
		}
	}
}
