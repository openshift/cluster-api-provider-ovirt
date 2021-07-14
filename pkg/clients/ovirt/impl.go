/*
Copyright oVirt Authors
SPDX-License-Identifier: Apache-2.0
*/

package ovirt

import (
	"fmt"
	"math"
	"regexp"
	"time"

	ovirtconfigv1 "github.com/openshift/cluster-api-provider-ovirt/pkg/apis/ovirtprovider/v1beta1"
	machinev1 "github.com/openshift/machine-api-operator/pkg/apis/machine/v1beta1"
	"github.com/openshift/machine-api-operator/pkg/util"
	ovirtsdk "github.com/ovirt/go-ovirt"
	"github.com/pkg/errors"
	"k8s.io/klog"
)

var _ Client = (*ovirtClient)(nil)

// CreateVMByMachine creates an oVirt VM according to the ovirt provider spec.
func (is *ovirtClient) CreateVMByMachine(
	machineName string,
	ovirtClusterID string,
	ignition []byte,
	providerSpec *ovirtconfigv1.OvirtMachineProviderSpec) (instance *Instance, err error) {
	if machineName == "" {
		return nil, errors.New("machine name must be specified to create instance")
	}
	if ovirtClusterID == "" {
		return nil, errors.New("ovirtClusterID must be specified to create instance")
	}
	if providerSpec == nil {
		return nil, errors.New("oVirt machine providerSpec must be specified to create instance")
	}

	cluster := ovirtsdk.NewClusterBuilder().Id(providerSpec.ClusterId).MustBuild()
	template := ovirtsdk.NewTemplateBuilder().Name(providerSpec.TemplateName).MustBuild()
	init := ovirtsdk.NewInitializationBuilder().
		CustomScript(string(ignition)).
		HostName(machineName).
		MustBuild()

	vmBuilder := ovirtsdk.NewVmBuilder().
		Name(machineName).
		Cluster(cluster).
		Template(template).
		Initialization(init)

	if providerSpec.VMType != "" {
		vmBuilder.Type(ovirtsdk.VmType(providerSpec.VMType))
	}
	if providerSpec.InstanceTypeId != "" {
		vmBuilder.InstanceTypeBuilder(
			ovirtsdk.NewInstanceTypeBuilder().
				Id(providerSpec.InstanceTypeId))
	} else {
		if providerSpec.CPU != nil {
			vmBuilder.CpuBuilder(
				ovirtsdk.NewCpuBuilder().
					TopologyBuilder(ovirtsdk.NewCpuTopologyBuilder().
						Cores(int64(providerSpec.CPU.Cores)).
						Sockets(int64(providerSpec.CPU.Sockets)).
						Threads(int64(providerSpec.CPU.Threads))))
		}
		if providerSpec.MemoryMB > 0 {
			vmBuilder.Memory(int64(math.Pow(2, 20)) * int64(providerSpec.MemoryMB))
		}
	}

	isAutoPinning := false
	if providerSpec.AutoPinningPolicy != "" {
		autoPinningPolicy := mapAutoPinningPolicy(providerSpec.AutoPinningPolicy)

		// if we have a policy, we need to set the pinning to all the hosts in the cluster.
		if autoPinningPolicy != ovirtsdk.AUTOPINNINGPOLICY_DISABLED {
			isAutoPinning = true
			hostsInCluster, err := is.getHostsInCluster(providerSpec.ClusterId)
			if err != nil {
				return nil, errors.Wrapf(
					err, "error finding hosts in cluster %s", providerSpec.ClusterId)
			}
			placementPolicyBuilder := ovirtsdk.NewVmPlacementPolicyBuilder()
			placementPolicy, err := placementPolicyBuilder.Hosts(hostsInCluster).
				Affinity(ovirtsdk.VMAFFINITY_MIGRATABLE).Build()
			if err != nil {
				return nil, errors.Wrap(err, "failed to build the placement policy of the vm")
			}
			vmBuilder.PlacementPolicy(placementPolicy)
		}
	}
	if providerSpec.Hugepages > 0 {
		customProp, err := ovirtsdk.NewCustomPropertyBuilder().
			Name("hugepages").
			Value(fmt.Sprint(providerSpec.Hugepages)).
			Build()
		if err != nil {
			return nil, errors.Wrap(err, "error setting hugepages custom property")
		}
		vmBuilder.CustomPropertiesOfAny(customProp)
	}

	vm, err := vmBuilder.Build()
	if err != nil {
		return nil, errors.Wrap(err, "failed to construct VM struct")
	}

	klog.Infof("creating VM: %v", vm.MustName())
	response, err := is.connection.SystemService().VmsService().Add().Vm(vm).Send()
	if err != nil {
		klog.Errorf("Failed creating VM %v", err)
		return nil, errors.Wrap(err, "error creating VM")
	}

	vmID := response.MustVm().MustId()

	err = is.connection.WaitForVM(vmID, ovirtsdk.VMSTATUS_DOWN, time.Minute)
	if err != nil {
		return nil, errors.Wrap(err, "timed out waiting for the VM creation to finish")
	}

	vmService := is.connection.SystemService().VmsService().VmService(vmID)

	if providerSpec.OSDisk != nil {
		err = is.handleDiskExtension(vmService, response, providerSpec)
		if err != nil {
			return nil, err
		}
	}

	err = is.handleNics(vmService, providerSpec)
	if err != nil {
		return nil, errors.Wrapf(err, "failed handling nics creation for VM %s", vm.MustName())
	}

	if isAutoPinning {
		err = is.handleAutoPinning(vmID, mapAutoPinningPolicy(providerSpec.AutoPinningPolicy))
		if err != nil {
			klog.Errorf("updating the VM (%s) with auto pinning policy failed! %v", vmID, err)
		}
	}

	_, err = is.connection.SystemService().VmsService().
		VmService(response.MustVm().MustId()).
		TagsService().Add().
		Tag(ovirtsdk.NewTagBuilder().Name(ovirtClusterID).MustBuild()).
		Send()
	if err != nil {
		klog.Errorf("Failed to add tag to VM, skipping %v", err)
	}

	err = is.handleAffinityGroups(
		response.MustVm(),
		providerSpec.ClusterId,
		providerSpec.AffinityGroupsNames)
	if err != nil {
		return nil, err
	}
	return &Instance{response.MustVm()}, nil
}

func (is *ovirtClient) handleDiskExtension(vmService *ovirtsdk.VmService, createdVM *ovirtsdk.VmsServiceAddResponse, providerSpec *ovirtconfigv1.OvirtMachineProviderSpec) error {
	attachmentsResponse, err := vmService.DiskAttachmentsService().List().Send()
	if err != nil {
		return errors.Wrap(err, "error finding disk attachment")
	}

	var bootableDiskAttachment *ovirtsdk.DiskAttachment
	for _, disk := range attachmentsResponse.MustAttachments().Slice() {
		if disk.MustBootable() {
			// found the os disk
			bootableDiskAttachment = disk
		}
	}
	if bootableDiskAttachment == nil {
		return fmt.Errorf("VM %s(%s) doesn't have a bootable disk",
			createdVM.MustVm().MustName(), createdVM.MustVm().MustId())
	}
	// extend the disk if requested size is bigger than template. We won't support shrinking it.
	newDiskSize := providerSpec.OSDisk.SizeGB * int64(math.Pow(2, 30))

	// get the disk
	getDisk, err := vmService.Connection().SystemService().DisksService().DiskService(bootableDiskAttachment.MustId()).Get().Send()
	if err != nil {
		return errors.Wrap(err, "error getting disk")
	}

	size := getDisk.MustDisk().MustProvisionedSize()
	if newDiskSize < size {
		klog.Warning("The machine spec specified new disk size %d, and the current disk size is %d. Shrinking is "+
			"not supported.", newDiskSize, size)
	}
	if newDiskSize > size {
		klog.Infof("Extending the OS disk from %d to %d", size, newDiskSize)
		bootableDiskAttachment.SetDisk(getDisk.MustDisk())
		bootableDiskAttachment.
			MustDisk().
			SetProvisionedSize(newDiskSize)
		_, err := vmService.DiskAttachmentsService().
			AttachmentService(bootableDiskAttachment.MustId()).
			Update().
			DiskAttachment(bootableDiskAttachment).
			Send()
		if err != nil {
			return errors.Wrap(err, "failed to update the OS disk")
		}
		klog.Infof("Waiting while extending the OS disk")
		// wait for the disk extension to be over
		err = is.connection.WaitForDisk(bootableDiskAttachment.MustId(), ovirtsdk.DISKSTATUS_OK, 20*time.Minute)
		if err != nil {
			return errors.Wrap(err, "error while waiting for disk")
		}
	}
	return nil
}

// DeleteVM deletes a VM with the given ID from the oVirt engine
func (is *ovirtClient) DeleteVM(id string) error {
	klog.Infof("Deleting VM with ID: %s", id)
	vmService := is.connection.SystemService().VmsService().VmService(id)
	_, err := vmService.Stop().Send()
	if err != nil {
		return fmt.Errorf("error stopping VM %s: %w", id, err)
	}
	err = util.PollImmediate(time.Second*10, time.Minute*5, func() (bool, error) {
		vmResponse, err := vmService.Get().Send()
		if err != nil {
			return false, nil
		}
		vm, ok := vmResponse.Vm()
		if !ok {
			return false, err
		}

		return vm.MustStatus() == ovirtsdk.VMSTATUS_DOWN, nil
	})
	if err != nil {
		return fmt.Errorf("error waiting for VM %s to be down: %w", id, err)
	}

	_, err = vmService.Remove().Send()
	if err != nil {
		return fmt.Errorf("error removing VM %s: %w", id, err)
	}
	// poll till VM doesn't exist
	err = util.PollImmediate(time.Second*10, time.Minute*5, func() (bool, error) {
		_, err := vmService.Get().Send()
		return err != nil, nil
	})
	if err != nil {
		return fmt.Errorf("error waiting for VM %s to be removed: %w", id, err)
	}
	return nil
}

// GetVMByMachine returns the VM instance corresponding to the machine object
// If there is no VM corresponding to the machine object then nil will be returned
func (is *ovirtClient) GetVMByMachine(machine machinev1.Machine) (instance *Instance, err error) {
	if machine.Spec.ProviderID != nil && *machine.Spec.ProviderID != "" {
		instance, err = is.GetVMByID(*machine.Spec.ProviderID)
		if err == nil {
			return instance, err
		}
	}
	instance, err = is.GetVMByName(machine.Name)
	return instance, err
}

// GetVMByID returns an oVirt VM instance in case it exists in the oVirt env
// If there is no VM with the given ID then nil will be returned
func (is *ovirtClient) GetVMByID(id string) (instance *Instance, err error) {
	klog.Infof("Fetching VM by ID: %s", id)
	if id == "" {
		return nil, fmt.Errorf("vm id should be specified")
	}
	response, err := is.connection.SystemService().VmsService().VmService(id).Get().Send()
	if err != nil {
		return nil, fmt.Errorf("error getting VM with ID %s: %w", id, err)
	}
	klog.Infof("Got VM by ID: %s", response.MustVm().MustName())
	return &Instance{Vm: response.MustVm()}, nil
}

// GetVMByName returns an oVirt VM instance in case it exists in the oVirt env
// If there is no VM with the given name then nil will be returned
func (is *ovirtClient) GetVMByName(mName string) (*Instance, error) {
	response, err := is.connection.SystemService().VmsService().
		List().Search("name=" + mName).Send()
	if err != nil {
		klog.Errorf("Failed to fetch VM by name")
		return nil, fmt.Errorf("error getting VM with name %s: %w", mName, err)
	}
	for _, vm := range response.MustVms().Slice() {
		if name, ok := vm.Name(); ok {
			if name == mName {
				return &Instance{Vm: vm}, nil
			}
		}
	}
	// returning an nil instance if we didn't find a match
	return nil, nil
}

func (is *ovirtClient) StartVM(id string) error {
	vmService := is.connection.SystemService().VmsService().VmService(id)
	if _, err := vmService.Start().Send(); err != nil {
		return errors.Wrap(err, "error starting oVirt VM")
	}
	return nil
}

func (is *ovirtClient) handleNics(vmService *ovirtsdk.VmService, spec *ovirtconfigv1.OvirtMachineProviderSpec) error {
	if spec.NetworkInterfaces == nil || len(spec.NetworkInterfaces) == 0 {
		return nil
	}
	nicList, err := vmService.NicsService().List().Send()
	if err != nil {
		return errors.Wrap(err, "failed fetching VM network interfaces")
	}

	// remove all existing nics
	for _, n := range nicList.MustNics().Slice() {
		_, err := vmService.NicsService().NicService(n.MustId()).Remove().Send()
		if err != nil {
			return errors.Wrap(err, "failed clearing all interfaces before populating new ones")
		}
	}

	// re-add nics
	for i, nic := range spec.NetworkInterfaces {
		_, err := vmService.NicsService().Add().Nic(
			ovirtsdk.NewNicBuilder().
				Name(fmt.Sprintf("nic%d", i+1)).
				VnicProfileBuilder(ovirtsdk.NewVnicProfileBuilder().Id(nic.VNICProfileID)).
				MustBuild()).
			Send()
		if err != nil {
			return errors.Wrap(err, "failed to create network interface")
		}
	}
	return nil
}

// Find virtual machine IP Address by ID
func (is *ovirtClient) FindVirtualMachineIP(id string, excludeAddr map[string]int) (string, error) {
	vmService := is.connection.SystemService().VmsService().VmService(id)

	// Get the guest reported devices
	reportedDeviceResp, err := vmService.ReportedDevicesService().List().Send()
	if err != nil {
		return "", fmt.Errorf("failed to get reported devices list, reason: %w", err)
	}
	reportedDeviceSlice, _ := reportedDeviceResp.ReportedDevice()

	if len(reportedDeviceSlice.Slice()) == 0 {
		return "", fmt.Errorf("cannot find NICs for vmId: %s", id)
	}

	var nicRegex = regexp.MustCompile(`^(eth|en).*`)

	for _, reportedDevice := range reportedDeviceSlice.Slice() {
		nicName, _ := reportedDevice.Name()
		if !nicRegex.MatchString(nicName) {
			klog.Infof("ovirt vm id: %s, skipped nic %s , naming regex mismatch", id, nicName)
			continue
		}

		ips, hasIps := reportedDevice.Ips()
		if hasIps {
			for _, ip := range ips.Slice() {
				ipAddress, hasAddress := ip.Address()

				if _, ok := excludeAddr[ipAddress]; ok {
					klog.Infof("ipAddress %s is excluded from usable IPs", ipAddress)
					continue
				}

				if hasAddress {
					klog.Infof("ovirt vm id: %s , found usable IP %s", id, ipAddress)
					return ipAddress, nil
				}
			}
		}
	}
	return "", fmt.Errorf("coudlnt find usable IP address for vm id: %s", id)
}

func (is *ovirtClient) getAffinityGroups(cID string, agNames []string) (ag []*ovirtsdk.AffinityGroup, err error) {
	res, err := is.connection.SystemService().ClustersService().
		ClusterService(cID).AffinityGroupsService().
		List().Send()
	if err != nil {
		return nil, fmt.Errorf("error listing affinity groups in cluster %s: %w", cID, err)
	}
	agNamesMap := make(map[string]*ovirtsdk.AffinityGroup)
	for _, af := range res.MustGroups().Slice() {
		agNamesMap[af.MustName()] = af
	}
	for _, agName := range agNames {
		if _, ok := agNamesMap[agName]; !ok {
			return nil, fmt.Errorf("affinity group %v was not found on cluster %v", agName, cID)
		}
		ag = append(ag, agNamesMap[agName])
	}
	return ag, nil
}

// handleAffinityGroups adds the VM to the provided affinity groups
func (is *ovirtClient) handleAffinityGroups(vm *ovirtsdk.Vm, cID string, agsName []string) error {
	ags, err := is.getAffinityGroups(cID, agsName)
	if err != nil {
		return errors.Wrap(err, "error getting affinity groups")
	}
	agService := is.connection.SystemService().ClustersService().
		ClusterService(cID).AffinityGroupsService()
	for _, ag := range ags {
		klog.Infof("Adding machine %v to affinity group %v", vm.MustName(), ag.MustName())
		_, err = agService.GroupService(ag.MustId()).VmsService().Add().Vm(vm).Send()

		// TODO: bug 1932320: Remove error handling workaround when BZ#1931932 is resolved and backported
		if err != nil && !errors.Is(err, ovirtsdk.XMLTagNotMatchError{ActualTag: "action", ExpectedTag: "vm"}) {
			return fmt.Errorf(
				"failed to add VM %s to AffinityGroup %s, error: %w",
				vm.MustName(),
				ag.MustName(),
				err)
		}
	}
	return nil
}

// getHostsInCluster will return a HostSlice of the hosts in a given cluster ID.
func (is *ovirtClient) getHostsInCluster(clusterID string) (*ovirtsdk.HostSlice, error) {
	clusterService := is.connection.SystemService().ClustersService().ClusterService(clusterID)
	clusterGet, err := clusterService.Get().Send()
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster %s: %w", clusterID, err)
	}
	clusterName := clusterGet.MustCluster().MustName()
	hostsInCluster, err := is.connection.SystemService().HostsService().List().Search(
		fmt.Sprintf("cluster=%s", clusterName)).Send()
	if err != nil {
		return nil, fmt.Errorf(
			"failed to get the list of hosts in the cluster %s: %w", clusterID, err)
	}
	return hostsInCluster.MustHosts(), nil
}

// handleAutoPinning updates the VM after creation to set the auto pinning policy configuration.
func (is *ovirtClient) handleAutoPinning(id string, autoPinningPolicy ovirtsdk.AutoPinningPolicy) error {
	vmService := is.connection.SystemService().VmsService().VmService(id)
	optimizeCPUSettings := autoPinningPolicy == ovirtsdk.AUTOPINNINGPOLICY_ADJUST
	_, err := vmService.AutoPinCpuAndNumaNodes().OptimizeCpuSettings(optimizeCPUSettings).Send()
	if err != nil {
		return errors.Errorf("failed to set the auto pinning policy on the VM!, %v", err)
	}
	return nil
}

func mapAutoPinningPolicy(policy string) ovirtsdk.AutoPinningPolicy {
	switch policy {
	case "none":
		return ovirtsdk.AUTOPINNINGPOLICY_DISABLED
	case "resize_and_pin":
		return ovirtsdk.AUTOPINNINGPOLICY_ADJUST
	default:
		return ovirtsdk.AUTOPINNINGPOLICY_DISABLED
	}
}

func (is *ovirtClient) GetEngineVersion() (*ovirtsdk.Version, error) {
	return is.connection.SystemService().Get().MustSend().MustApi().MustProductInfo().MustVersion(), nil
}

// CreateAPIConnection returns a a client to oVirt's API endpoint by using the given credentials
func CreateAPIConnection(creds *Creds) (*ovirtsdk.Connection, error) {
	connection, err := ovirtsdk.NewConnectionBuilder().
		URL(creds.URL).
		Username(creds.Username).
		Password(creds.Password).
		CAFile(creds.CAFile).
		Insecure(creds.Insecure).
		Build()
	if err != nil {
		return nil, errors.Wrap(err, "error creating API connection")
	}
	return connection, nil
}
