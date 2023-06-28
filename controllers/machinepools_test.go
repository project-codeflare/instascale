package controllers

import (
	"testing"
	"fmt"
	"github.com/onsi/gomega"
	arbv1 "github.com/project-codeflare/multi-cluster-app-dispatcher/pkg/apis/controller/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type OCMServiceMock struct{
	doesMachinePoolExist bool
	shouldReturnError    bool
	machinePools         []string
}

func (m *OCMServiceMock) scaleMachinePool(aw *arbv1.AppWrapper, userRequestedInstanceType string, replicas int) error {
	return nil
}

func (m *OCMServiceMock) deleteMachinePool(aw *arbv1.AppWrapper) error {
	return nil
}

func (m *OCMServiceMock) machinePoolExists(aw *arbv1.AppWrapper) (bool, error) {
	if m.shouldReturnError {
		return false, fmt.Errorf("some error")
	}
	for _, mp := range m.machinePools {
		if mp == aw.Name {
			return true, nil
		}
	}
	return false, nil
}

func (m *OCMServiceMock) getOCMClusterID(r *AppWrapperReconciler) error {
	return nil
}


func TestMachinePoolExists(t *testing.T) {
	type args struct {
		err error
	}
	tests := []struct {
		name     string
		ocm      OCMService
		aw       *arbv1.AppWrapper
		modifyFn func(aw *arbv1.AppWrapper)
		want bool
		wantErr bool
	}{
		{
			name: "machine pool exists",
			ocm:  &OCMServiceMock{
				doesMachinePoolExist: true,
				shouldReturnError: false,
				machinePools: []string{"test-instance"},
			},
			aw: &arbv1.AppWrapper{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-instance",
				},
			},
			want: true,
			wantErr: false,
		},
		{
			name: "Machine pool does not exist",
			ocm:  &OCMServiceMock{
				doesMachinePoolExist: false,
				shouldReturnError: false,
				machinePools: []string{"test-instance"},
			},
			aw: &arbv1.AppWrapper{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-instance-fail",
				},
			},
			want: false,
			wantErr: false,
		},
		{
			name: "Error occurs when checking if machine pool exists",
			ocm: &OCMServiceMock{
				doesMachinePoolExist: false,
				shouldReturnError:    true,
				machinePools: []string{"test-instance"},
			},
			aw: &arbv1.AppWrapper{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-instance-fail",
				},
			},
			want: false, 
			wantErr: true, 
		},

	}

	for _, testcase := range tests {
		tt := testcase
		t.Run(tt.name, func(t *testing.T) {
			g := gomega.NewWithT(t)
			t.Parallel()

			result, err := tt.ocm.machinePoolExists(tt.aw)

			g.Expect(result).To(gomega.Equal(tt.want))
			g.Expect(err != nil).To(gomega.Equal(tt.wantErr))
		})
	}
}
