package machine

import (
	"fmt"
	ovirtconfigv1 "github.com/openshift/cluster-api-provider-ovirt/pkg/apis/ovirtprovider/v1beta1"
	ovirtC "github.com/openshift/cluster-api-provider-ovirt/pkg/clients/ovirt"
	apierrors "github.com/openshift/machine-api-operator/pkg/controller/machine"
	ovirtsdk "github.com/ovirt/go-ovirt"
)

// validateMachine validates the machine object yaml fields and
// returns InvalidMachineConfiguration in case the validation failed
func validateMachine(ovirtClient ovirtC.OvirtClient, config *ovirtconfigv1.OvirtMachineProviderSpec) *apierrors.MachineError {
	// UserDataSecret
	if config.UserDataSecret == nil {
		return apierrors.InvalidMachineConfiguration(
			fmt.Sprintf("%s UserDataSecret must be provided!", ErrorInvalidMachineObject))
	} else if config.UserDataSecret.Name == "" {
		return apierrors.InvalidMachineConfiguration(
			fmt.Sprintf("%s UserDataSecret *Name* must be provided!", ErrorInvalidMachineObject))
	}

	err := validateInstanceID(config)
	if err != nil {
		return err
	}

	// root disk of the node
	if config.OSDisk == nil {
		return apierrors.InvalidMachineConfiguration(
			fmt.Sprintf("%s OS Disk (os_disk) must be specified!", ErrorInvalidMachineObject))
	} else if config.OSDisk.SizeGB == 0 {
		return apierrors.InvalidMachineConfiguration(
			fmt.Sprintf("%s OS Disk (os_disk) *SizeGB* must be specified!", ErrorInvalidMachineObject))
	}

	err = validateVirtualMachineType(config.VMType)
	if err != nil {
		return err
	}

	if config.AutoPinningPolicy != "" {
		err := autoPinningSupported(ovirtClient, config)
		if err != nil {
			return apierrors.InvalidMachineConfiguration(fmt.Sprintf("%s", err))
		}
	}
	if err := validateHugepages(config.Hugepages); err != nil {
		return apierrors.InvalidMachineConfiguration(fmt.Sprintf("%s", err))
	}
	return nil
}

// validateInstanceID execute validations regarding the InstanceID.
// Returns: nil or InvalidMachineConfiguration
func validateInstanceID(config *ovirtconfigv1.OvirtMachineProviderSpec) *apierrors.MachineError {
	// Cannot set InstanceTypeID and at same time: MemoryMB OR CPU
	if len(config.InstanceTypeId) != 0 {
		if config.MemoryMB != 0 || config.CPU != nil {
			return apierrors.InvalidMachineConfiguration(
				fmt.Sprintf("%s InstanceTypeID and MemoryMB OR CPU cannot be set at the same time!", ErrorInvalidMachineObject))
		}
	} else {
		if config.MemoryMB == 0 {
			return apierrors.InvalidMachineConfiguration(
				fmt.Sprintf("%s MemoryMB must be specified!", ErrorInvalidMachineObject))
		}
		if config.CPU == nil {
			return apierrors.InvalidMachineConfiguration(
				fmt.Sprintf("%s CPU must be specified!", ErrorInvalidMachineObject))
		}
	}
	return nil
}

// validateVirtualMachineType execute validations regarding the
// Virtual Machine type (desktop, server, high_performance).
// Returns: nil or InvalidMachineConfiguration
func validateVirtualMachineType(vmtype string) *apierrors.MachineError {
	if len(vmtype) == 0 {
		return apierrors.InvalidMachineConfiguration("VMType (keyword: type in YAML) must be specified")
	}
	switch vmtype {
	case "server", "high_performance", "desktop":
		return nil
	default:
		return apierrors.InvalidMachineConfiguration(
			"error creating oVirt instance: The machine type must "+
				"be one of the following options: "+
				"server, high_performance or desktop. The value: %s is not valid", vmtype)
	}
}

// validateHugepages execute validation regarding the Virtual Machine hugepages
// custom property (2048, 1048576).
// Returns: nil or error
func validateHugepages(value int32) error {
	switch value {
	case 0, 2048, 1048576:
		return nil
	default:
		return fmt.Errorf(
			"error creating oVirt instance: The machine `hugepages` custom property must "+
				"be one of the following options: 2048, 1048576. "+
				"The value: %d is not valid", value)
	}
}

// autoPinningSupported will check if the engine's version is relevant for the feature.
func autoPinningSupported(ovirtClient ovirtC.OvirtClient, config *ovirtconfigv1.OvirtMachineProviderSpec) error {
	err := validateAutPinningPolicyValue(config.AutoPinningPolicy)
	if err != nil {
		return err
	}
	// TODO: remove the version check when everyone uses engine 4.4.5
	engineVer, err := ovirtClient.GetEngineVersion()
	if err != nil {
		return err
	}
	autoPiningRequiredEngineVersion := ovirtsdk.NewVersionBuilder().
		Major(4).
		Minor(4).
		Build_(5).
		Revision(0).
		MustBuild()
	versionCompareResult, err := versionCompare(engineVer, autoPiningRequiredEngineVersion)
	if err != nil {
		return err
	}
	// The version is OK.
	if versionCompareResult >= 0 {
		return nil
	}
	return fmt.Errorf("the engine version %d.%d.%d is not supporting the auto pinning feature. "+
		"Please update to 4.4.5 or later", engineVer.MustMajor(), engineVer.MustMinor(), engineVer.MustBuild())
}

// validateAutPinningPolicyValue execute validations regarding the
// Virtual Machine auto pinning policy (disabled, existing, adjust).
// Returns: nil or error
func validateAutPinningPolicyValue(autopinningpolicy string) error {
	switch autopinningpolicy {
	case "disabled", "existing", "adjust":
		return nil
	default:
		return fmt.Errorf(
			"error creating oVirt instance: The machine auto pinning policy must "+
				"be one of the following options: "+
				"disabled, existing or adjust. The value: %s is not valid", autopinningpolicy)
	}
}

// versionCompare will take two *ovirtsdk.Version and compare the two
func versionCompare(v *ovirtsdk.Version, other *ovirtsdk.Version) (int64, error) {
	if v == nil || other == nil {
		return 5, fmt.Errorf("can't compare nil objects")
	}
	if v == other {
		return 0, nil
	}
	result := v.MustMajor() - other.MustMajor()
	if result == 0 {
		result = v.MustMinor() - other.MustMinor()
		if result == 0 {
			result = v.MustBuild() - other.MustBuild()
			if result == 0 {
				result = v.MustRevision() - other.MustRevision()
			}
		}
	}
	return result, nil
}