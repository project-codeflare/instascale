package controllers

import (
	"context"
	"fmt"
	"os"
	"strings"

	ocmsdk "github.com/openshift-online/ocm-sdk-go"
	cmv1 "github.com/openshift-online/ocm-sdk-go/clustersmgmt/v1"
	configv1 "github.com/openshift/api/config/v1"
	arbv1 "github.com/project-codeflare/multi-cluster-app-dispatcher/pkg/apis/controller/v1beta1"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
	ctrl "sigs.k8s.io/controller-runtime"
)

func createOCMConnection() (*ocmsdk.Connection, error) {
	logger, err := ocmsdk.NewGoLoggerBuilder().
		Debug(false).
		Build()
	if err != nil {
		return nil, fmt.Errorf("can't build logger: %v", err)
	}

	connection, err := ocmsdk.NewConnectionBuilder().
		Logger(logger).
		Tokens(ocmToken).
		Build()
	if err != nil {
		return nil, fmt.Errorf("can't build connection: %v", err)
	}

	return connection, nil
}

func scaleMachinePool(aw *arbv1.AppWrapper, userRequestedInstanceType string, replicas int) {
	connection, err := createOCMConnection()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating OCM connection: %v", err)
		return
	}
	defer connection.Close()

	clusterMachinePools := connection.ClustersMgmt().V1().Clusters().Cluster(ocmClusterID).MachinePools()

	m := make(map[string]string)
	m[aw.Name] = aw.Name

	machinePoolID := strings.ReplaceAll(aw.Name+"-"+userRequestedInstanceType, ".", "-")
	createMachinePool, err := cmv1.NewMachinePool().ID(machinePoolID).InstanceType(userRequestedInstanceType).Replicas(replicas).Labels(m).Build()
	if err != nil {
		klog.Errorf(`Error building MachinePool: %v`, err)
	}
	klog.Infof("Built MachinePool with instance type %v and name %v", userRequestedInstanceType, createMachinePool.ID())
	response, err := clusterMachinePools.Add().Body(createMachinePool).SendContext(context.Background())
	if err != nil {
		klog.Errorf(`Error creating MachinePool: %v`, err)
	}
	klog.Infof("Created MachinePool: %v", response)
}

func hasAwLabel(machinePool *cmv1.MachinePool, aw *arbv1.AppWrapper) bool {
	labels := machinePool.Labels()
	for key, value := range labels {
		if key == aw.Name && value == aw.Name {
			return true
		}
	}
	return false
}

func (r *AppWrapperReconciler) scaleMachinePool(ctx context.Context, aw *arbv1.AppWrapper, demandPerInstanceType map[string]int) (ctrl.Result, error) {
	for userRequestedInstanceType := range demandPerInstanceType {
		replicas := demandPerInstanceType[userRequestedInstanceType]
		connection, err := createOCMConnection()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating OCM connection: %v", err)
			return ctrl.Result{}, err
		}
		defer connection.Close()

		clusterMachinePools := connection.ClustersMgmt().V1().Clusters().Cluster(ocmClusterID).MachinePools()

		response, err := clusterMachinePools.List().SendContext(ctx)
		if err != nil {
			klog.Errorf("error retrieving machine pools: %v", err)
		}

		numberOfMachines := 0
		response.Items().Each(func(machinePool *cmv1.MachinePool) bool {
			if machinePool.InstanceType() == userRequestedInstanceType && hasAwLabel(machinePool, aw) {
				numberOfMachines = machinePool.Replicas()
				return false
			}
			return true
		})

		if numberOfMachines != replicas {
			m := make(map[string]string)
			m[aw.Name] = aw.Name
			klog.Infof("The instanceRequired array: %v", userRequestedInstanceType)

			machinePoolID := strings.ReplaceAll(aw.Name+"-"+userRequestedInstanceType, ".", "-")
			createMachinePool, err := cmv1.NewMachinePool().ID(machinePoolID).InstanceType(userRequestedInstanceType).Replicas(replicas).Labels(m).Build()
			if err != nil {
				klog.Errorf(`Error building MachinePool: %v`, err)
			}
			klog.Infof("Built MachinePool with instance type %v and name %v", userRequestedInstanceType, createMachinePool.ID())
			response, err := clusterMachinePools.Add().Body(createMachinePool).SendContext(ctx)
			if err != nil {
				klog.Errorf(`Error creating MachinePool: %v`, err)
			}
			klog.Infof("Created MachinePool: %v", response)
		}
	}
	return ctrl.Result{Requeue: false}, nil
}

func (r *AppWrapperReconciler) deleteMachinePool(ctx context.Context, aw *arbv1.AppWrapper) (ctrl.Result, error) {
	connection, err := createOCMConnection()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating OCM connection: %v", err)
		return ctrl.Result{}, err
	}
	defer connection.Close()

	machinePoolsConnection := connection.ClustersMgmt().V1().Clusters().Cluster(ocmClusterID).MachinePools().List()

	machinePoolsListResponse, _ := machinePoolsConnection.Send()
	machinePoolsList := machinePoolsListResponse.Items()
	machinePoolsList.Range(func(index int, item *cmv1.MachinePool) bool {
		id, _ := item.GetID()
		if strings.Contains(id, aw.Name) {
			targetMachinePool, err := connection.ClustersMgmt().V1().Clusters().Cluster(ocmClusterID).MachinePools().MachinePool(id).Delete().SendContext(ctx)
			if err != nil {
				klog.Infof("Error deleting target machinepool %v", targetMachinePool)
			}
			klog.Infof("Successfully Scaled down target machinepool %v", id)
		}
		return true
	})
	return ctrl.Result{Requeue: false}, nil
}

func machinePoolExists() (bool, error) {
	connection, err := createOCMConnection()
	if err != nil {
		return false, fmt.Errorf("error creating OCM connection: %w", err)
	}
	defer connection.Close()

	machinePools := connection.ClustersMgmt().V1().Clusters().Cluster(ocmClusterID).MachinePools()
	return machinePools != nil, nil
}

// getOCMClusterID determines the internal clusterID to be used for OCM API calls
func (r *AppWrapperReconciler) getOCMClusterID(ctx context.Context) error {
	cv := &configv1.ClusterVersion{}
	err := r.Get(ctx, types.NamespacedName{Name: "version"}, cv)
	if err != nil {
		return fmt.Errorf("can't get clusterversion: %v", err)
	}

	internalClusterID := string(cv.Spec.ClusterID)

	connection, err := createOCMConnection()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating OCM connection: %v", err)
	}
	defer connection.Close()

	// Get the client for the resource that manages the collection of clusters:
	collection := connection.ClustersMgmt().V1().Clusters()

	response, err := collection.List().Search(fmt.Sprintf("external_id = '%s'", internalClusterID)).Size(1).Page(1).SendContext(ctx)
	if err != nil {
		klog.Errorf(`Error getting cluster id: %v`, err)
	}

	response.Items().Each(func(cluster *cmv1.Cluster) bool {
		ocmClusterID = cluster.ID()
		fmt.Printf("%s - %s - %s\n", cluster.ID(), cluster.Name(), cluster.State())
		return true
	})
	return nil
}
