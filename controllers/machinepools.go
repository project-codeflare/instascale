package controllers

import (
	"context"
	"fmt"
	ocmsdk "github.com/openshift-online/ocm-sdk-go"
	cmv1 "github.com/openshift-online/ocm-sdk-go/clustersmgmt/v1"
	"github.com/openshift-online/ocm-sdk-go/logging"
	configv1 "github.com/openshift/api/config/v1"
	arbv1 "github.com/project-codeflare/multi-cluster-app-dispatcher/pkg/apis/controller/v1beta1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
	"strings"
)

type OCMService interface {
	scaleMachinePool(aw *arbv1.AppWrapper, userRequestedInstanceType string, replicas int) error
	deleteMachinePool(aw *arbv1.AppWrapper) error
	machinePoolExists(aw *arbv1.AppWrapper) (bool, error)
	getOCMClusterID(r *AppWrapperReconciler) error
}


type OCMServiceImp struct {
	OcmToken     string
	OcmClusterID string
}

func (o *OCMServiceImp) scaleMachinePool(aw *arbv1.AppWrapper, userRequestedInstanceType string, replicas int) error {
	logger, err := ocmsdk.NewGoLoggerBuilder().
		Debug(false).
		Build()
	if err != nil {
		return fmt.Errorf("can't build logger: %v", err)
	}

	// Create the connection, and remember to close it:
	connection, err := ocmsdk.NewConnectionBuilder().
		Logger(logger).
		Tokens(ocmToken).
		Build()
	if err != nil {
		return fmt.Errorf("can't build connection: %v", err)
	}
	defer connection.Close()

	clusterMachinePools := connection.ClustersMgmt().V1().Clusters().Cluster(ocmClusterID).MachinePools()

	m := make(map[string]string)
	m[aw.Name] = aw.Name

	machinePoolID := strings.ReplaceAll(aw.Name+"-"+userRequestedInstanceType, ".", "-")
	createMachinePool, _ := cmv1.NewMachinePool().ID(machinePoolID).InstanceType(userRequestedInstanceType).Replicas(replicas).Labels(m).Build()

	klog.Infof("Create machinepool with instance type %v and name %v", userRequestedInstanceType, createMachinePool.ID())
	response, err := clusterMachinePools.Add().Body(createMachinePool).SendContext(context.Background())
	if err != nil {
		return fmt.Errorf("failed to add machine pool: %v", err)
	}
	if response.Error() != nil {
		return fmt.Errorf("error from server: %s", response.Error())
	}

	return nil
}

func (o *OCMServiceImp) deleteMachinePool(aw *arbv1.AppWrapper) error {
	logger, err := ocmsdk.NewGoLoggerBuilder().
		Debug(false).
		Build()
	if err != nil {
		return fmt.Errorf("can't build logger: %v", err)
	}
	connection, err := ocmsdk.NewConnectionBuilder().
		Logger(logger).
		Tokens(ocmToken).
		Build()
	if err != nil {
		return fmt.Errorf("can't build connection: %v", err)
	}
	defer connection.Close()
	machinePoolsConnection := connection.ClustersMgmt().V1().Clusters().Cluster(ocmClusterID).MachinePools().List()

	machinePoolsListResponse, err := machinePoolsConnection.Send()
	if err != nil {
		return fmt.Errorf("failed to send machine pools list request: %v", err)
	}
	if machinePoolsListResponse.Error() != nil {
		return fmt.Errorf("error from server: %s", machinePoolsListResponse.Error())
	}
	machinePoolsList := machinePoolsListResponse.Items()
	machinePoolsList.Range(func(index int, item *cmv1.MachinePool) bool {
		id, _ := item.GetID()
		if strings.Contains(id, aw.Name) {
			targetMachinePool, err := connection.ClustersMgmt().V1().Clusters().Cluster(ocmClusterID).MachinePools().MachinePool(id).Delete().SendContext(context.Background())
			if err != nil {
				klog.Infof("Error deleting target machinepool %v", targetMachinePool)
				return false
			}
		}
		return true
	})
	return nil
}

// Check if machine pools exist
func (o *OCMServiceImp) machinePoolExists(aw *arbv1.AppWrapper) (bool, error) {
	logger, err := ocmsdk.NewGoLoggerBuilder().
		Debug(false).
		Build()
	if err != nil {
		return false, fmt.Errorf("can't build logger: %v", err)
	}
	connection, err := ocmsdk.NewConnectionBuilder().
		Logger(logger).
		Tokens(ocmToken).
		Build()
	if err != nil {
		return false, fmt.Errorf("can't build connection: %v", err)
	}
	defer connection.Close()
	
	machinePoolsConnection := connection.ClustersMgmt().V1().Clusters().Cluster(ocmClusterID).MachinePools().List()
	machinePoolsListResponse, err := machinePoolsConnection.Send()
	if err != nil {
		return false, fmt.Errorf("failed to send machine pools list request: %v", err)
	}
	if machinePoolsListResponse.Error() != nil {
		return false, fmt.Errorf("error from server: %s", machinePoolsListResponse.Error())
	}
	machinePoolsList := machinePoolsListResponse.Items()

	exists := false
	machinePoolsList.Range(func(index int, item *cmv1.MachinePool) bool {
		id, _ := item.GetID()
		if strings.Contains(id, aw.Name) {
			exists = true
			return false // exit Range function 		
		}
		return true 
	})

	return exists, nil
}

// getOCMClusterID determines the internal clusterID to be used for OCM API calls
func (o *OCMServiceImp) getOCMClusterID(r *AppWrapperReconciler) error {
	cv := &configv1.ClusterVersion{}
	err := r.Get(context.TODO(), types.NamespacedName{Name: "version"}, cv)
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
		return fmt.Errorf("can't get clusterversion: %v", err)
	}

	connection, err := ocmsdk.NewConnectionBuilder().
		Logger(logger).
		Tokens(ocmToken).
		Build()
	if err != nil {
		return fmt.Errorf("can't build connection: %v", err)
	}
	defer connection.Close()

	// Get the client for the resource that manages the collection of clusters:
	collection := connection.ClustersMgmt().V1().Clusters()

	response, err := collection.List().Search(fmt.Sprintf("external_id = '%s'", internalClusterID)).Size(1).Page(1).SendContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to send cluster list request: %v", err)
	}
	if response.Error() != nil {
		return fmt.Errorf("error from server: %s", response.Error())
	}

	response.Items().Each(func(cluster *cmv1.Cluster) bool {
		ocmClusterID = cluster.ID()
		fmt.Printf("%s - %s - %s\n", cluster.ID(), cluster.Name(), cluster.State())
		return true
	})
	return nil
}
