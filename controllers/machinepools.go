package controllers

import (
	"context"
	"fmt"
	"strings"

	ocmsdk "github.com/openshift-online/ocm-sdk-go"
	cmv1 "github.com/openshift-online/ocm-sdk-go/clustersmgmt/v1"
	configv1 "github.com/openshift/api/config/v1"
	arbv1 "github.com/project-codeflare/multi-cluster-app-dispatcher/pkg/apis/controller/v1beta1"

	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
)

func (r *AppWrapperReconciler) createOCMConnection() (*ocmsdk.Connection, error) {
	logger, err := ocmsdk.NewGoLoggerBuilder().
		Debug(false).
		Build()
	if err != nil {
		return nil, fmt.Errorf("can't build logger: %v", err)
	}

	connection, err := ocmsdk.NewConnectionBuilder().
		Logger(logger).
		Tokens(r.ocmToken).
		Build()
	if err != nil {
		return nil, fmt.Errorf("can't build connection: %v", err)
	}

	return connection, nil
}

func hasAwLabel(machinePool *cmv1.MachinePool, aw *arbv1.AppWrapper) bool {
	value, ok := machinePool.Labels()[aw.Name]
	if ok && value == aw.Name {
		return true
	}
	return false
}

func (r *AppWrapperReconciler) scaleMachinePool(ctx context.Context, aw *arbv1.AppWrapper, demandPerInstanceType map[string]int) (ctrl.Result, error) {
	logger := ctrl.LoggerFrom(ctx)
	connection, err := r.createOCMConnection()
	if err != nil {
		logger.Error(err, "Error creating OCM connection")
		return ctrl.Result{}, err
	}
	defer connection.Close()
	for userRequestedInstanceType := range demandPerInstanceType {
		replicas := demandPerInstanceType[userRequestedInstanceType]

		clusterMachinePools := connection.ClustersMgmt().V1().Clusters().Cluster(r.ocmClusterID).MachinePools()

		response, err := clusterMachinePools.List().SendContext(ctx)
		if err != nil {
			return ctrl.Result{}, err
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

			machinePoolID := strings.ReplaceAll(aw.Name+"-"+userRequestedInstanceType, ".", "-")
			createMachinePool, err := cmv1.NewMachinePool().ID(machinePoolID).InstanceType(userRequestedInstanceType).Replicas(replicas).Labels(m).Build()
			if err != nil {
				logger.Error(
					err, "Error building MachinePool",
					"userRequestedInstanceType", userRequestedInstanceType,
				)
			}
			logger.Info(
				"Sending MachinePool creation request",
				"instanceType", userRequestedInstanceType,
				"machinePoolName", createMachinePool.ID(),
			)
			response, err := clusterMachinePools.Add().Body(createMachinePool).SendContext(ctx)
			if err != nil {
				logger.Error(err, "Error creating MachinePool")
			} else {
				logger.Info(
					"Successfully created MachinePool",
					"machinePoolName", createMachinePool.ID(),
					"response", response,
				)
			}
		}
	}
	return ctrl.Result{Requeue: false}, nil
}

func (r *AppWrapperReconciler) deleteMachinePool(ctx context.Context, aw *arbv1.AppWrapper) (ctrl.Result, error) {
	logger := ctrl.LoggerFrom(ctx)
	connection, err := r.createOCMConnection()
	if err != nil {
		logger.Error(err, "Error creating OCM connection")
		return ctrl.Result{}, err
	}
	defer connection.Close()

	machinePoolsConnection := connection.ClustersMgmt().V1().Clusters().Cluster(r.ocmClusterID).MachinePools().List()

	machinePoolsListResponse, _ := machinePoolsConnection.Send()
	machinePoolsList := machinePoolsListResponse.Items()
	machinePoolsList.Range(func(index int, item *cmv1.MachinePool) bool {
		id, _ := item.GetID()
		if strings.Contains(id, aw.Name) {
			targetMachinePool, err := connection.ClustersMgmt().V1().Clusters().Cluster(r.ocmClusterID).MachinePools().MachinePool(id).Delete().SendContext(ctx)
			if err != nil {
				logger.Error(
					err, "Error deleting machinepool",
					"machinePool", targetMachinePool,
				)
			} else {
				logger.Info(
					"Successfully scaled down target machinepool",
					"machinePool", targetMachinePool,
				)
			}
		}
		return true
	})
	return ctrl.Result{Requeue: false}, nil
}

func (r *AppWrapperReconciler) machinePoolExists() (bool, error) {
	connection, err := r.createOCMConnection()
	if err != nil {
		return false, fmt.Errorf("error creating OCM connection: %w", err)
	}
	defer connection.Close()

	machinePools := connection.ClustersMgmt().V1().Clusters().Cluster(r.ocmClusterID).MachinePools()
	return machinePools != nil, nil
}

// getOCMClusterID determines the internal clusterID to be used for OCM API calls
func (r *AppWrapperReconciler) getOCMClusterID(ctx context.Context) error {
	logger := ctrl.LoggerFrom(ctx)
	cv := &configv1.ClusterVersion{}
	err := r.Get(ctx, types.NamespacedName{Name: "version"}, cv)
	if err != nil {
		return fmt.Errorf("can't get clusterversion: %v", err)
	}

	internalClusterID := string(cv.Spec.ClusterID)

	connection, err := r.createOCMConnection()
	if err != nil {
		logger.Error(err, "Error creating OCM connection")
	}
	defer connection.Close()

	// Get the client for the resource that manages the collection of clusters:
	collection := connection.ClustersMgmt().V1().Clusters()

	response, err := collection.List().Search(fmt.Sprintf("external_id = '%s'", internalClusterID)).Size(1).Page(1).SendContext(ctx)
	if err != nil {
		logger.Error(err, "Error getting cluster id")
	}

	response.Items().Each(func(cluster *cmv1.Cluster) bool {
		r.ocmClusterID = cluster.ID()
		logger.Info(
			"Cluster Info",
			"clusterId", cluster.ID(),
			"clusterName", cluster.Name(),
			"clusterState", cluster.State(),
		)
		return true
	})
	return nil
}
