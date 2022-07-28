//go:build unit

package machine

import (
	"testing"

	"github.com/openshift/cluster-api-provider-ovirt/pkg/apis/ovirtprovider/v1beta1"
	ovirtclientlog "github.com/ovirt/go-ovirt-client-log/v3"
	ovirtclient "github.com/ovirt/go-ovirt-client/v2"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestValidateMachine(t *testing.T) {
	helper, err := ovirtclient.NewMockTestHelper(ovirtclientlog.NewTestLogger(t))
	if err != nil {
		t.Fatalf("failed to setup test helper: %v", err)
	}

	testCases := []struct {
		name          string
		spec          *v1beta1.OvirtMachineProviderSpec
		expectIsValid bool
	}{
		{
			name: "validation of minimal valid machine provider spec succeeds",
			spec: BasicValidSpec(func(omps *v1beta1.OvirtMachineProviderSpec) *v1beta1.OvirtMachineProviderSpec {
				return omps
			}),
			expectIsValid: true,
		},
		{
			name: "validation of machine provider spec without user data secret fails",
			spec: BasicValidSpec(func(omps *v1beta1.OvirtMachineProviderSpec) *v1beta1.OvirtMachineProviderSpec {
				omps.UserDataSecret = nil
				return omps
			}),
			expectIsValid: false,
		},
		{
			name: "validation of machine provider spec with empty user data secret name fails",
			spec: BasicValidSpec(func(omps *v1beta1.OvirtMachineProviderSpec) *v1beta1.OvirtMachineProviderSpec {
				omps.UserDataSecret.Name = ""
				return omps
			}),
			expectIsValid: false,
		},
	}
	for _, testcase := range testCases {
		t.Run(testcase.name, func(t *testing.T) {
			validationError := validateMachine(helper.GetClient(), testcase.spec)
			if validationError != nil == testcase.expectIsValid {
				t.Errorf("expected spec to be valid(%t), but got error '%v'", testcase.expectIsValid, validationError)
			}
		})
	}
}

func BasicValidSpec(f func(*v1beta1.OvirtMachineProviderSpec) *v1beta1.OvirtMachineProviderSpec) *v1beta1.OvirtMachineProviderSpec {
	basicValidSpec := &v1beta1.OvirtMachineProviderSpec{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ovirt-machine-12345",
			Namespace: "default",
		},
		MemoryMB: 16348,
		VMType:   "server",
		OSDisk: &v1beta1.Disk{
			SizeGB: 31,
		},
		CPU: &v1beta1.CPU{
			Cores:   4,
			Threads: 1,
			Sockets: 1,
		},
		Name:      "ovirt-vm-12345",
		ClusterId: "46991e3f-8752-4ab6-9f2d-c37a98358d52",
		UserDataSecret: &v1.LocalObjectReference{
			Name: "top secret user data",
		},
		Hugepages: noHugePages,
	}

	return f(basicValidSpec)
}