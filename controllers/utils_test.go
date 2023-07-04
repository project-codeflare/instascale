package controllers

import (
	"testing"

	"fmt"
	"github.com/onsi/gomega"
	machinev1 "github.com/openshift/api/machine/v1beta1"

	arbv1 "github.com/project-codeflare/multi-cluster-app-dispatcher/pkg/apis/controller/v1beta1"
	clusterstateapi "github.com/project-codeflare/multi-cluster-app-dispatcher/pkg/controller/clusterstate/api"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestGetPodResourcesWithReplicas(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	tests := []struct {
		name    string
		pod     arbv1.CustomPodResourceTemplate
		wantRes *clusterstateapi.Resource
		wantRep int
	}{
		{
			name: "Empty requests and limits",
			pod: arbv1.CustomPodResourceTemplate{
				Replicas: 0,
				Requests: v1.ResourceList{},
				Limits:   v1.ResourceList{},
			},
			wantRes: &clusterstateapi.Resource{
				MilliCPU: 0,
				Memory:   0,
				GPU:      0,
			},
			wantRep: 0,
		},
		{
			name: "Requests and limits",
			pod: arbv1.CustomPodResourceTemplate{
				Replicas: 3,
				Requests: v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("1"),
					v1.ResourceMemory: resource.MustParse("2Gi"),
					v1.ResourceName(clusterstateapi.GPUResourceName): resource.MustParse("1"),
				},
				Limits: v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("2"),
					v1.ResourceMemory: resource.MustParse("2Gi"),
					v1.ResourceName(clusterstateapi.GPUResourceName): resource.MustParse("2"),
				},
			},
			wantRes: &clusterstateapi.Resource{
				MilliCPU: 1000,
				Memory:   2 * 1024 * 1024 * 1024,
				GPU:      1,
			},
			wantRep: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, rep := getPodResourcesWithReplicas(tt.pod)
			g.Expect(res).To(gomega.Equal(tt.wantRes))
			g.Expect(rep).To(gomega.Equal(tt.wantRep))
		})
	}
}

func TestGetListOfPodResourcesFromOneGenericItem(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	tests := []struct {
		name string
		awr  *arbv1.AppWrapperGenericResource
		want []*clusterstateapi.Resource
	}{
		{
			name: "Empty requests and limits",
			awr: &arbv1.AppWrapperGenericResource{
				CustomPodResources: []arbv1.CustomPodResourceTemplate{
					{
						Replicas: 0,
						Requests: v1.ResourceList{},
						Limits:   v1.ResourceList{},
					},
				},
			},
			want: []*clusterstateapi.Resource{},
		},
		{
			name: "Request and Limits",
			awr: &arbv1.AppWrapperGenericResource{
				GenericTemplate: runtime.RawExtension{
					Raw: []byte(`{"customPodResources": [{"replicas": 1, "requests": {"gpu": "0", "memory": "1Gi"}, "limits": {"gpu": "0.5", "memory": "2Gi"}}]}`),
				},
				CustomPodResources: []arbv1.CustomPodResourceTemplate{
					{
						Replicas: 1,
						Requests: v1.ResourceList{
							v1.ResourceMemory: resource.MustParse("2Gi"),
							v1.ResourceName(clusterstateapi.GPUResourceName): resource.MustParse("1"),
						},
						Limits: v1.ResourceList{
							v1.ResourceMemory: resource.MustParse("2Gi"),
							v1.ResourceName(clusterstateapi.GPUResourceName): resource.MustParse("2"),
						},
					},
				},
			},
			want: []*clusterstateapi.Resource{
				{
					GPU:    1,
					Memory: 2 * 1024 * 1024 * 1024,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, _ := GetListOfPodResourcesFromOneGenericItem(tt.awr)
			g.Expect(len(result)).To(gomega.Equal(len(tt.want)))
			for i := range result {
				g.Expect(result[i]).To(gomega.Equal(tt.want[i]))
			}
		})
	}
}

func TestProviderSpecFromRawExtension(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	tests := []struct {
		name         string
		rawExtension *runtime.RawExtension
		want         *machinev1.AWSMachineProviderConfig
		wantErr      error
	}{
		{
			name:         "Empty raw extension",
			rawExtension: nil,
			want:         &machinev1.AWSMachineProviderConfig{},
			wantErr:      nil,
		},
		{
			name: "valid raw extension",
			rawExtension: &runtime.RawExtension{
				Raw: []byte(`{"instanceType": "m4.xlarge"}`),
			},
			want: &machinev1.AWSMachineProviderConfig{
				InstanceType: "m4.xlarge",
			},
			wantErr: nil,
		},
		{
			name: "invalid raw extension",
			rawExtension: &runtime.RawExtension{
				Raw: []byte(`{"ami"}, "instanceType": "m4.xlarge"}`),
			},
			want:    nil,
			wantErr: fmt.Errorf("error unmarshalling providerSpec: invalid character '}' after object key"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ProviderSpecFromRawExtension(tt.rawExtension)
			if err != nil {
				g.Expect(err).To(gomega.Equal(tt.wantErr))
			} else {
				g.Expect(err).ToNot(gomega.HaveOccurred())
				g.Expect(result).To(gomega.Equal(tt.want))
			}
		})
	}
}

func TestNewClientBuilder(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	tests := []struct {
		name       string
		kubeconfig string
		wantErr    bool
	}{
		{
			name:       "empty kubeconfig",
			kubeconfig: "",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := NewClientBuilder(tt.kubeconfig)
			g.Expect(err).To(gomega.HaveOccurred())
			g.Expect(result).To(gomega.BeNil())
		})
	}
}

func TestGetRestConfig(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	tests := []struct {
		name       string
		kubeconfig string
		wantErr    bool
	}{
		{
			name:       "invalid kubeconfig",
			kubeconfig: "/path-to-invalid-kube/config",
			wantErr:    true,
		},
		{
			name:       "empty kubeconfig",
			kubeconfig: "",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := getRestConfig(tt.kubeconfig)
			g.Expect(err).To(gomega.HaveOccurred())
			g.Expect(result).To(gomega.BeNil())
		})
	}
}

func TestContains(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	tests := []struct {
		name string
		s    []string
		str  string
		want bool
	}{
		{
			name: "value is present",
			s:    []string{"test1", "test2", "test3"},
			str:  "test2",
			want: true,
		},
		{
			name: "value is not present",
			s:    []string{"test1", "test2", "test3"},
			str:  "test4",
			want: false,
		},
		{
			name: "empty slice",
			s:    []string{},
			str:  "test1",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := contains(tt.s, tt.str)
			g.Expect(result).To(gomega.Equal(tt.want))
		})
	}
}
