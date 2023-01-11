package controllers

import (
	"testing"

	arbv1 "github.com/project-codeflare/multi-cluster-app-dispatcher/pkg/apis/controller/v1beta1"
	clusterstateapi "github.com/project-codeflare/multi-cluster-app-dispatcher/pkg/controller/clusterstate/api"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestProviderSpecFromRawExtension(t *testing.T) {
	spec, val := ProviderSpecFromRawExtension(nil)

	if val != nil {
		t.Errorf("got %v, wanted %v for provide %v", val, nil, spec)
	}
}

func TestPodResourcesWithReplicas(t *testing.T) {
	pod := arbv1.CustomPodResourceTemplate{
		Replicas: 0,
		Requests: map[v1.ResourceName]resource.Quantity{},
		Limits:   map[v1.ResourceName]resource.Quantity{},
	}

	req, replicas := getPodResourcesWithReplicas(pod)

	if req.MilliCPU != 0 && req.Memory != 0 && replicas != 0 {
		t.Errorf("got %v, %v, wanted 0,0", req, replicas)
	}

}

func TestGetListOfPodResourcesFromOneGenericItem(t *testing.T) {
	pod := arbv1.CustomPodResourceTemplate{
		Replicas: 0,
		Requests: map[v1.ResourceName]resource.Quantity{},
		Limits:   map[v1.ResourceName]resource.Quantity{},
	}

	podTotalresource := clusterstateapi.EmptyResource()
	res, _ := getPodResourcesWithReplicas(pod)
	podTotalresource = podTotalresource.Add(res)

	if podTotalresource.Memory != 0 && podTotalresource.MilliCPU != 0 {
		t.Errorf("got %v, %v, wanted 0,0", podTotalresource.Memory, podTotalresource.MilliCPU)
	}
}
