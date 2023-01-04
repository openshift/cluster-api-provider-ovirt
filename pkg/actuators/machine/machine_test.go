//go:build unit

package machine

import (
	"testing"

	machinev1 "github.com/openshift/api/machine/v1beta1"
	"github.com/openshift/cluster-api-provider-ovirt/pkg/apis/ovirtprovider/v1beta1"
	capoV1Beta1 "github.com/openshift/cluster-api-provider-ovirt/pkg/apis/ovirtprovider/v1beta1"
	"github.com/openshift/cluster-api-provider-ovirt/pkg/ovirt"
	ovirtclient "github.com/ovirt/go-ovirt-client/v2"
	k8sCorev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMachineScope_IsAutoPinning(t *testing.T) {
	testcases := []struct {
		name              string
		autoPinningPolicy string
		expected          bool
	}{
		{
			name:              "Empty AutoPinningPolicy should return false",
			autoPinningPolicy: "",
			expected:          false,
		},
		{
			name:              "AutoPinningPolicy 'none' should return false",
			autoPinningPolicy: "none",
			expected:          false,
		},
		{
			name:              "AutoPinningPolicy 'adjust' should return true",
			autoPinningPolicy: "adjust",
			expected:          true,
		},
	}

	for _, testcase := range testcases {
		t.Run(testcase.name, func(t *testing.T) {
			ms := machineScope{
				machineProviderSpec: &v1beta1.OvirtMachineProviderSpec{
					AutoPinningPolicy: testcase.autoPinningPolicy,
				},
			}

			got := ms.isAutoPinning()
			if testcase.expected != got {
				t.Fatalf("Expected AutoPinningPolicy to be %t, but got %t", testcase.expected, got)
			}
		})
	}
}

func TestMachineScope_BuildOptionalVMParameter(t *testing.T) {
	testcases := []struct {
		name   string
		setup  func(basicSpec *v1beta1.OvirtMachineProviderSpec, basicClient ovirtclient.Client)
		verify func(t *testing.T, params ovirtclient.OptionalVMParameters)
	}{
		{
			name: "verify CPU Topo",
			setup: func(
				basicSpec *v1beta1.OvirtMachineProviderSpec,
				basicClient ovirtclient.Client) {
				basicSpec.CPU.Cores = 1
				basicSpec.CPU.Threads = 1
				basicSpec.CPU.Sockets = 10
			},
			verify: func(t *testing.T, params ovirtclient.OptionalVMParameters) {
				cpuTopo := params.CPU().Topo()
				if cpuTopo.Cores() != 1 {
					t.Errorf("Expected CPU Cores to be %d, but got %d", 1, cpuTopo.Cores())
				}
				if cpuTopo.Threads() != 1 {
					t.Errorf("Expected CPU Threads to be %d, but got %d", 1, cpuTopo.Threads())
				}
				if cpuTopo.Sockets() != 10 {
					t.Errorf("Expected CPU Sockets to be %d, but got %d", 10, cpuTopo.Sockets())
				}
			},
		},
	}

	for _, testcase := range testcases {
		t.Run(testcase.name, func(t *testing.T) {
			helper, err := ovirtclient.NewMockTestHelper(ovirt.NewKLogr("go-ovirt-client"))
			if err != nil {
				t.Fatalf("Unexpected error occurred setting up test helper: %v", err)
			}

			template, err := helper.GetClient().GetBlankTemplate()
			if err != nil {
				t.Fatalf("Failed to get blank template: %v", err)
			}
			spec := basicMachineProviderSpec(template.Name(), string(helper.GetClusterID()))

			testcase.setup(spec, helper.GetClient())

			ms := machineScope{
				machineProviderSpec: spec,
				machine: &machinev1.Machine{
					ObjectMeta: v1.ObjectMeta{
						Name: "test-machine",
					},
				},
				ovirtClient: helper.GetClient(),
			}
			params, err := ms.buildOptionalVMParameters("", template.ID())
			if err != nil {
				t.Fatalf("Unexpected error occurred while building optional VM parameter: %v", err)
			}

			testcase.verify(t, params)
		})
	}
}

func basicMachineProviderSpec(templateName string, clusterID string) *v1beta1.OvirtMachineProviderSpec {
	return &v1beta1.OvirtMachineProviderSpec{
		ClusterId:    clusterID,
		TemplateName: templateName,
		Name:         "vm-hello-ovirt",
		VMType:       "server",
		MemoryMB:     16348,
		Format:       "raw",
		OSDisk:       &capoV1Beta1.Disk{SizeGB: 31},
		CPU: &capoV1Beta1.CPU{
			Cores:   1,
			Threads: 1,
			Sockets: 8,
		},
		UserDataSecret: &k8sCorev1.LocalObjectReference{
			Name: "ignitionscript",
		},
		AutoPinningPolicy:  "",
		Hugepages:          0,
		GuaranteedMemoryMB: 10000,
	}
}
