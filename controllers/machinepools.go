package controllers

import (
	"context"
	"strings"

	cmv1 "github.com/openshift-online/ocm-sdk-go/clustersmgmt/v1"
	arbv1 "github.com/project-codeflare/multi-cluster-app-dispatcher/pkg/apis/controller/v1beta1"

	ctrl "sigs.k8s.io/controller-runtime"
)

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
			if machinePool.InstanceType() == userRequestedInstanceType && hasAwLabel(machinePool.Labels(), aw) {
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
