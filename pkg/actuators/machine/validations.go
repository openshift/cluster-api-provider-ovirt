package machine

import (
	"fmt"

	ovirtconfigv1 "github.com/openshift/cluster-api-provider-ovirt/pkg/apis/ovirtprovider/v1beta1"
	ovirtC "github.com/openshift/cluster-api-provider-ovirt/pkg/clients/ovirt"
	ovirtsdk "github.com/ovirt/go-ovirt"
	"github.com/pkg/errors"
)

const (
	ErrorInvalidMachineObject    = "error validating machine object fields"
	noHugePages                  = 0
	hugePages2M                  = 2048
	hugePages1GB                 = 1048576
	autoPiningPolicyNone         = "none"
	autoPiningPolicyResizeAndPin = "resize_and_pin"
)

// validateMachine validates the machine object yaml fields and
// returns InvalidMachineConfiguration in case the validation failed
func validateMachine(ovirtClient ovirtC.Client, config *ovirtconfigv1.OvirtMachineProviderSpec) error {
	// UserDataSecret
	if config.UserDataSecret == nil {
		return fmt.Errorf("%s UserDataSecret must be provided!", ErrorInvalidMachineObject)
	} else if config.UserDataSecret.Name == "" {
		return fmt.Errorf("%s UserDataSecret *Name* must be provided!", ErrorInvalidMachineObject)
	}

	err := validateInstanceID(config)
	if err != nil {
		return fmt.Errorf("error validating InstanceID %v", err)
	}

	// root disk of the node
	if config.OSDisk == nil {
		return fmt.Errorf("%s OS Disk (os_disk) must be specified!", ErrorInvalidMachineObject)
	} else if config.OSDisk.SizeGB == 0 {
		return fmt.Errorf("%s OS Disk (os_disk) *SizeGB* must be specified!", ErrorInvalidMachineObject)
	}

	err = validateVirtualMachineType(config.VMType)
	if err != nil {
		return fmt.Errorf("error validating Machine Type %w", err)
	}

	if config.AutoPinningPolicy != "" {
		err := autoPinningSupported(ovirtClient, config)
		if err != nil {
			return errors.Wrap(err, "error validating AutoPinningPolicy")
		}
	}
	if err := validateHugepages(config.Hugepages); err != nil {
		return errors.Wrap(err, "error validating Hugepages")
	}
	return nil
}

// validateInstanceID execute validations regarding the InstanceID.
// Returns: nil or InvalidMachineConfiguration
func validateInstanceID(config *ovirtconfigv1.OvirtMachineProviderSpec) error {
	// Cannot set InstanceTypeID and at same time: MemoryMB OR CPU
	if len(config.InstanceTypeId) != 0 {
		if config.MemoryMB != 0 || config.CPU != nil {
			return fmt.Errorf(
				"%s InstanceTypeID and MemoryMB OR CPU cannot be set at the same time", ErrorInvalidMachineObject)
		}
	} else {
		if config.MemoryMB == 0 {
			return fmt.Errorf("%s MemoryMB must be specified", ErrorInvalidMachineObject)
		}
		if config.CPU == nil {
			return fmt.Errorf("%s CPU must be specified", ErrorInvalidMachineObject)
		}
	}
	return nil
}

// validateVirtualMachineType execute validations regarding the
// Virtual Machine type (desktop, server, high_performance).
// Returns: nil or InvalidMachineConfiguration
func validateVirtualMachineType(vmtype string) error {
	if len(vmtype) == 0 {
		return fmt.Errorf("VMType (keyword: type in YAML) must be specified")
	}
	switch vmtype {
	case "server", "high_performance", "desktop":
		return nil
	default:
		return fmt.Errorf(
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
	case noHugePages, hugePages2M, hugePages1GB:
		return nil
	default:
		return fmt.Errorf(
			"error creating oVirt instance: The machine `hugepages` custom property must "+
				"be one of the following options: 2048, 1048576. "+
				"The value: %d is not valid", value)
	}
}

// autoPinningSupported will check if the engine's version is relevant for the feature.
func autoPinningSupported(ovirtClient ovirtC.Client, config *ovirtconfigv1.OvirtMachineProviderSpec) error {
	err := validateAutoPinningPolicyValue(config.AutoPinningPolicy)
	if err != nil {
		return errors.Wrap(err, "error validating auto pinning policy")
	}
	// TODO: remove the version check when everyone uses engine 4.4.5
	engineVer, err := ovirtClient.GetEngineVersion()
	if err != nil {
		return errors.Wrap(err, "error finding engine version")
	}
	autoPiningRequiredEngineVersion := ovirtsdk.NewVersionBuilder().
		Major(4).
		Minor(4).
		Build_(5).
		Revision(0).
		MustBuild()
	versionCompareResult, err := versionCompare(engineVer, autoPiningRequiredEngineVersion)
	if err != nil {
		return errors.Wrap(err, "error comparing engine versions")
	}
	// The version is OK.
	if versionCompareResult >= 0 {
		return nil
	}
	return fmt.Errorf("the engine version %d.%d.%d is not supporting the auto pinning feature. "+
		"Please update to 4.4.5 or later", engineVer.MustMajor(), engineVer.MustMinor(), engineVer.MustBuild())
}

// validateAutPinningPolicyValue execute validations regarding the
// Virtual Machine auto pinning policy (none, resize_and_pin).
// Returns: nil or error
func validateAutoPinningPolicyValue(autopinningpolicy string) error {
	switch autopinningpolicy {
	case autoPiningPolicyNone, autoPiningPolicyResizeAndPin:
		return nil
	default:
		return fmt.Errorf(
			"error creating oVirt instance: The machine auto pinning policy must "+
				"be one of the following options: "+
				"none or resize_and_pin. "+
				"The value: %s is not valid", autopinningpolicy)
	}
}

// versionCompare will take two *ovirtsdk.Version and compare the then
// 1 - is returned if v > other
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
