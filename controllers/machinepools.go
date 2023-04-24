package controllers

import (
	"context"
	"fmt"
	"os"
	"strings"

	ocmsdk "github.com/openshift-online/ocm-sdk-go"
	cmv1 "github.com/openshift-online/ocm-sdk-go/clustersmgmt/v1"
	v1 "github.com/openshift-online/ocm-sdk-go/clustersmgmt/v1"
	"github.com/openshift-online/ocm-sdk-go/logging"
	configv1 "github.com/openshift/api/config/v1"
	arbv1 "github.com/project-codeflare/multi-cluster-app-dispatcher/pkg/apis/controller/v1beta1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
)

func scaleMachinePool(aw *arbv1.AppWrapper, userRequestedInstanceType string, replicas int) {
	logger, err := ocmsdk.NewGoLoggerBuilder().
		Debug(false).
		Build()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Can't build logger: %v\n", err)
		os.Exit(1)
	}

	// Create the connection, and remember to close it:
	connection, err := ocmsdk.NewConnectionBuilder().
		Logger(logger).
		Tokens(ocmToken).
		Build()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Can't build connection: %v\n", err)
		os.Exit(1)
	}
	defer connection.Close()

	clusterMachinePools := connection.ClustersMgmt().V1().Clusters().Cluster(ocmClusterID).MachinePools()

	m := make(map[string]string)
	m[aw.Name] = aw.Name

	machinePoolID := strings.ReplaceAll(aw.Name+"-"+userRequestedInstanceType, ".", "-")
	createMachinePool, _ := cmv1.NewMachinePool().ID(machinePoolID).InstanceType(userRequestedInstanceType).Replicas(replicas).Labels(m).Build()

	klog.Infof("Create machinepool with instance type %v and name %v", userRequestedInstanceType, createMachinePool.ID())
	clusterMachinePools.Add().Body(createMachinePool).SendContext(context.Background())
}

func deleteMachinePool(aw *arbv1.AppWrapper) {

	logger, err := ocmsdk.NewGoLoggerBuilder().
		Debug(false).
		Build()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Can't build logger: %v\n", err)
		os.Exit(1)
	}
	connection, err := ocmsdk.NewConnectionBuilder().
		Logger(logger).
		Tokens(ocmToken).
		Build()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Can't build connection: %v\n", err)
		os.Exit(1)
	}
	defer connection.Close()
	machinePoolsConnection := connection.ClustersMgmt().V1().Clusters().Cluster(ocmClusterID).MachinePools().List()

	machinePoolsListResponse, _ := machinePoolsConnection.Send()
	machinePoolsList := machinePoolsListResponse.Items()

	if reuse {
		matchedAw := findExactMatch(aw)
		if matchedAw != nil {
			klog.Infof("Appwrapper %s deleted, swapping machines to %s", aw.Name, matchedAw.Name)
			//implement a seperate method to swap labels and add taints
			//swapMachinepoolLabels(aw, matchedAw, machinePoolsList)
			machinePoolsList.Range(func(index int, item *cmv1.MachinePool) bool {
				fmt.Println(item.GetID())
				id, _ := item.GetID()
				if id == aw.Name {
					targetMachinePool := connection.ClustersMgmt().V1().Clusters().Cluster(ocmClusterID).MachinePools()
					updatePoolBuilder := cmv1.NewMachinePool().ID(id)
					m := make(map[string]string)
					m[id] = id
					taintBuilders := []*cmv1.TaintBuilder{}
					taintBuilders = append(taintBuilders, cmv1.NewTaint().Key(matchedAw.Name).Value(matchedAw.Name).Effect("PreferNoSchedule"))
					updatePoolBuilder = updatePoolBuilder.Labels(m).Taints(taintBuilders...)
					machinePool, errBuild := updatePoolBuilder.Build()
					if errBuild != nil {
						klog.Infof("Error building machinepool build %v", errBuild)
					}
					_, err := targetMachinePool.MachinePool(id).Update().Body(machinePool).SendContext(context.Background())
					if err != nil {
						klog.Errorf("Error updating the cluster: %v\n", err)
						//should we crash? or return false?
					}

				}
				return true
			})
		} else {
			klog.Infof("Appwrapper %s deleted, scaling down machines", aw.Name)
			machinePoolsList.Range(func(index int, item *cmv1.MachinePool) bool {
				id, _ := item.GetID()
				if strings.Contains(id, aw.Name) {
					targetMachinePool, err := connection.ClustersMgmt().V1().Clusters().Cluster(ocmClusterID).MachinePools().MachinePool(id).Delete().SendContext(context.Background())
					if err != nil {
						klog.Infof("Error deleting target machinepool %v", targetMachinePool)
					}
				}
				return true
			})
		}
	}

}

// getOCMClusterID determines the internal clusterID to be used for OCM API calls
func getOCMClusterID(r *AppWrapperReconciler) error {

	cv := &configv1.ClusterVersion{}
	err := r.Client.Get(context.TODO(), types.NamespacedName{Name: "version"}, cv)
	if err != nil {
		return fmt.Errorf("can't get clusterversion: %v", err)
	}

	internalClusterID := string(cv.Spec.ClusterID)

	ctx := context.Background()

	// Create a logger that has the debug level enabled:
	logger, err := logging.NewGoLoggerBuilder().
		Debug(false).
		Build()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Can't build logger: %v\n", err)
		os.Exit(1)
	}

	connection, err := ocmsdk.NewConnectionBuilder().
		Logger(logger).
		Tokens(ocmToken).
		Build()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Can't build connection: %v\n", err)
		os.Exit(1)
	}
	defer connection.Close()

	// Get the client for the resource that manages the collection of clusters:
	collection := connection.ClustersMgmt().V1().Clusters()

	response, _ := collection.List().Search(fmt.Sprintf("external_id = '%s'", internalClusterID)).Size(1).Page(1).SendContext(ctx)

	response.Items().Each(func(cluster *cmv1.Cluster) bool {
		ocmClusterID = cluster.ID()
		fmt.Printf("%s - %s - %s\n", cluster.ID(), cluster.Name(), cluster.State())
		return true
	})
	return nil
}

func swapMachinepoolLabels(aw *arbv1.AppWrapper, matchedAw *arbv1.AppWrapper, machinePoollist *cmv1.MachinePoolList) {

	machinePoollist.Range(func(index int, item *v1.MachinePool) bool {
		fmt.Println(item.GetID())
		id, _ := item.GetID()
		if id == "test-mp" {
			targetMachinePool := connection.ClustersMgmt().V1().Clusters().Cluster(ocmClusterID).MachinePools()
			// targetMachinePool, _ := targetMachinePool.MachinePool(id).Get().SendContext(context.Background())

			//updatePoolBuilder := v1.NewMachinePool().Copy(item)
			updatePoolBuilder := v1.NewMachinePool().ID(id)
			m := make(map[string]string)
			m["update2"] = "update2"
			//updatepool, error1 := updatePoolBuilder.Labels(m).InstanceType("").Build()
			updatePoolBuilder = updatePoolBuilder.Labels(m)
			machinePool, error1 := updatePoolBuilder.Build()
			if error1 != nil {
				klog.Infof("The error is %v", error1)
			}
			_, err := targetMachinePool.MachinePool(id).Update().Body(machinePool).SendContext(context.Background())
			if err != nil {
				fmt.Fprintf(os.Stderr, "Can't update cluster: %v\n", err)
				os.Exit(1)
			}

		}
		return true
	})
}
