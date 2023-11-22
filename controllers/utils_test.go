package controllers

import (
	"testing"

	"fmt"
	"github.com/onsi/gomega"
	machinev1 "github.com/openshift/api/machine/v1beta1"

	"k8s.io/apimachinery/pkg/runtime"
)

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
