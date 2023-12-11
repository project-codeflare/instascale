package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"time"

	machinev1 "github.com/openshift/api/machine/v1beta1"
	arbv1 "github.com/project-codeflare/multi-cluster-app-dispatcher/pkg/apis/controller/v1beta1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
)

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
