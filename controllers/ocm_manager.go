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
	ocmsdk "github.com/openshift-online/ocm-sdk-go"
	cmv1 "github.com/openshift-online/ocm-sdk-go/clustersmgmt/v1"
	configv1 "github.com/openshift/api/config/v1"
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

	r.ocmConnection = connection
	return r.ocmConnection, nil
}

// getOCMClusterID determines the internal clusterID to be used for OCM API calls
func (r *AppWrapperReconciler) getOCMClusterID(ctx context.Context) error {
	logger := ctrl.LoggerFrom(ctx)
	cv := &configv1.ClusterVersion{}
	err := r.Get(ctx, types.NamespacedName{Name: "version"}, cv)
	if err != nil {
		logger.Error(err, "can't get clusterversion")
		return err
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

func (r *AppWrapperReconciler) Close() {
	if r.ocmConnection != nil {
		r.ocmConnection.Close()
	}
}
