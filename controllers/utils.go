package controllers

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"time"

	machinev1 "github.com/openshift/api/machine/v1beta1"
	mapiclientset "github.com/openshift/client-go/machine/clientset/versioned"
	arbv1 "github.com/project-codeflare/multi-cluster-app-dispatcher/pkg/apis/controller/v1beta1"
	clusterstateapi "github.com/project-codeflare/multi-cluster-app-dispatcher/pkg/controller/clusterstate/api"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog"
)

// ClientBuilder can create a variety of kubernetes client interface
// with its embeded rest.Config.
type ClientBuilder struct {
	config *rest.Config
}

func getPodResourcesWithReplicas(pod arbv1.CustomPodResourceTemplate) (resource *clusterstateapi.Resource, count int) {
	replicas := pod.Replicas
	req := clusterstateapi.NewResource(pod.Requests)
	limit := clusterstateapi.NewResource(pod.Limits)
	tolerance := 0.001

	// Use limit if request is 0
	if diff := math.Abs(req.MilliCPU - float64(0.0)); diff < tolerance {
		req.MilliCPU = limit.MilliCPU
	}

	if diff := math.Abs(req.Memory - float64(0.0)); diff < tolerance {
		req.Memory = limit.Memory
	}

	if req.GPU <= 0 {
		req.GPU = limit.GPU
	}

	return req, replicas
}

// MachineClientOrDie returns the machine api client interface for machine api objects.
func (cb *ClientBuilder) MachineClientOrDie(name string) mapiclientset.Interface {
	return mapiclientset.NewForConfigOrDie(rest.AddUserAgent(cb.config, name))
}

func resyncPeriod() func() time.Duration {
	return func() time.Duration {
		factor := rand.Float64() + 1
		return time.Duration(float64(minResyncPeriod.Nanoseconds()) * factor)
	}
}

// ProviderSpecFromRawExtension unmarshals the JSON-encoded spec
func ProviderSpecFromRawExtension(rawExtension *runtime.RawExtension) (*machinev1.AWSMachineProviderConfig, error) {
	if rawExtension == nil {
		return &machinev1.AWSMachineProviderConfig{}, nil
	}

	spec := new(machinev1.AWSMachineProviderConfig)
	if err := json.Unmarshal(rawExtension.Raw, &spec); err != nil {
		return nil, fmt.Errorf("error unmarshalling providerSpec: %v", err)
	}

	klog.V(5).Infof("Got provider spec from raw extension: %+v", spec)
	return spec, nil
}

// NewClientBuilder returns a *ClientBuilder with the given kubeconfig.
func NewClientBuilder(kubeconfig string) (*ClientBuilder, error) {
	config, err := getRestConfig(kubeconfig)
	if err != nil {
		return nil, err
	}

	return &ClientBuilder{
		config: config,
	}, nil
}

//This function is changed from genericresource.go file as we force to look for resources under custompodresources.
func GetListOfPodResourcesFromOneGenericItem(awr *arbv1.AppWrapperGenericResource) (resource []*clusterstateapi.Resource, er error) {
	var podResourcesList []*clusterstateapi.Resource

	podTotalresource := clusterstateapi.EmptyResource()
	var replicas int
	var res *clusterstateapi.Resource
	if awr.GenericTemplate.Raw != nil {
		podresources := awr.CustomPodResources
		for _, item := range podresources {
			res, replicas = getPodResourcesWithReplicas(item)
			podTotalresource = podTotalresource.Add(res)
		}
		klog.V(8).Infof("[GetListOfPodResourcesFromOneGenericItem] Requested total allocation resource from 1 pod `%v`.\n", podTotalresource)

		// Addd individual pods to results
		var replicaCount int = int(replicas)
		for i := 0; i < replicaCount; i++ {
			podResourcesList = append(podResourcesList, podTotalresource)
		}
	}

	return podResourcesList, nil
}

func getRestConfig(kubeconfig string) (*rest.Config, error) {
	var config *rest.Config
	var err error
	if kubeconfig != "" {
		klog.V(10).Infof("Loading kube client config from path %q", kubeconfig)
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	} else {
		klog.V(4).Infof("Using in-cluster kube client config")
		config, err = rest.InClusterConfig()
		if err == rest.ErrNotInCluster {
			return nil, errors.New("Not running in-cluster? Try using --kubeconfig")
		}
	}
	return config, err
}

func contains(s []string, str string) bool {
	for _, v := range s {
		if v == str {
			return true
		}
	}

	return false
}
