package controllers

import (
	"context"
	"fmt"
	"strings"
	"time"

	arbv1 "github.com/project-codeflare/multi-cluster-app-dispatcher/pkg/apis/controller/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
)

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
				klog.Infof("Existing machines owned in %s are %v", copyOfaMachineSet.Name, existingMachinesOwned)

				//copyOfaMachineSet.ResourceVersion = ""
				ms, err := machineClient.MachineV1beta1().MachineSets(namespaceToList).Update(context.Background(), copyOfaMachineSet, metav1.UpdateOptions{})
				if err != nil {
					klog.Infof("Error updating machineset %v", err)
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
					klog.Infof("Error listing machineset: %v", errm)
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

func canScaleMachineset(demandPerInstanceType map[string]int) bool {
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
