package ovirt

import (
	ovirtconfigv1 "github.com/openshift/cluster-api-provider-ovirt/pkg/apis/ovirtprovider/v1beta1"
	machinev1 "github.com/openshift/machine-api-operator/pkg/apis/machine/v1beta1"
	ovirtsdk "github.com/ovirt/go-ovirt"
)

type Client interface {
	CreateVMByMachine(machineName string, ovirtClusterID string, ignition []byte, providerSpec *ovirtconfigv1.OvirtMachineProviderSpec) (instance *Instance, err error)
	DeleteVM(id string) error
	GetVMByMachine(machine machinev1.Machine) (instance *Instance, err error)
	GetVMByID(id string) (instance *Instance, err error)
	GetVMByName(mName string) (*Instance, error)
	StartVM(id string) error
	FindVirtualMachineIP(id string, excludeAddr map[string]int) (string, error)
	GetEngineVersion() (*ovirtsdk.Version, error)
}
