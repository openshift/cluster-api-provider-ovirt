//go:build functional

package ovirt_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/openshift/cluster-api-provider-ovirt/pkg/ovirt"
	"github.com/openshift/cluster-api-provider-ovirt/pkg/utils"
	k8sCorev1 "k8s.io/api/core/v1"
	k8sMetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

func TestClientServiceCredentialUpdate(t *testing.T) {
	cfg, stopEnv := setupTestEnv(t)
	defer stopEnv()

	mgr, cancel := setupCtrlManager(t, cfg)
	defer cancel()

	k8sClient := mgr.GetClient()

	testcases := []struct {
		name        string
		updatables  []*mockUpdatable
		credentials []map[string]string
		verify      func([]*mockUpdatable, *ovirt.Credentials) error
	}{
		{
			name:        "update fails on invalid credentials",
			updatables:  []*mockUpdatable{{}},
			credentials: []map[string]string{{}},
			verify: func(updateables []*mockUpdatable, latestCreds *ovirt.Credentials) error {
				if len(updateables) != 1 {
					return fmt.Errorf("Unexpected number of updatables (%d)", len(updateables))
				}

				if updateables[0].currentCreds != nil {
					return fmt.Errorf("Expected credentials to be <nil>, but got: %v", updateables[0].currentCreds)
				}
				return nil
			},
		},
		{
			name:       "one updatable with one credentials",
			updatables: []*mockUpdatable{{}},
			credentials: []map[string]string{
				defaultCredentials,
			},
			verify: func(updateables []*mockUpdatable, latestCreds *ovirt.Credentials) error {
				if len(updateables) != 1 {
					return fmt.Errorf("Unexpected number of updatables (%d)", len(updateables))
				}

				return retry(3, func() error {
					if diff := cmp.Diff(updateables[0].currentCreds, latestCreds); diff != "" {
						return fmt.Errorf("Detected different credentials in updatable client: %s", diff)
					}
					return nil
				})
			},
		},
		{
			name:       "multiple updatable with multiple credential updates",
			updatables: []*mockUpdatable{{}, {}, {}},
			credentials: []map[string]string{
				defaultCredentials, updatedCredentials,
			},
			verify: func(updateables []*mockUpdatable, latestCreds *ovirt.Credentials) error {
				return retry(3, func() error {
					for _, updateable := range updateables {
						if diff := cmp.Diff(updateable.currentCreds, latestCreds); diff != "" {
							return fmt.Errorf("Detected different credentials in updatable client: %s", diff)
						}
					}
					return nil
				})
			},
		},
	}

	for i, testcase := range testcases {
		t.Run(testcase.name, func(tt *testing.T) {
			ctx, ctxCancel := context.WithCancel(context.Background())

			namespace := fmt.Sprintf("openshift-machine-api-%d", i)
			if err := createK8sNamespace(namespace, k8sClient); err != nil {
				tt.Fatalf("Unexpected error occurred while creating k8s namespace: %v", err)
			}

			oVirtClientService := ovirt.NewClientService(cfg, ovirt.SecretsToWatch{
				Namespace:  namespace,
				SecretName: utils.OvirtCloudCredsSecretName,
			})

			for i := range testcase.updatables {
				oVirtClientService.AddListener(testcase.updatables[i])
			}

			oVirtClientService.Run(ctx)

			for i, credential := range testcase.credentials {
				if i == 0 {
					if err := createOVirtCredentials(namespace, k8sClient, credential); err != nil {
						tt.Fatalf("Unexpected error occurred while creating secret: %v", err)
					}
				}
				if err := updateOVirtCredentials(namespace, k8sClient, credential); err != nil {
					tt.Fatalf("Unexpected error occurred while updating secret: %v", err)
				}
			}

			var latestCreds *ovirt.Credentials
			if len(testcase.credentials) > 0 {
				c := testcase.credentials[len(testcase.credentials)-1]
				latestCreds = &ovirt.Credentials{
					URL:      c["ovirt_url"],
					Username: c["ovirt_username"],
					Password: c["ovirt_password"],
					CAFile:   c["ovirt_cafile"],
					Insecure: c["ovirt_insecure"] == "true",
					CABundle: c["ovirt_ca_bundle"],
				}
			}
			if err := testcase.verify(testcase.updatables, latestCreds); err != nil {
				tt.Fatal(err)
			}

			ctxCancel()
			oVirtClientService.Shutdown(3 * time.Second)
		})
	}
}

func setupTestEnv(t *testing.T) (*rest.Config, func()) {
	testEnv := &envtest.Environment{}

	cfg, err := testEnv.Start()
	if err != nil {
		t.Fatalf("Unexpected error occurred while starting testEnv: %v", err)
	}
	stopEnv := func() {
		if err := testEnv.Stop(); err != nil {
			t.Fatalf("Unexpected error occurred while stopping testEnv: %v", err)
		}
	}

	return cfg, stopEnv
}

func setupCtrlManager(t *testing.T, cfg *rest.Config) (manager.Manager, func()) {
	mgr, err := manager.New(cfg, manager.Options{})
	if err != nil {
		t.Fatalf("Unexpected error occurred while creating manager: %v", err)
	}

	mgrCtx, cancel := context.WithCancel(context.Background())
	go func() {
		if err := mgr.Start(mgrCtx); err != nil {
			panic(fmt.Sprintf("Unexpected error occurred while running manager: %v", err))
		}
	}()

	return mgr, cancel
}

func createK8sNamespace(namespace string, k8sClient client.Client) error {
	testNamespace := &k8sCorev1.Namespace{
		ObjectMeta: k8sMetav1.ObjectMeta{
			Name: namespace,
		},
	}
	return k8sClient.Create(context.Background(), testNamespace)
}

func createOVirtCredentials(namespace string, k8sClient client.Client, data map[string]string) error {
	ovirtCredentials := &k8sCorev1.Secret{
		ObjectMeta: k8sMetav1.ObjectMeta{
			Name:      utils.OvirtCloudCredsSecretName,
			Namespace: namespace,
		},
		StringData: data,
	}

	return k8sClient.Create(context.Background(), ovirtCredentials)
}

func updateOVirtCredentials(namespace string, k8sClient client.Client, data map[string]string) error {
	ovirtCredentials := &k8sCorev1.Secret{
		ObjectMeta: k8sMetav1.ObjectMeta{
			Name:      utils.OvirtCloudCredsSecretName,
			Namespace: namespace,
		},
		StringData: data,
	}

	return k8sClient.Update(context.Background(), ovirtCredentials)
}

func retry(numOfRetries int, check func() error) error {
	var err error
	for i := 0; i < numOfRetries; i++ {
		time.Sleep(1 * time.Second)
		if e := check(); e != nil {
			err = e
			continue
		}
		err = nil
		break
	}

	return err

}

type mockUpdatable struct {
	currentCreds *ovirt.Credentials
}

func (u *mockUpdatable) SetCredentials(newCreds *ovirt.Credentials) {
	u.currentCreds = newCreds
}

var defaultCredentials = map[string]string{
	"ovirt_url":       "http://localhost/ovirt-engine/api",
	"ovirt_username":  "user@internal",
	"ovirt_password":  "topsecret",
	"ovirt_cafile":    "",
	"ovirt_insecure":  "true",
	"ovirt_ca_bundle": "",
}

var updatedCredentials = map[string]string{
	"ovirt_url":       "http://new.localhost/ovirt-engine/api",
	"ovirt_username":  "user@internal",
	"ovirt_password":  "verysecret",
	"ovirt_cafile":    "",
	"ovirt_insecure":  "false",
	"ovirt_ca_bundle": "",
}
