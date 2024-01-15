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
	"encoding/json"
	"fmt"

	machinev1 "github.com/openshift/api/machine/v1beta1"
	arbv1 "github.com/project-codeflare/multi-cluster-app-dispatcher/pkg/apis/controller/v1beta1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"

	"math/rand"
	"strings"
	"time"
)

func getInstanceRequired(labels map[string]string) []string {
	if value, exists := labels["orderedinstance"]; exists {
		return strings.Split(value, "_")
	}
	return []string{}
}

func (r *AppWrapperReconciler) generateMachineName(ctx context.Context, awName string) string {

	logger := ctrl.LoggerFrom(ctx)
	const randomSuffixLength = 4
	maxBaseNameLength := r.machineNameCharacterLimit - randomSuffixLength - 1

	// Truncate the base name if it exceeds the maximum length.
	if len(awName) > maxBaseNameLength {
		truncatedName := awName[:maxBaseNameLength]

		logger.Info(
			"instance name exceeds character limit",
			"limit", r.machineNameCharacterLimit,
			"truncatedName", truncatedName,
		)

		awName = truncatedName
	}

	return fmt.Sprintf("%s-%04x", awName, rand.Intn(1<<16))
}

func resyncPeriod() func() time.Duration {
	return func() time.Duration {
		factor := rand.Float64() + 1
		return time.Duration(float64(minResyncPeriod.Nanoseconds()) * factor)
	}
}

func containsInsufficientCondition(allconditions []arbv1.AppWrapperCondition) bool {
	for _, condition := range allconditions {
		if strings.Contains(condition.Message, "Insufficient") {
			return true
		}
	}
	return false
}

// ProviderSpecFromRawExtension unmarshals the JSON-encoded spec
func ProviderSpecFromRawExtension(ctx context.Context, rawExtension *runtime.RawExtension) (*machinev1.AWSMachineProviderConfig, error) {
	logger := ctrl.LoggerFrom(ctx)
	if rawExtension == nil {
		return &machinev1.AWSMachineProviderConfig{}, nil
	}

	spec := new(machinev1.AWSMachineProviderConfig)
	if err := json.Unmarshal(rawExtension.Raw, &spec); err != nil {
		return nil, fmt.Errorf("error unmarshalling providerSpec: %v", err)
	}

	logger.V(5).Info(
		"Got provider spec from raw extension",
		"awsMachineProviderConfig", spec,
	)
	return spec, nil
}

func contains(s []string, str string) bool {
	for _, v := range s {
		if v == str {
			return true
		}
	}

	return false
}

func hasAwLabel(labels map[string]string, aw *arbv1.AppWrapper) bool {
	label := fmt.Sprintf("%s-%s", aw.Name, aw.Namespace)
	value, ok := labels[label]
	return ok && value == label
}
