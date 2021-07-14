package machine

import (
	configv1 "github.com/openshift/api/config/v1"
	ovirtconfigv1 "github.com/openshift/cluster-api-provider-ovirt/pkg/apis/ovirtprovider/v1beta1"
	ovirtClient "github.com/openshift/cluster-api-provider-ovirt/pkg/clients/ovirt"
	"github.com/openshift/cluster-api-provider-ovirt/pkg/utils"
	machinev1 "github.com/openshift/machine-api-operator/pkg/apis/machine/v1beta1"
	machinecontroller "github.com/openshift/machine-api-operator/pkg/controller/machine"
	ovirtsdk "github.com/ovirt/go-ovirt"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	userDataSecretName = "worker-user-data"
	userDataBlob       = `{"ignition":{"config":{"append":[{"source":"https://xxx.xxx.xxx.xx:xxxxx/config/master","verification":{}}]},"security":{"tls":{"certificateAuthorities":[{"source":"data:text/plain;charset=utf-8;base64,ZWNobyAiZ2FsIHphaWRtYW4gaXMgYW4gYW1hemluZyBkZXZlbG9wZXIvZ2VuaXVzIgo=","verification":{}}]}},"timeouts":{},"version":"2.2.0"},"networkd":{},"passwd":{},"storage":{},"systemd":{}}`

	stubClusterID = "ovirt-cluster"

	machineName               = "ovirt-cluster-worker-0"
	stubMachineTestLabel      = "testlabel"
	stubMachineTestLabelValue = "test"

	stubOvirtTemplateName       = "ovirt-cluster-template"
	stubOvirtVMId               = "12345678-1234-1234-1234-123456789123"
	stubOvirtClusterId          = "12345678-1234-1234-1234-123456789123"
	stubOvirtVMIP               = "10.40.32.120"
	stubOvirtinstanceTypeId     = "12345678-1234-1234-1234-123456789123"
	stubOvirtMemoryMB           = 1024
	stubOvirtSizeGB             = 30
	stubOvirtVMType             = "server"
	stubOvirtVNICProfileID      = "12345678-1234-1234-1234-123456789123"
	stubOvirtAffinityGroupsName = "compute"
	stubOvirtHugepages          = 2048
	stubOvirtVMStatus           = ovirtsdk.VMSTATUS_UP

	stubAPIServerInternalIP = "10.40.32.30"
	stubIngressIP           = "10.40.32.31"
)

func stubProviderSpec() *ovirtconfigv1.OvirtMachineProviderSpec {
	return &ovirtconfigv1.OvirtMachineProviderSpec{
		TypeMeta: metav1.TypeMeta{
			Kind:       "",
			APIVersion: "",
		},
		ObjectMeta: metav1.ObjectMeta{},
		UserDataSecret: &corev1.LocalObjectReference{
			Name: userDataSecretName,
		},
		CredentialsSecret: &corev1.LocalObjectReference{
			Name: utils.OvirtCloudCredsSecretName,
		},
		Name:           machineName,
		TemplateName:   stubOvirtTemplateName,
		ClusterId:      stubOvirtClusterId,
		InstanceTypeId: "",
		CPU: &ovirtconfigv1.CPU{
			Sockets: 8,
			Cores:   1,
			Threads: 1,
		},
		MemoryMB: stubOvirtMemoryMB,
		OSDisk:   &ovirtconfigv1.Disk{SizeGB: stubOvirtSizeGB},
		VMType:   stubOvirtVMType,
		NetworkInterfaces: []*ovirtconfigv1.NetworkInterface{
			{VNICProfileID: stubOvirtVNICProfileID},
		},
		AffinityGroupsNames: []string{stubOvirtAffinityGroupsName},
		AutoPinningPolicy:   "disabled",
		Hugepages:           stubOvirtHugepages,
	}
}

// Returns a representation of a machine that hasn't been patched (no provider status)
// Ignores conversions errors since we create the source ourselves
func stubEmptyMachine() *machinev1.Machine {
	machinePc := stubProviderSpec()

	providerSpec, _ := ovirtconfigv1.RawExtensionFromProviderSpec(machinePc)

	machine := &machinev1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      machineName,
			Namespace: utils.NAMESPACE,
			Labels: map[string]string{
				machinev1.MachineClusterIDLabel:                 stubClusterID,
				"machine.openshift.io/cluster-api-machine-role": "worker",
			},
			Annotations: map[string]string{
				// skip node draining since it's not mocked
				machinecontroller.ExcludeNodeDrainingAnnotation: "",
			},
		},
		Spec: machinev1.MachineSpec{
			ProviderID: nil,
			ProviderSpec: machinev1.ProviderSpec{
				Value: providerSpec,
			},
		},
	}
	return machine
}

// Returns a representation of a machine that hasn been patched (with provider status)
// Ignores conversions errors since we create the source ourselves
func stubRunningMachineFromStabVM() *machinev1.Machine {
	m := stubEmptyMachine()
	id := stubOvirtVMId
	providerID := utils.ProviderIDPrefix + id
	addresses := []corev1.NodeAddress{
		{Type: corev1.NodeInternalDNS, Address: machineName},
		{Type: corev1.NodeInternalIP, Address: stubOvirtVMIP},
	}
	providerStatus, _ := ovirtconfigv1.ProviderStatusFromRawExtension(m.Status.ProviderStatus)
	status := string(ovirtsdk.VMSTATUS_UP)
	providerStatus.InstanceState = &status
	providerStatus.InstanceID = &id
	// TODO: Implement
	//providerStatus.Conditions =

	m.Spec.ProviderID = &providerID
	m.Status.Addresses = addresses
	m.ObjectMeta.Annotations = map[string]string{}
	rawExtension, _ := ovirtconfigv1.RawExtensionFromProviderStatus(providerStatus)
	m.Status.ProviderStatus = rawExtension
	return m
}

// Returns a representation of a machine that hasn't been patched (no provider status) and with a test label
// Ignores conversions errors since we create the source ourselves
func stubEmptyMachineWithLabels() *machinev1.Machine {
	m := stubEmptyMachine()
	m.ObjectMeta.Labels[stubMachineTestLabel] = stubMachineTestLabelValue
	return m
}

func stubVM() *ovirtClient.Instance {
	vm := &ovirtsdk.Vm{}
	vm.SetId(stubOvirtVMId)
	vm.SetName(machineName)
	vm.SetStatus(ovirtsdk.VMSTATUS_UP)
	return &ovirtClient.Instance{Vm: vm}
}

func stubOvirtCredentialsSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      utils.OvirtCloudCredsSecretName,
			Namespace: utils.NAMESPACE,
		},
		Data: map[string][]byte{
			ovirtClient.UrlField:      []byte("engine.com/ovirt-engine/api"),
			ovirtClient.UsernameField: []byte("admin@internal"),
			ovirtClient.PasswordField: []byte("123456"),
			ovirtClient.InsecureField: []byte("false"),
			ovirtClient.CaBundleField: []byte("-----BEGIN CERTIFICATE-----\nabcd\n-----END CERTIFICATE-----"),
		},
	}
}

func stubOvirtUserDataSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      userDataSecretName,
			Namespace: utils.NAMESPACE,
		},
		Data: map[string][]byte{
			userDataSecretKey: []byte(userDataBlob),
		},
	}
}

func stubInfrastructure() *configv1.Infrastructure {
	return &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
		Status: configv1.InfrastructureStatus{
			PlatformStatus: &configv1.PlatformStatus{
				Type: "",
				Ovirt: &configv1.OvirtPlatformStatus{
					APIServerInternalIP: stubAPIServerInternalIP,
					IngressIP:           stubIngressIP,
				},
			},
		},
	}
}
