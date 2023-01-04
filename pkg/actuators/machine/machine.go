package machine

import (
	"context"
	"fmt"
	"math"
	"net"
	"regexp"

	configv1 "github.com/openshift/api/config/v1"
	machinev1 "github.com/openshift/api/machine/v1beta1"
	ovirtconfigv1 "github.com/openshift/cluster-api-provider-ovirt/pkg/apis/ovirtprovider/v1beta1"
	"github.com/openshift/cluster-api-provider-ovirt/pkg/ovirt"
	"github.com/openshift/cluster-api-provider-ovirt/pkg/utils"
	ovirtC "github.com/ovirt/go-ovirt-client/v2"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	InstanceStatusAnnotationKey = "machine.openshift.io/instance-state"
	userDataSecretKey           = "userData"
	// GlobalInfrastuctureName default name for infrastructure object
	globalInfrastuctureName = "cluster"
	bytesInMB               = 1048576
)

type machineScope struct {
	context.Context
	logger      *ovirt.KLogr
	ovirtClient ovirtC.Client
	client      client.Client
	machine     *machinev1.Machine
	// originalMachineToBePatched contains a patch copy of the machine when the machine scope was created
	// it is used by k8sclient to understand the diff and patch the machine object
	originalMachineToBePatched client.Patch
	machineProviderSpec        *ovirtconfigv1.OvirtMachineProviderSpec
}

func newMachineScope(
	ctx context.Context,
	ovirtClient ovirtC.Client,
	c client.Client,
	machine *machinev1.Machine,
	providerSpec *ovirtconfigv1.OvirtMachineProviderSpec) *machineScope {

	return &machineScope{
		Context:                    ctx,
		logger:                     ovirt.NewKLogr("machine-scope").WithVInfo(0),
		ovirtClient:                ovirtClient,
		client:                     c,
		machine:                    machine,
		originalMachineToBePatched: client.MergeFrom(machine.DeepCopy()),
		machineProviderSpec:        providerSpec,
	}
}

// create creates an oVirt VM from the machine object if it does not exists.
func (ms *machineScope) create() error {

	vms, err := ms.ovirtClient.GetVMByName(ms.machine.Name, ovirtC.ContextStrategy(ms.Context))
	clusterId := ms.machineProviderSpec.ClusterId

	if err != nil {
		if !ovirtC.HasErrorCode(err, ovirtC.ENotFound) {
			return errors.Wrap(err, "error finding VM by name")
		}
	}
	if vms != nil {
		ms.logger.Infof("Skipped creating a VM that already exists.")
		return nil
	}

	// Add ignition to the VM params
	ignition, err := ms.getIgnition()
	if err != nil {
		return errors.Wrap(err, "error getting VM ignition")
	}
	// CREATE VM from a template
	templateName := ms.machineProviderSpec.TemplateName
	template, err := ms.ovirtClient.GetTemplateByName(templateName, ovirtC.ContextStrategy(ms.Context))
	if err != nil {
		return errors.Wrapf(err, "error finding template name %s.", templateName)
	}

	optionalVMParams, err := ms.buildOptionalVMParameters(string(ignition), template.ID())
	if err != nil {
		return errors.Wrapf(err, "error building parameters for VM creation")
	}

	instance, err := ms.ovirtClient.CreateVM(ovirtC.ClusterID(clusterId),
		template.ID(),
		ms.machine.Name,
		optionalVMParams, ovirtC.ContextStrategy(ms.Context))

	if err != nil {
		return errors.Wrap(err, "error creating Ovirt instance")
	}

	// Wait till ready
	_, err = ms.ovirtClient.WaitForVMStatus(instance.ID(), ovirtC.VMStatusDown, ovirtC.ContextStrategy(ms.Context))
	if err != nil {
		return errors.Wrap(err, "error creating oVirt VM")
	}

	// apply high_performance rules
	// see: https://access.redhat.com/documentation/en-us/red_hat_virtualization/4.4/html-single/virtual_machine_management_guide/index?extIdCarryOver=true&sc_cid=701f2000001Css5AAC#Automatic_High_Performance_Configuration_Settings
	if ms.machineProviderSpec.VMType == string(ovirtC.VMTypeHighPerformance) {
		graphicsConsoles, err := instance.ListGraphicsConsoles()
		if err != nil {
			return errors.Wrapf(err, "failed to list graphics consoles")
		}
		for _, graphicsConsole := range graphicsConsoles {
			err := graphicsConsole.Remove()
			if err != nil {
				return errors.Wrapf(err, "failed to remove graphics console '%s' from VM '%s'",
					graphicsConsole.ID(), graphicsConsole.VMID())
			}
		}
	}

	var bootableDiskAttachment ovirtC.DiskAttachment
	newDiskSize := uint64(ms.machineProviderSpec.OSDisk.SizeGB * int64(math.Pow(2, 30)))
	// Handle OS disk extension
	if ms.machineProviderSpec.OSDisk != nil {
		diskAttachments, err := instance.ListDiskAttachments()
		if err != nil {
			return errors.Wrapf(err, "failed to list disk attachments for VM %s.", instance.ID())
		}
		for _, diskAttachment := range diskAttachments {
			if diskAttachment.Bootable() {
				bootableDiskAttachment = diskAttachment
			}
		}

		if bootableDiskAttachment == nil {
			return errors.Wrapf(err, "VM %s(%s) doesn't have a bootable disk", instance.Name(), instance.ID())
		}

		disk, err := ms.ovirtClient.GetDisk(bootableDiskAttachment.DiskID(), ovirtC.ContextStrategy(ms.Context))
		if err != nil {
			return err
		}

		if newDiskSize > disk.ProvisionedSize() {
			updatedDisk, err := disk.Update(ovirtC.UpdateDiskParams().MustWithProvisionedSize(newDiskSize))
			if err != nil {
				return errors.Wrapf(err, "failed to extend disk %s", disk.ID())
			}
			ms.logger.Infof("waiting for disk to become OK...")
			updatedDisk, err = updatedDisk.WaitForOK()
			if err != nil {
				return err
			}
		}
	}

	// handleNics reattachment
	if ms.machineProviderSpec.NetworkInterfaces != nil && len(ms.machineProviderSpec.NetworkInterfaces) > 0 {
		nics, err := instance.ListNICs()
		if err != nil {
			return errors.Wrapf(err, "failed to list NICs on VM %s", instance.ID())
		}

		//remove all the nics from the VM instance
		for _, nic := range nics {
			if err := nic.Remove(); err != nil {
				return errors.Wrapf(err, "failed to remove NIC %s", nic.ID())
			}
		}

		//re-create NICs According to the machinespec
		for i, nic := range ms.machineProviderSpec.NetworkInterfaces {
			_, err := instance.CreateNIC(fmt.Sprintf("nic%d", i+1), ovirtC.VNICProfileID(nic.VNICProfileID), ovirtC.CreateNICParams())

			if err != nil {
				return err
			}
		}
	}

	if ms.isAutoPinning() {
		err = ms.ovirtClient.AutoOptimizeVMCPUPinningSettings(instance.ID(), true, ovirtC.ContextStrategy(ms.Context))
		if err != nil {
			return err
		}
	}

	err = ms.ovirtClient.AddTagToVMByName(instance.ID(), ms.machine.Labels["machine.openshift.io/cluster-api-cluster"], ovirtC.ContextStrategy(ms.Context))
	if err != nil {
		return err
	}

	for _, agName := range ms.machineProviderSpec.AffinityGroupsNames {
		ag, err := ms.ovirtClient.GetAffinityGroupByName(ovirtC.ClusterID(clusterId), agName, ovirtC.ContextStrategy(ms.Context))
		if err != nil {
			return err
		}
		err = ag.AddVM(instance.ID())
		if err != nil {
			return err
		}
	}

	// Start the VM
	err = ms.ovirtClient.StartVM(instance.ID(), ovirtC.ContextStrategy(ms.Context))
	if err != nil {
		return errors.Wrap(err, "error running oVirt VM")
	}

	// Wait till running
	_, err = ms.ovirtClient.WaitForVMStatus(instance.ID(), ovirtC.VMStatusUp, ovirtC.ContextStrategy(ms.Context))
	if err != nil {
		return errors.Wrap(err, "error waiting for oVirt VM to be UP")
	}
	return nil
}

// exists returns true if machine exists.
func (ms *machineScope) exists() (bool, error) {
	_, err := ms.ovirtClient.GetVMByName(ms.machine.Name, ovirtC.ContextStrategy(ms.Context))
	if err != nil {
		if ovirtC.HasErrorCode(err, ovirtC.ENotFound) {
			return false, nil
		}
		return false, errors.Wrap(err, "error finding VM by name")
	}
	return true, nil
}

// delete deletes the VM which corresponds with the machine object from the oVirt engine
func (ms *machineScope) delete() error {
	vm, err := ms.ovirtClient.GetVMByName(ms.machine.Name, ovirtC.ContextStrategy(ms.Context))
	if err != nil {
		if ovirtC.HasErrorCode(err, ovirtC.ENotFound) {
			return nil
		}
		return errors.Wrap(err, "error finding VM by name")
	}
	if err := vm.Stop(true, ovirtC.ContextStrategy(ms.Context)); err != nil {
		return err
	}
	if _, err := vm.WaitForStatus(ovirtC.VMStatusDown, ovirtC.ContextStrategy(ms.Context)); err != nil {
		return err
	}
	if err := vm.Remove(ovirtC.ContextStrategy(ms.Context)); err != nil && !ovirtC.HasErrorCode(err, ovirtC.ENotFound) {
		return err
	}

	return nil
}

// returns the ignition from the userData secret
// Ignition is the utility that is used by RHCOS to manipulate disks during initial configuration.
// Ignition completes common disk tasks, including partitioning disks, formatting partitions, writing files,
// and configuring users. For more details see Openshift/RHCOS Docs
func (ms *machineScope) getIgnition() ([]byte, error) {
	if ms.machineProviderSpec == nil || ms.machineProviderSpec.UserDataSecret == nil {
		return nil, nil
	}
	userDataSecret := &corev1.Secret{}
	objKey := client.ObjectKey{
		Namespace: ms.machine.Namespace,
		Name:      ms.machineProviderSpec.UserDataSecret.Name,
	}
	if err := ms.client.Get(ms.Context, objKey, userDataSecret); err != nil {
		return nil, errors.Wrap(err, "error getting userDataSecret")
	}
	userData, exists := userDataSecret.Data[userDataSecretKey]
	if !exists {
		return nil, fmt.Errorf("secret %s missing %s key", objKey, userDataSecretKey)
	}
	return userData, nil
}

func (ms *machineScope) reconcileMachine(ctx context.Context) error {
	instance, err := ms.ovirtClient.GetVMByName(ms.machine.Name, ovirtC.ContextStrategy(ms.Context))

	if err != nil {
		return errors.Wrap(err, "error finding VM by name")
	}

	id := instance.ID()
	status := instance.Status()
	name := instance.Name()
	ms.reconcileMachineProviderID(string(id))
	ms.reconcileMachineAnnotations(string(status), string(id))
	err = ms.reconcileMachineNetwork(ctx, status, name, string(id))
	if err != nil {
		return errors.Wrap(err, "error reconciling machine network")
	}
	err = ms.reconcileMachineProviderStatus(string(status), (*string)(&id))
	if err != nil {
		return errors.Wrap(err, "error reconciling machine provider status")
	}
	return nil
}

func (ms *machineScope) patchMachine(ctx context.Context) error {
	// Copy the status, because its discarded and returned fresh from the DB by the machine resource update.
	// Save it for the status sub-resource update.
	statusCopy := *ms.machine.Status.DeepCopy()
	ms.logger.Infof("Updating machine resource")

	if err := ms.client.Patch(ctx, ms.machine, ms.originalMachineToBePatched); err != nil {
		ms.logger.Errorf("Failed to patch machine %q: %v", ms.machine.GetName(), err)
		return err
	}

	ms.machine.Status = statusCopy

	// patch status
	ms.logger.Infof("Updating machine status sub-resource")
	if err := ms.client.Status().Patch(ctx, ms.machine, ms.originalMachineToBePatched); err != nil {
		ms.logger.Errorf("Failed to patch machine status %q: %v", ms.machine.GetName(), err)
		return err
	}
	return nil
}

func (ms *machineScope) reconcileMachineNetwork(ctx context.Context, status ovirtC.VMStatus,
	name string, vmID string) error {
	switch status {
	// expect IP addresses only on those statuses.
	// in those statuses we 'll try reconciling
	case ovirtC.VMStatusUp, ovirtC.VMStatusMigrating:
	// Do nothing, we can proceed to reconcile Network
	// update machine status.
	// TODO: Should we clean the addresses here?
	case ovirtC.VMStatusDown:
		return nil

	// return error if vm is transient state this will force retry reconciling until VM is up.
	// there is no event generated that will trigger this.  BZ1854787
	default:
		return fmt.Errorf("requeuing reconciliation, VM %s state is %s", name, status)
	}
	addresses := []corev1.NodeAddress{{Address: name, Type: corev1.NodeInternalDNS}}
	ms.logger.Debugf("using oVirt SDK to find %s IP addresses", name)

	// get API and ingress addresses that will be excluded from the node address selection
	excludeAddr, err := ms.getClusterAddress(ctx)
	if err != nil {
		return errors.Wrap(err, "error getting cluster address")
	}

	ip, err := ms.findUsableInternalAddress(ctx, vmID, excludeAddr)

	if err != nil {
		// stop reconciliation till we get IP addresses - otherwise the state will be considered stable.
		ms.logger.Errorf("failed to lookup the VM IP %s - skip setting addresses for this machine", err)
		return errors.Wrap(
			err, "failed to lookup the VM IP - skip setting addresses for this machine")
	}

	if err != nil {
		// stop reconciliation till we get IP addresses - otherwise the state will be considered stable.
		ms.logger.Errorf("failed to lookup the VM IP %s - skip setting addresses for this machine", err)
		return errors.Wrap(
			err, "failed to lookup the VM IP - skip setting addresses for this machine")
	}
	ms.logger.Debugf("received IP address %v from engine", ip)
	addresses = append(addresses, corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: ip})
	ms.machine.Status.Addresses = addresses
	return nil
}

func (ms *machineScope) getClusterAddress(ctx context.Context) (map[string]int, error) {
	infra := &configv1.Infrastructure{}
	objKey := client.ObjectKey{Name: globalInfrastuctureName}
	if err := ms.client.Get(ctx, objKey, infra); err != nil {
		return nil, errors.Wrap(err, "error getting infrastucture data")
	}

	var clusterAddr = make(map[string]int)
	clusterAddr[infra.Status.PlatformStatus.Ovirt.APIServerInternalIP] = 1
	clusterAddr[infra.Status.PlatformStatus.Ovirt.IngressIP] = 1
	return clusterAddr, nil
}

func (ms *machineScope) findUsableInternalAddress(ctx context.Context, vmID string, excludeAddr map[string]int) (string, error) {

	nicRegex := regexp.MustCompile(`^(eth|en|br\-ex).*`)
	IPParams := ovirtC.NewVMIPSearchParams().WithIncludedInterfacePattern(nicRegex)
	for excludedAdrr, _ := range excludeAddr {
		_, ipnet, err := net.ParseCIDR(fmt.Sprintf("%s/32", excludedAdrr))
		if err != nil {
			return "", errors.Wrapf(err, "failed to convert IP Address %s to CIDR", excludedAdrr)
		}
		IPParams = IPParams.WithExcludedRange(*ipnet)
	}

	nics, err := ms.ovirtClient.GetVMIPAddresses(ovirtC.VMID(vmID), IPParams, ovirtC.ContextStrategy(ms.Context))
	if err != nil {
		return "", errors.Wrapf(err, "failed to get reported devices list")
	}

	for _, ipAddressSlice := range nics {
		for _, ipAddress := range ipAddressSlice {
			return ipAddress.String(), nil
		}
	}
	return "", errors.Wrapf(err, "failed to find usable address for VM %s ", vmID)
}

func (ms *machineScope) reconcileMachineProviderStatus(status string, id *string) error {
	providerStatus, err := ovirtconfigv1.ProviderStatusFromRawExtension(ms.machine.Status.ProviderStatus)
	if err != nil {
		return errors.Wrap(err, "error unmarshaling machine ProviderStatus field")
	}
	providerStatus.InstanceState = &status
	providerStatus.InstanceID = id
	rawExtension, err := ovirtconfigv1.RawExtensionFromProviderStatus(providerStatus)
	if err != nil {
		return errors.Wrap(err, "error marshaling machine ProviderStatus field")
	}
	ms.machine.Status.ProviderStatus = rawExtension
	return nil
}

func (ms *machineScope) reconcileMachineProviderID(id string) {
	providerID := utils.ProviderIDPrefix + id
	ms.machine.Spec.ProviderID = &providerID
}

func (ms *machineScope) reconcileMachineAnnotations(status string, id string) {
	if ms.machine.ObjectMeta.Annotations == nil {
		ms.machine.ObjectMeta.Annotations = make(map[string]string)
	}
	ms.machine.ObjectMeta.Annotations[InstanceStatusAnnotationKey] = status
	ms.machine.ObjectMeta.Annotations[utils.OvirtIDAnnotationKey] = id
}

func (ms *machineScope) buildOptionalVMParameters(ignition string, templateID ovirtC.TemplateID) (ovirtC.BuildableVMParameters, error) {
	optionalVMParams := ovirtC.CreateVMParams()
	optionalVMParams = optionalVMParams.MustWithInitializationParameters(ignition, ms.machine.Name)

	if ms.machineProviderSpec.VMType != "" {
		optionalVMParams = optionalVMParams.MustWithVMType(ovirtC.VMType(ms.machineProviderSpec.VMType))
	}
	if ms.machineProviderSpec.InstanceTypeId != "" {
		optionalVMParams = optionalVMParams.MustWithInstanceTypeID(ovirtC.InstanceTypeID(ms.machineProviderSpec.InstanceTypeId))
	} else {
		// Add CPU
		if ms.machineProviderSpec.CPU != nil {
			optionalVMParams = optionalVMParams.MustWithCPUParameters(uint(ms.machineProviderSpec.CPU.Cores),
				uint(ms.machineProviderSpec.CPU.Sockets),
				uint(ms.machineProviderSpec.CPU.Threads))
		}

		if ms.machineProviderSpec.MemoryMB > 0 {
			optionalVMParams = optionalVMParams.MustWithMemory(int64(bytesInMB) * int64(ms.machineProviderSpec.MemoryMB))
		}

		if ms.machineProviderSpec.GuaranteedMemoryMB > 0 {
			optionalMemoryPolicy := ovirtC.NewMemoryPolicyParameters().MustWithGuaranteed(int64(bytesInMB) * int64(ms.machineProviderSpec.GuaranteedMemoryMB))
			optionalVMParams = optionalVMParams.WithMemoryPolicy(optionalMemoryPolicy)
		}
	}

	optionalPlacementPolicy := ovirtC.NewVMPlacementPolicyParameters()
	if ms.isAutoPinning() {
		hosts, err := ms.ovirtClient.ListHosts(ovirtC.ContextStrategy(ms.Context))
		if err != nil {
			return nil, errors.Wrap(err, "error Listing hosts")
		}
		hostIDs := make([]ovirtC.HostID, 0)
		for _, host := range hosts {
			if string(host.ClusterID()) == ms.machineProviderSpec.ClusterId {
				hostIDs = append(hostIDs, host.ID())
			}
		}
		if len(hostIDs) > 0 {
			optionalPlacementPolicy, err = optionalPlacementPolicy.WithHostIDs(hostIDs)
			if err != nil {
				return nil, errors.Wrapf(err, "failed to create placement policy parameters with host IDs: %v", hostIDs)
			}
		}
	}

	if ms.machineProviderSpec.Hugepages > 0 {
		optionalVMParams = optionalVMParams.MustWithHugePages(ovirtC.VMHugePages(ms.machineProviderSpec.Hugepages))
	}

	// Handle Sparse disks and Format
	if ms.machineProviderSpec.Sparse != nil || ms.machineProviderSpec.Format != "" {
		tempDiskAttachment, err := ms.ovirtClient.ListTemplateDiskAttachments(templateID, ovirtC.ContextStrategy(ms.Context))
		if err != nil {
			return nil, errors.Wrapf(err, "failed to fetch template %s disk attachments from oVirt Engine",
				ms.machineProviderSpec.TemplateName)
		}

		diskParams := []ovirtC.OptionalVMDiskParameters{}
		for _, diskAttachment := range tempDiskAttachment {
			diskBuilder, _ := ovirtC.NewBuildableVMDiskParameters(diskAttachment.DiskID())
			if ms.machineProviderSpec.Sparse != nil {
				diskBuilder.MustWithSparse(*ms.machineProviderSpec.Sparse)
			}

			if ms.machineProviderSpec.Format != "" {
				diskBuilder.MustWithFormat(ovirtC.ImageFormat(ms.machineProviderSpec.Format))
			}

			diskParams = append(diskParams, diskBuilder)
		}
		optionalVMParams = optionalVMParams.MustWithDisks(diskParams)
	}

	// Handle Disk Clone
	if ms.machineProviderSpec.Clone != nil {
		optionalVMParams = optionalVMParams.MustWithClone(*ms.machineProviderSpec.Clone)
	} else {
		if ms.machineProviderSpec.VMType == string(ovirtC.VMTypeDesktop) {
			optionalVMParams = optionalVMParams.MustWithClone(false)
		} else {
			optionalVMParams = optionalVMParams.MustWithClone(true)
		}
	}

	if ms.machineProviderSpec.StorageDomainId != "" {
		tempDiskAttachment, err := ms.ovirtClient.ListTemplateDiskAttachments(templateID, ovirtC.ContextStrategy(ms.Context))
		if err != nil {
			return nil, errors.Wrapf(err, "failed to fetch template %s disk attachments from oVirt Engine",
				ms.machineProviderSpec.TemplateName)
		}
		optionalVMParams = optionalVMParams.MustWithDisks([]ovirtC.OptionalVMDiskParameters{
			ovirtC.MustNewBuildableVMDiskParameters(tempDiskAttachment[0].DiskID()).MustWithStorageDomainID(ovirtC.StorageDomainID(ms.machineProviderSpec.StorageDomainId)),
		})
	}

	vmAffinity := ovirtC.VMAffinityMigratable
	// apply high_performance rules
	// see: https://access.redhat.com/documentation/en-us/red_hat_virtualization/4.4/html-single/virtual_machine_management_guide/index?extIdCarryOver=true&sc_cid=701f2000001Css5AAC#Automatic_High_Performance_Configuration_Settings
	if ms.machineProviderSpec.VMType == string(ovirtC.VMTypeHighPerformance) {
		vmAffinity = ovirtC.VMAffinityUserMigratable
		optionalVMParams.WithSoundcardEnabled(false)
		optionalVMParams.WithSerialConsole(true)

		cpuMode := ovirtC.NewVMCPUParams()
		cpuMode = cpuMode.MustWithMode(ovirtC.CPUModeHostPassthrough)
		optionalVMParams = optionalVMParams.MustWithCPU(cpuMode)

		memPolicy := ovirtC.NewMemoryPolicyParameters()
		memPolicy = memPolicy.MustWithBallooning(false)
		optionalVMParams = optionalVMParams.WithMemoryPolicy(memPolicy)
	}

	optionalPlacementPolicy = optionalPlacementPolicy.MustWithAffinity(vmAffinity)
	optionalVMParams = optionalVMParams.WithPlacementPolicy(optionalPlacementPolicy)

	return optionalVMParams, nil
}

func (ms *machineScope) isAutoPinning() bool {
	return ms.machineProviderSpec.AutoPinningPolicy != "" && ms.machineProviderSpec.AutoPinningPolicy != "none"
}
