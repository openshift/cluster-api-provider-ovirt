package machine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/golang/mock/gomock"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-api-provider-ovirt/pkg/utils"
	ovirtsdk "github.com/ovirt/go-ovirt"
	"k8s.io/apimachinery/pkg/types"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	ovirtconfigv1 "github.com/openshift/cluster-api-provider-ovirt/pkg/apis/ovirtprovider/v1beta1"
	mockOVirtClient "github.com/openshift/cluster-api-provider-ovirt/pkg/clients/ovirt/mock"
	machinev1 "github.com/openshift/machine-api-operator/pkg/apis/machine/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	testOvirtVMName = "ovirt-test-vm"
	testNamespace   = "ovirt-test"

	timeout = 10 * time.Second
)

func init() {
	// Add types to scheme
	machinev1.AddToScheme(scheme.Scheme)
	configv1.Install(scheme.Scheme)
}

func machineWithSpec(spec *ovirtconfigv1.OvirtMachineProviderSpec) *machinev1.Machine {
	rawSpec, err := ovirtconfigv1.RawExtensionFromProviderSpec(spec)
	if err != nil {
		panic("Failed to encode raw extension from provider spec")
	}

	return &machinev1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testOvirtVMName,
			Namespace: testNamespace,
		},
		Spec: machinev1.MachineSpec{
			ProviderSpec: machinev1.ProviderSpec{
				Value: rawSpec,
			},
		},
	}
}

func TestGetIgnition(t *testing.T) {
	userDataSecretName := "worker-user-data"
	defaultMachineProviderSpec := &ovirtconfigv1.OvirtMachineProviderSpec{
		UserDataSecret: &corev1.LocalObjectReference{
			Name: userDataSecretName,
		},
	}

	testCases := []struct {
		testCase            string
		userDataSecret      *corev1.Secret
		machineProviderSpec *ovirtconfigv1.OvirtMachineProviderSpec
		expectedIgnition    []byte
		expectError         bool
	}{
		{
			testCase: "good ignition",
			userDataSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      userDataSecretName,
					Namespace: testNamespace,
				},
				Data: map[string][]byte{
					userDataSecretKey: []byte("{}"),
				},
			},
			machineProviderSpec: defaultMachineProviderSpec,
			expectedIgnition:    []byte("{}"),
			expectError:         false,
		},
		{
			testCase:            "missing secret",
			userDataSecret:      nil,
			machineProviderSpec: defaultMachineProviderSpec,
			expectError:         true,
		},
		{
			testCase: "missing key in secret",
			userDataSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      userDataSecretName,
					Namespace: testNamespace,
				},
				Data: map[string][]byte{
					"badKey": []byte("{}"),
				},
			},
			machineProviderSpec: defaultMachineProviderSpec,
			expectError:         true,
		},
		{
			testCase:            "no provider spec",
			userDataSecret:      nil,
			machineProviderSpec: nil,
			expectError:         false,
			expectedIgnition:    nil,
		},
		{
			testCase:            "no user-data in provider spec",
			userDataSecret:      nil,
			machineProviderSpec: &ovirtconfigv1.OvirtMachineProviderSpec{},
			expectError:         false,
			expectedIgnition:    nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.testCase, func(t *testing.T) {
			clientObjs := []runtime.Object{}

			if tc.userDataSecret != nil {
				clientObjs = append(clientObjs, tc.userDataSecret)
			}

			client := fake.NewClientBuilder().WithRuntimeObjects(clientObjs...).Build()
			machine := machineWithSpec(tc.machineProviderSpec)
			machineProviderSpec := tc.machineProviderSpec

			ms := newMachineScope(
				context.Background(),
				nil,
				client,
				machine,
				machineProviderSpec)

			ignition, err := ms.getIgnition()
			if !tc.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}

			if !bytes.Equal(ignition, tc.expectedIgnition) {
				t.Errorf("Got: %q, Want: %q", ignition, tc.expectedIgnition)
			}
		})
	}
}

func TestPatchMachine(t *testing.T) {
	testCases := []struct {
		name          string
		machine       *machinev1.Machine
		mutate        func(*machinev1.Machine)
		expectError   bool
		expectMachine func(*machinev1.Machine) error
	}{
		{
			name:    "Test changing labels",
			machine: stubEmptyMachine(),
			mutate: func(m *machinev1.Machine) {
				m.ObjectMeta.Labels[stubMachineTestLabel] = stubMachineTestLabelValue
			},
			expectError: false,
			expectMachine: func(m *machinev1.Machine) error {
				if _, ok := m.ObjectMeta.Labels[stubMachineTestLabel]; !ok {
					return fmt.Errorf("label %s was not set", stubMachineTestLabel)
				}
				if m.ObjectMeta.Labels[stubMachineTestLabel] != stubMachineTestLabelValue {
					return fmt.Errorf("label %s not got unexpected value, expected %s but recived %s",
						stubMachineTestLabel, stubMachineTestLabelValue, m.ObjectMeta.Labels[stubMachineTestLabel])
				}
				return nil
			},
		},
		{
			name:    "Test setting provider status",
			machine: stubEmptyMachine(),
			mutate: func(m *machinev1.Machine) {
				providerStatus, _ := ovirtconfigv1.ProviderStatusFromRawExtension(m.Status.ProviderStatus)
				instanceID := stubOvirtVMId
				instanceState := string(stubOvirtVMStatus)
				providerStatus.InstanceState = &instanceState
				providerStatus.InstanceID = &instanceID
				rawExtension, _ := ovirtconfigv1.RawExtensionFromProviderStatus(providerStatus)
				m.Status.ProviderStatus = rawExtension
			},
			expectError: false,
			expectMachine: func(m *machinev1.Machine) error {
				providerStatus, err := ovirtconfigv1.ProviderStatusFromRawExtension(m.Status.ProviderStatus)
				if err != nil {
					return fmt.Errorf("unable to get provider status: %v", err)
				}
				if providerStatus.InstanceID == nil || *providerStatus.InstanceID != stubOvirtVMId {
					return fmt.Errorf("instanceID is nil or not equal expected %s", stubOvirtVMId)
				}

				if providerStatus.InstanceState == nil || *providerStatus.InstanceState != string(stubOvirtVMStatus) {
					return fmt.Errorf("instanceState is nil or not equal expected %s", string(stubOvirtVMStatus))
				}
				return nil
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			gs := NewWithT(t)
			ctx := context.TODO()

			testMachine := &machinev1.Machine{}
			// Setup Mock k8sClient
			k8sClient := fake.NewClientBuilder().
				WithScheme(scheme.Scheme).
				WithRuntimeObjects(tc.machine).
				Build()

			machineKey := types.NamespacedName{Namespace: tc.machine.Namespace, Name: tc.machine.Name}

			// Ensure the machine has synced to the cache
			err = k8sClient.Get(ctx, machineKey, testMachine)
			gs.Expect(err).ShouldNot(HaveOccurred())

			ms := newMachineScope(
				ctx,
				nil,
				k8sClient,
				tc.machine,
				nil)

			tc.mutate(ms.machine)

			// Patch the machine and check the expectation from the test case
			err = ms.patchMachine(ctx)
			if tc.expectError == true {
				gs.Expect(err).Should(HaveOccurred())
			} else {
				gs.Expect(err).ShouldNot(HaveOccurred())

				err = k8sClient.Get(ctx, machineKey, testMachine)
				gs.Expect(err).ShouldNot(HaveOccurred())
				gs.Expect(tc.expectMachine(testMachine)).ShouldNot(HaveOccurred())

				// TODO: check if the resource version increases just because it is mock

				// Check that resource version doesn't change if we call patchMachine() again
				//machineResourceVersion := testMachine.ResourceVersion
				//gs.Expect(ms.patchMachine(ctx)).To(Succeed())
				//err = k8sClient.Get(ctx, machineKey, testMachine)
				//gs.Expect(err).ShouldNot(HaveOccurred())
				//gs.Expect(testMachine.ResourceVersion).To(Equal(machineResourceVersion))
			}
		})
	}
}

func TestReconcileMachineNetwork(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	testCases := []struct {
		name          string
		machine       *machinev1.Machine
		vmIP          string
		vmStatus      ovirtsdk.VmStatus
		expectError   bool
		expectMachine func(*machinev1.Machine) error
	}{
		{
			name:        "Test Status: UP",
			machine:     stubEmptyMachine(),
			vmIP:        stubOvirtVMIP,
			vmStatus:    ovirtsdk.VMSTATUS_UP,
			expectError: false,
			expectMachine: func(m *machinev1.Machine) error {
				addresses := map[corev1.NodeAddress]int{
					{Address: machineName, Type: corev1.NodeInternalDNS}:  1,
					{Type: corev1.NodeInternalIP, Address: stubOvirtVMIP}: 1,
				}
				if m.Status.Addresses == nil {
					return errors.New("addresses field is not set for machine")
				}
				if len(m.Status.Addresses) != 2 {
					return fmt.Errorf("expected %v addresses but got %v", 2, len(m.Status.Addresses))
				}
				for _, a := range m.Status.Addresses {
					if _, ok := addresses[a]; !ok {
						return fmt.Errorf("addresse %v was not set", a)
					}
				}
				return nil
			},
		},
		{
			name:        "Test Status: MIGRATING",
			machine:     stubEmptyMachine(),
			vmIP:        stubOvirtVMIP,
			vmStatus:    ovirtsdk.VMSTATUS_MIGRATING,
			expectError: false,
			expectMachine: func(m *machinev1.Machine) error {
				addresses := map[corev1.NodeAddress]int{
					{Address: machineName, Type: corev1.NodeInternalDNS}:  1,
					{Type: corev1.NodeInternalIP, Address: stubOvirtVMIP}: 1,
				}
				if m.Status.Addresses == nil {
					return errors.New("addresses field is not set for machine")
				}
				if len(m.Status.Addresses) != 2 {
					return fmt.Errorf("expected %v addresses but got %v", 2, len(m.Status.Addresses))
				}
				for _, a := range m.Status.Addresses {
					if _, ok := addresses[a]; !ok {
						return fmt.Errorf("addresse %v was not set", a)
					}
				}
				return nil
			},
		},
		{
			name:        "Test Status: DOWN",
			machine:     stubEmptyMachine(),
			vmIP:        stubOvirtVMIP,
			vmStatus:    ovirtsdk.VMSTATUS_DOWN,
			expectError: false,
			expectMachine: func(m *machinev1.Machine) error {
				if len(m.Status.Addresses) != 0 {
					return fmt.Errorf("addresses field have %v addresses, expected 0 addresses",
						len(m.Status.Addresses))
				}
				return nil
			},
		},
		{
			name:        "Test Status: REBOOT_IN_PROGRESS",
			machine:     stubEmptyMachine(),
			vmIP:        stubOvirtVMIP,
			vmStatus:    ovirtsdk.VMSTATUS_REBOOT_IN_PROGRESS,
			expectError: true,
			expectMachine: func(m *machinev1.Machine) error {
				return errors.New("should have failed on error returned from reconcileMachineNetwork")
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			gs := NewWithT(t)
			ctx := context.TODO()

			// Setup Mock ovirtClient
			ovirtClient := mockOVirtClient.NewMockClient(mockCtrl)
			ovirtClient.EXPECT().FindVirtualMachineIP(gomock.Any(), gomock.Any()).Return(tc.vmIP, nil).AnyTimes()

			// Setup Mock k8sClient
			k8sClient := fake.NewClientBuilder().
				WithScheme(scheme.Scheme).
				WithRuntimeObjects(stubInfrastructure()).
				Build()

			ms := newMachineScope(
				ctx,
				ovirtClient,
				k8sClient,
				tc.machine,
				nil)

			// Patch the machine and check the expectation from the test case
			err = ms.reconcileMachineNetwork(ctx, tc.vmStatus, machineName, stubOvirtVMId)
			if tc.expectError == true {
				gs.Expect(err).Should(HaveOccurred())
			} else {
				gs.Expect(err).ShouldNot(HaveOccurred())
				gs.Expect(tc.expectMachine(ms.machine)).ShouldNot(HaveOccurred())
			}
		})
	}
}

func testMachineFields(m *machinev1.Machine, id string, status string,
	addresses map[corev1.NodeAddress]int) error {
	providerID := utils.ProviderIDPrefix + id
	annotations := map[string]string{
		InstanceStatusAnnotationKey: status,
		utils.OvirtIDAnnotationKey:  id,
	}
	if err := testMachineProviderID(m, providerID); err != nil {
		return err
	}
	if err := testMachineAnnotations(m, annotations); err != nil {
		return err
	}
	if err := testMachineAddresses(m, addresses); err != nil {
		return err
	}
	if err := testMachineStatus(m, id, status); err != nil {
		return err
	}
	return nil
}

func testMachineProviderID(m *machinev1.Machine, providerID string) error {
	if m.Spec.ProviderID == nil {
		return fmt.Errorf("providerID field wasn't set for machine")
	}
	if *m.Spec.ProviderID != providerID {
		return fmt.Errorf("providerID field got unexpected value, expected %s but got %s",
			providerID, *m.Spec.ProviderID)
	}
	return nil
}

func testMachineAnnotations(m *machinev1.Machine, annotations map[string]string) error {
	if m.ObjectMeta.Annotations == nil {
		return fmt.Errorf("Annotations wasn't set for machine")
	}
	for k, v := range annotations {
		if m.ObjectMeta.Annotations[k] != v {
			return fmt.Errorf("annotation %s got unexpected value, expected %s but got %s",
				k, v, m.ObjectMeta.Annotations[k])
		}
	}
	return nil
}

func testMachineAddresses(m *machinev1.Machine, addresses map[corev1.NodeAddress]int) error {
	if m.Status.Addresses == nil {
		return fmt.Errorf("Addresses wasn't set for machine")
	}
	if len(m.Status.Addresses) != len(addresses) {
		return fmt.Errorf("expected %v addreses on machine but found %v",
			len(addresses), len(m.Status.Addresses))
	}
	for _, a := range m.Status.Addresses {
		if _, ok := addresses[a]; !ok {
			return fmt.Errorf("got unexpected address %s", a)
		}
	}
	return nil
}

func testMachineStatus(m *machinev1.Machine, instanceID string, instanceState string) error {
	providerStatus, err := ovirtconfigv1.ProviderStatusFromRawExtension(m.Status.ProviderStatus)
	if err != nil {
		return fmt.Errorf("error converting raw extension to provider status: %w", err)
	}
	if *providerStatus.InstanceID != instanceID {
		return fmt.Errorf("expected providerStatus.InstanceID to be %v but found %v",
			instanceID, *providerStatus.InstanceID)
	}
	if *providerStatus.InstanceState != instanceState {
		return fmt.Errorf("expected providerStatus.InstanceState to be %v but found %v",
			instanceState, *providerStatus.InstanceState)
	}
	return nil
}
