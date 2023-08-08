package controllers

import (
	"testing"

	"github.com/onsi/gomega"
	arbv1 "github.com/project-codeflare/multi-cluster-app-dispatcher/pkg/apis/controller/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDiscoverInstanceTypes(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	tests := []struct {
		name        string
		input       *arbv1.AppWrapper
		expected    map[string]int
		expectedErr error
	}{
		{
			name: "Test with multiple orderedinstance",
			input: &arbv1.AppWrapper{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"orderedinstance": "test.instance1_test.instance2",
					},
				},
				Spec: arbv1.AppWrapperSpec{
					AggrResources: arbv1.AppWrapperResourceList{
						GenericItems: []arbv1.AppWrapperGenericResource{
							{
								CustomPodResources: []arbv1.CustomPodResourceTemplate{
									{
										Replicas: 1,
									},
									{
										Replicas: 2,
									},
								},
							},
						},
					},
				},
			},
			expected: map[string]int{
				"test.instance1": 1,
				"test.instance2": 2,
			},
		},
		{
			name: "Test with one orderedinstance",
			input: &arbv1.AppWrapper{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"orderedinstance": "test.instance1",
					},
				},
				Spec: arbv1.AppWrapperSpec{
					AggrResources: arbv1.AppWrapperResourceList{
						GenericItems: []arbv1.AppWrapperGenericResource{
							{
								CustomPodResources: []arbv1.CustomPodResourceTemplate{
									{
										Replicas: 1,
									},
								},
							},
						},
					},
				},
			},
			expected: map[string]int{
				"test.instance1": 1,
			},
		},
		{
			name: "Test with empty orderedinstance",
			input: &arbv1.AppWrapper{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"orderedinstance": "",
					},
				},
			},
			expected: map[string]int{},
		},
		{
			name: "Test with no orderedinstance",
			input: &arbv1.AppWrapper{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						// zero instances
					},
				},
			},
			expected: map[string]int{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := discoverInstanceTypes(test.input)
			g.Expect(result).To(gomega.Equal(test.expected))
		})
	}
}
