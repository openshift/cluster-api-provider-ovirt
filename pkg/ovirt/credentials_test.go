//go:build unit

package ovirt_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/openshift/cluster-api-provider-ovirt/pkg/ovirt"
	apicorev1 "k8s.io/api/core/v1"
)

func TestFromK8sSecret(t *testing.T) {
	type testcase struct {
		name          string
		description   string
		inputSecret   *apicorev1.Secret
		expectedCreds *ovirt.Credentials
		expectedError bool
	}

	testcases := []testcase{
		{
			name:        "successful conversion",
			description: "successfully converts ovirt credentials in k8s secret to credentials struct",
			inputSecret: &apicorev1.Secret{
				Data: map[string][]byte{
					"ovirt_url":       []byte("https://ovirt-engine.test.com/api"),
					"ovirt_username":  []byte("user"),
					"ovirt_password":  []byte("topsecret"),
					"ovirt_cafile":    []byte(""),
					"ovirt_ca_bundle": []byte("ovirt.ca-bundle"),
					"ovirt_insecure":  []byte("false"),
				},
			},
			expectedCreds: &ovirt.Credentials{
				URL:      "https://ovirt-engine.test.com/api",
				Username: "user",
				Password: "topsecret",
				CAFile:   "",
				CABundle: "ovirt.ca-bundle",
				Insecure: false,
			},
			expectedError: false,
		},
		{
			name:        "fails parsing insecure bool",
			description: "failure on parsing the insecure boolean flag in ovirt credentials of k8s secret",
			inputSecret: &apicorev1.Secret{
				Data: map[string][]byte{
					"ovirt_url":       []byte("https://ovirt-engine.test.com/api"),
					"ovirt_username":  []byte("user"),
					"ovirt_password":  []byte("topsecret"),
					"ovirt_cafile":    []byte(""),
					"ovirt_ca_bundle": []byte("ovirt.ca-bundle"),
					"ovirt_insecure":  []byte("Flse"),
				},
			},
			expectedError: true,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			creds, err := ovirt.FromK8sSecret(tc.inputSecret)
			if tc.expectedError && err == nil {
				t.Fatal("expected error, but got <nil>")
			}
			if !tc.expectedError && err != nil {
				t.Fatalf("expected no error, but got '%v'", err)
			}

			if diff := cmp.Diff(tc.expectedCreds, creds); diff != "" {
				t.Fatalf("detected difference: %s", diff)
			}
		})
	}
}
