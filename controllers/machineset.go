package controllers

import (
	"context"
	"encoding/json"
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

const (
	timeFiveSeconds   = 5 * time.Second
	timeThirtySeconds = 30 * time.Second
)

func (r *AppWrapperReconciler) reconcileCreateMachineSet(ctx context.Context, aw *arbv1.AppWrapper, demandMapPerInstanceType map[string]int) (ctrl.Result, error) {

	allMachineSet := machinev1beta1.MachineSetList{}
	err := r.List(ctx, &allMachineSet)
	if err != nil {
		klog.Infof("Error listing a machineset, %v", err)
	}

	for userRequestedInstanceType := range demandMapPerInstanceType {
		for _, aMachineSet := range allMachineSet.Items {
			var existingMachine machinev1beta1.MachineSet
			providerConfig, err := ProviderSpecFromRawExtension(aMachineSet.Spec.Template.Spec.ProviderSpec.Value)
			if err != nil {
				klog.Infof("Error retrieving provider config %v", err)
			}
			if userRequestedInstanceType == providerConfig.InstanceType {
				copyOfaMachineSet := aMachineSet.DeepCopy()
				replicas := int32(demandMapPerInstanceType[userRequestedInstanceType])
				copyOfaMachineSet.Spec.Replicas = &replicas
				copyOfaMachineSet.ResourceVersion = ""
				copyOfaMachineSet.Spec.Template.Spec.Taints = []corev1.Taint{{Key: aw.Name, Value: "value1", Effect: "PreferNoSchedule"}}
				copyOfaMachineSet.Name = aw.Name + "-" + aw.Namespace + "-" + userRequestedInstanceType
				copyOfaMachineSet.Spec.Template.Labels = map[string]string{
					aw.Name: aw.Name,
				}
				workerLabels := map[string]string{
					aw.Name: aw.Name,
				}
				copyOfaMachineSet.Spec.Selector = metav1.LabelSelector{
					MatchLabels: workerLabels,
				}
				copyOfaMachineSet.Labels["instascale.codeflare.dev-aw"] = fmt.Sprintf("%s-%s", aw.Name, aw.Namespace)
				machineSetlabelSelector := labels.SelectorFromSet(labels.Set(map[string]string{
					"instascale.codeflare.dev-aw": fmt.Sprintf("%s-%s", aw.Name, aw.Namespace),
				}))
				listOptions := &client.ListOptions{
					LabelSelector: machineSetlabelSelector,
				}
				existingMs := machinev1beta1.MachineSetList{}
				if err := r.List(ctx, &existingMs, listOptions); err != nil {
					return ctrl.Result{}, err
				}

				for _, machineSet := range existingMs.Items {
					if machineSet.Name == fmt.Sprintf("%s-%s-%s", aw.Name, aw.Namespace, userRequestedInstanceType) {
						existingMachine = machineSet
					}
				}
				if existingMachine.Name != fmt.Sprintf("%s-%s-%s", aw.Name, aw.Namespace, userRequestedInstanceType) {
					if err := r.Create(ctx, copyOfaMachineSet); err != nil {
						return ctrl.Result{RequeueAfter: timeFiveSeconds}, err
					} else {
						klog.Infof("Created MachineSet: %v", copyOfaMachineSet.Name)
						return ctrl.Result{RequeueAfter: timeFiveSeconds}, nil
					}
				}

				//wait until all replicas are available
				if (replicas - existingMachine.Status.AvailableReplicas) != 0 {
					klog.Infof("waiting for machines to be in state Ready. replicas needed: %v and replicas available: %v", replicas, copyOfaMachineSet.Status.AvailableReplicas)
					klog.Infof("Querying machineset %v to get updated replicas", copyOfaMachineSet.Name)
					return ctrl.Result{Requeue: true, RequeueAfter: timeThirtySeconds}, nil
				}
				allMachines := machinev1beta1.MachineList{}
				err = r.List(ctx, &allMachines)
				if err != nil {
					klog.Infof("Error listing machines: %v", err)
				}
				//map machines to machinesets?
				//for non-reuse case labels can be added directly to machineset: https://github.com/openshift/machine-api-operator/issues/1077
				err = r.List(ctx, &allMachines, listOptions)
				if err != nil {
					klog.Infof("Error listing machines: %v", err)
				}
				for idx := range allMachines.Items {
					machine := &allMachines.Items[idx]
					nodeName := machine.Status.NodeRef.Name
					labelPatch := fmt.Sprintf(`[{"op":"add","path":"/metadata/labels/%s-%s","value":"%s-%s" }]`, aw.Name, aw.Namespace, aw.Name, aw.Namespace)
					ms, err := r.kubeClient.CoreV1().Nodes().Patch(ctx, nodeName, types.JSONPatchType, []byte(labelPatch), metav1.PatchOptions{})
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
	label := fmt.Sprintf("%s-%s", aw.Name, aw.Namespace)
	labelSelector := labels.SelectorFromSet(labels.Set(map[string]string{
		label:                                label,
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
	isScaled := false
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
			label := fmt.Sprintf("%s-%s", aw.Name, aw.Namespace)
			labelSelector := labels.SelectorFromSet(labels.Set(map[string]string{
				label:                                label,
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
					if k == fmt.Sprintf("instascale.codeflare.dev-%s-%s", aw.Name, aw.Namespace) {
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
							if err := r.addLabelToMachine(ctx, aw, machine.Name); err != nil {
								return ctrl.Result{}, err
							}
							if err := r.addLabelToNode(ctx, aw, nodeName); err != nil {
								return ctrl.Result{}, err
							}
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
					copyOfaMachineSet.Labels[fmt.Sprintf("instascale.codeflare.dev-%s-%s", aw.Name, aw.Namespace)] = strconv.FormatInt(int64(replicas), 10)

					// If the MachineSet's requested replica count label is not equal to the requested replica count then the machineSet must be updated
					if aMachineSet.Labels[fmt.Sprintf("instascale.codeflare.dev-%s-%s", aw.Name, aw.Namespace)] != strconv.FormatInt(int64(replicas), 10) {
						if strconv.FormatInt(int64(replicas), 10) < aMachineSet.Labels[fmt.Sprintf("instascale.codeflare.dev-%s-%s", aw.Name, aw.Namespace)] {
							if _, err := r.removeMachinesBasedOnReplicas(ctx, aw, userRequestedInstanceType, int(replicas)); err != nil {
								return ctrl.Result{}, err
							}
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
						return ctrl.Result{Requeue: true, RequeueAfter: timeThirtySeconds}, nil
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
	nodes, _ := r.kubeClient.CoreV1().Nodes().List(ctx, *listOptions)

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
								updateMsReplicas.Labels[fmt.Sprintf("instascale.codeflare.dev-%s-%s", aw.Name, aw.Namespace)] = strconv.FormatInt(int64(replicas), 10)

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

func (r *AppWrapperReconciler) annotateToDeleteMachine(ctx context.Context, aw *arbv1.AppWrapper) error {
	// We get a list of Nodes with the AppWrapper name and namespace
	label := fmt.Sprintf("%s-%s", aw.Name, aw.Namespace)
	labelSelector := labels.SelectorFromSet(labels.Set(map[string]string{
		label: label,
	}))
	listOptions := &metav1.ListOptions{
		LabelSelector: labelSelector.String(),
	}
	awLabel := fmt.Sprintf("instascale.codeflare.dev-%s-%s", aw.Name, aw.Namespace)
	// List nodes with the AppWrapper name & namespace
	nodes, _ := r.kubeClient.CoreV1().Nodes().List(ctx, *listOptions)

	for _, node := range nodes.Items {
		// Get the machines associated with each node and annotate them for deletion
		value, ok := node.Annotations["machine.openshift.io/machine"]
		if ok {
			machineName := strings.Split(value, "/")

			allMachines := machinev1beta1.MachineList{}
			errm := r.List(ctx, &allMachines)
			if errm != nil {
				klog.Infof("Error listing machines: %v", errm)
				return errm
			}

			for _, aMachine := range allMachines.Items {
				if aMachine.Name == machineName[1] {
					machinePhase := *aMachine.Status.Phase
					if machinePhase != "Deleting" {
						updateMachine := aMachine.DeepCopy()
						klog.Infof("The machine name to be annotated %v", aMachine.Name)
						klog.Infof("Filtered node name is %v", node.Name)
						updateMachine.Annotations["machine.openshift.io/cluster-api-delete-machine"] = "true"
						if err := r.Update(ctx, updateMachine); err != nil {
							return err
						}
					}
				}
			}
		}
	}
	machineSets := machinev1beta1.MachineSetList{}
	if err := r.List(ctx, &machineSets, client.HasLabels{awLabel}); err != nil {
		return err
	}
	var replicaCount int32
	for _, aMachineSet := range machineSets.Items {
		klog.Infof("Machineset to update is %v", aMachineSet.Name)
		value, ok := aMachineSet.Labels[awLabel]
		if ok {
			intValue, err := strconv.Atoi(value)
			if err != nil {
				return err
			}
			replicaCount = int32(intValue)
		}
		copyOfaMachineSet := aMachineSet.DeepCopy()
		existingReplicas := int32(*copyOfaMachineSet.Spec.Replicas)
		totalReplicas := existingReplicas - replicaCount

		copyOfaMachineSet.Spec.Replicas = &totalReplicas

		if err := r.Update(ctx, copyOfaMachineSet); err != nil {
			return err
		}

		if err := r.removeMachineSetLabel(ctx, aw, aMachineSet.Name); err != nil {
			return err
		}
	}
	return nil
}

func (r *AppWrapperReconciler) patchMachineLabels(ctx context.Context, oldAw *arbv1.AppWrapper, newAw *arbv1.AppWrapper, machineName string) error {
	// Retrieve the machine object
	machine := &machinev1beta1.Machine{}
	err := r.Get(ctx, types.NamespacedName{Name: machineName, Namespace: namespaceToList}, machine)
	if err != nil {
		klog.Infof("Error retrieving machine: %v", err)
		return err
	}
	patchOps := []map[string]interface{}{
		{
			"op":    "remove",
			"path":  fmt.Sprintf("/metadata/labels/%s-%s", oldAw.Name, oldAw.Namespace),
			"value": oldAw.Name,
		},
		{
			"op":    "add",
			"path":  fmt.Sprintf("/metadata/labels/%s-%s", newAw.Name, newAw.Namespace),
			"value": newAw.Name,
		},
	}

	patchBytes, err := json.Marshal(patchOps)
	if err != nil {
		return err
	}

	// Apply the patch to remove the old label and add the new one
	err = r.Patch(ctx, machine, client.RawPatch(types.JSONPatchType, patchBytes))
	if err != nil {
		klog.Infof("Error removing label from machine: %v", err)
		return err
	}
	return nil
}

func (r *AppWrapperReconciler) patchNodeLabels(ctx context.Context, oldAw *arbv1.AppWrapper, newAw *arbv1.AppWrapper, nodeName string) error {
	// Patch Operations for adding and deleting
	patchOps := []map[string]interface{}{
		{
			"op":    "remove",
			"path":  fmt.Sprintf("/metadata/labels/%s-%s", oldAw.Name, oldAw.Namespace),
			"value": oldAw.Name,
		},
		{
			"op":    "add",
			"path":  fmt.Sprintf("/metadata/labels/%s-%s", newAw.Name, oldAw.Namespace),
			"value": newAw.Name,
		},
	}

	patchBytes, err := json.Marshal(patchOps)
	if err != nil {
		return err
	}

	_, err = r.kubeClient.CoreV1().Nodes().Patch(ctx, nodeName, types.JSONPatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		klog.Infof("Error deleted label to machines %v", err)
		return err
	}

	return nil
}

func (r *AppWrapperReconciler) addLabelToMachine(ctx context.Context, aw *arbv1.AppWrapper, machineName string) error {
	labelPatch := fmt.Sprintf(`[{"op":"add","path":"/metadata/labels/%s-%s","value":"%s-%s" }]`, aw.Name, aw.Namespace, aw.Name, aw.Namespace)
	patchBytes := []byte(labelPatch)

	// Retrieve the machine object
	machine := &machinev1beta1.Machine{}
	err := r.Get(ctx, types.NamespacedName{Name: machineName, Namespace: namespaceToList}, machine)
	if err != nil {
		klog.Infof("Error retrieving machine: %v", err)
		return err
	}

	// Apply the patch to add the label
	patch := client.RawPatch(types.JSONPatchType, patchBytes)
	err = r.Patch(ctx, machine, patch)
	if err != nil {
		klog.Infof("Error adding label to machine: %v", err)
		return err
	}
	return nil
}

func (r *AppWrapperReconciler) addLabelToNode(ctx context.Context, aw *arbv1.AppWrapper, nodeName string) error {
	labelPatch := fmt.Sprintf(`[{"op":"add","path":"/metadata/labels/%s-%s","value":"%s-%s" }]`, aw.Name, aw.Namespace, aw.Name, aw.Namespace)
	_, err := r.kubeClient.CoreV1().Nodes().Patch(ctx, nodeName, types.JSONPatchType, []byte(labelPatch), metav1.PatchOptions{})
	if err != nil {
		klog.Infof("Error adding label to machine %v", err)
		return err
	}
	return nil
}

// add logic to swap out labels with new appwrapper label
func (r *AppWrapperReconciler) swapNodeLabels(ctx context.Context, oldAw *arbv1.AppWrapper, newAw *arbv1.AppWrapper) error {
	labelSelector := labels.SelectorFromSet(labels.Set(map[string]string{
		oldAw.Name: oldAw.Name,
	}))
	listOptions := &client.ListOptions{
		LabelSelector: labelSelector,
	}
	allMachines := machinev1beta1.MachineList{}
	errm := r.List(ctx, &allMachines, listOptions)
	if errm != nil {
		klog.Infof("Error listing machineset: %v", errm)
		return errm
	}
	//klog.Infof("Got all machines %v", allMachines)
	for _, machine := range allMachines.Items {
		nodeName := machine.Status.NodeRef.Name
		klog.Infof("removing label from node %v that belonged to appwrapper %v and adding node to new appwrapper %v", nodeName, oldAw.Name, newAw.Name)
		if err := r.patchMachineLabels(ctx, oldAw, newAw, machine.Name); err != nil {
			return err
		}

		if err := r.patchNodeLabels(ctx, oldAw, newAw, nodeName); err != nil {
			return err
		}
	}
	return nil
}

func (r *AppWrapperReconciler) removeMachineSetLabel(ctx context.Context, aw *arbv1.AppWrapper, machineSetName string) error {
	// Retrieve the machineSet object
	machineSet := &machinev1beta1.MachineSet{}
	err := r.Get(ctx, types.NamespacedName{Name: machineSetName, Namespace: namespaceToList}, machineSet)
	if err != nil {
		klog.Infof("Error retrieving MachineSet: %v", err)
		return err
	}

	var labelValue string
	value, ok := machineSet.Labels[fmt.Sprintf("instascale.codeflare.dev-%s-%s", aw.Name, aw.Namespace)]
	if ok {
		labelValue = value
	}

	labelPatch := fmt.Sprintf(`[{"op":"remove","path":"/metadata/labels/instascale.codeflare.dev-%s-%s","value":"%s"}]`, aw.Name, aw.Namespace, labelValue)
	patchBytes := []byte(labelPatch)

	// Apply the patch to remove the label
	patch := client.RawPatch(types.JSONPatchType, patchBytes)
	err = r.Patch(ctx, machineSet, patch)
	if err != nil {
		klog.Infof("Error removing label from MachineSet: %v", err)
		return err
	}
	return nil
}

func (r *AppWrapperReconciler) deleteMachineSet(ctx context.Context, aw *arbv1.AppWrapper) error {
	labelSelector := labels.SelectorFromSet(labels.Set(map[string]string{
		"instascale.codeflare.dev-aw": fmt.Sprintf("%s-%s", aw.Name, aw.Namespace),
	}))
	listOptions := &client.ListOptions{
		LabelSelector: labelSelector,
	}

	allMachineSet := machinev1beta1.MachineSetList{}
	err := r.List(ctx, &allMachineSet, listOptions)
	if err != nil {
		klog.Infof("Error listing MachineSets: %v", err)
		return err
	}
	for _, aMachineSet := range allMachineSet.Items {
		klog.Infof("Deleting machineset named %v", aMachineSet.Name)
		err := r.Delete(ctx, &aMachineSet)
		if err != nil {
			klog.Infof("Failed to delete machine set: %v", err)
			return err
		}
	}
	return nil
}
