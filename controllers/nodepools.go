/*
Copyright 2024.

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
	cmv1 "github.com/openshift-online/ocm-sdk-go/clustersmgmt/v1"
	arbv1 "github.com/project-codeflare/multi-cluster-app-dispatcher/pkg/apis/controller/v1beta1"

	ctrl "sigs.k8s.io/controller-runtime"
)

func (r *AppWrapperReconciler) scaleNodePool(ctx context.Context, aw *arbv1.AppWrapper, demandPerInstanceType map[string]int) (ctrl.Result, error) {
	logger := ctrl.LoggerFrom(ctx)
	connection, err := r.createOCMConnection()
	if err != nil {
		logger.Error(err, "Error creating OCM connection")
		return ctrl.Result{}, err
	}
	defer connection.Close()
	for userRequestedInstanceType := range demandPerInstanceType {
		replicas := demandPerInstanceType[userRequestedInstanceType]

		clusterNodePools := connection.ClustersMgmt().V1().Clusters().Cluster(r.ocmClusterID).NodePools()

		response, err := clusterNodePools.List().SendContext(ctx)
		if err != nil {
			return ctrl.Result{}, err
		}

		numberOfMachines := 0
		response.Items().Each(func(nodePool *cmv1.NodePool) bool {
			if nodePool.AWSNodePool().InstanceType() == userRequestedInstanceType && hasAwLabel(nodePool.Labels(), aw) {
				numberOfMachines = nodePool.Replicas()
				return false
			}
			return true
		})

		if numberOfMachines != replicas {
			label := fmt.Sprintf("%s-%s", aw.Name, aw.Namespace)

			m := make(map[string]string)
			m[label] = label

			logger.Info("The instanceRequired array",
				"InstanceRequired", userRequestedInstanceType)

			nodePoolID := r.generateMachineName(ctx, aw.Name)
			nodePool, err := cmv1.NewNodePool().AWSNodePool(cmv1.NewAWSNodePool().InstanceType(userRequestedInstanceType)).ID(nodePoolID).Replicas(replicas).Labels(m).Build()
			if err != nil {
				logger.Error(
					err, "Error building NodePool",
					"userRequestedInstanceType", userRequestedInstanceType,
				)
			}
			logger.Info(
				"Sending NodePool creation request",
				"instanceType", userRequestedInstanceType,
				"nodePoolName", nodePool.ID(),
			)
			response, err := clusterNodePools.Add().Body(nodePool).SendContext(ctx)
			if err != nil {
				logger.Error(err, "Error creating NodePool")
			} else {
				logger.Info(
					"Successfully created NodePool",
					"nodePoolName", nodePool.ID(),
					"response", response,
				)
			}
		}
	}
	return ctrl.Result{Requeue: false}, nil
}

func (r *AppWrapperReconciler) deleteNodePool(ctx context.Context, aw *arbv1.AppWrapper) (ctrl.Result, error) {
	logger := ctrl.LoggerFrom(ctx)
	connection, err := r.createOCMConnection()
	if err != nil {
		logger.Error(err, "Error creating OCM connection")
		return ctrl.Result{}, err
	}
	defer connection.Close()

	nodePoolsConnection := connection.ClustersMgmt().V1().Clusters().Cluster(r.ocmClusterID).NodePools().List()

	nodePoolsListResponse, _ := nodePoolsConnection.Send()
	nodePoolsList := nodePoolsListResponse.Items()
	nodePoolsList.Range(func(index int, item *cmv1.NodePool) bool {
		if hasAwLabel(item.Labels(), aw) {
			id, _ := item.GetID()
			targetNodePool, err := connection.ClustersMgmt().V1().Clusters().Cluster(r.ocmClusterID).NodePools().NodePool(id).Delete().SendContext(ctx)
			if err != nil {
				logger.Error(
					err, "Error deleting nodepool",
					"nodePool", targetNodePool,
				)
			} else {
				logger.Info(
					"Successfully scaled down target nodepool",
					"nodePool", targetNodePool,
				)
			}
		}
		return true
	})
	return ctrl.Result{Requeue: false}, nil
}

func (r *AppWrapperReconciler) checkHypershiftEnabled(ctx context.Context) (bool, error) {
	logger := ctrl.LoggerFrom(ctx)
	connection, err := r.createOCMConnection()
	if err != nil {
		logger.Error(err, "Error creating OCM connection")
		return false, err
	}
	defer connection.Close()

	clusterResource := connection.ClustersMgmt().V1().Clusters().Cluster(r.ocmClusterID)

	response, err := clusterResource.Get().SendContext(ctx)
	if err != nil {
		logger.Error(err, "error fetching cluster details")
		return false, err
	}

	body := response.Body()
	if body == nil {
		logger.Error(err, "Empty resource body when checking Hypershift enabled status")
		return false, err
	}

	hypershiftEnabled := false
	if body.Hypershift() != nil {
		hypershiftEnabled = body.Hypershift().Enabled()
	}

	logger.Info("Checked Hypershift enabled status", "status", hypershiftEnabled)
	return hypershiftEnabled, nil
}
