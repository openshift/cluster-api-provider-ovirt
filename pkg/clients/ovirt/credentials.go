/*
Copyright oVirt Authors
SPDX-License-Identifier: Apache-2.0
*/

package ovirt

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"strconv"
	"strings"

	apicorev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	UrlField      = "ovirt_url"
	UsernameField = "ovirt_username"
	PasswordField = "ovirt_password"
	CafileField   = "ovirt_cafile"
	InsecureField = "ovirt_insecure"
	CaBundleField = "ovirt_ca_bundle"
)

//TODO: remove CAFILE and use CABundle on CreateAPIConnection

// GetCredentialsSecret fetches the secret in the given namespace which contains the oVirt engine credentials
func GetCredentialsSecret(coreClient client.Client, namespace string, secretName string) (*Creds, error) {
	var credentialsSecret apicorev1.Secret
	key := client.ObjectKey{Namespace: namespace, Name: secretName}

	if err := coreClient.Get(context.Background(), key, &credentialsSecret); err != nil {
		if errors.IsNotFound(err) {
			return nil, fmt.Errorf(
				"error credentials secret %s not found in namespace %s: %w", secretName, namespace, err)
		}
		return nil, fmt.Errorf(
			"error getting credentials secret %s in namespace %s: %w", secretName, namespace, err)
	}

	o := Creds{}
	o.URL = string(credentialsSecret.Data[UrlField])
	o.Username = string(credentialsSecret.Data[UsernameField])
	o.Password = string(credentialsSecret.Data[PasswordField])
	o.CAFile = string(credentialsSecret.Data[CafileField])
	insecure, err := strconv.ParseBool(string(credentialsSecret.Data[InsecureField]))
	if err != nil {
		return nil, fmt.Errorf("failed to identify %s in credentials %w", InsecureField, err)
	}
	o.Insecure = insecure
	o.CABundle = string(credentialsSecret.Data[CaBundleField])

	// write CA bundle to a file if exist.
	// its best if we could mount the secret into a file,
	// but this controller deployment cannot
	if o.CABundle != "" {
		caFilePath, err := writeCA(strings.NewReader(o.CABundle))
		if err != nil {
			klog.Errorf("failed to extract and store the CA %s", err)
			return nil, err
		}
		o.CAFile = caFilePath
	}
	return &o, nil
}

// TODO: remove after we port to CABundle
func writeCA(source io.Reader) (string, error) {
	f, err := ioutil.TempFile("", "ovirt-ca-bundle")
	if err != nil {
		return "", err
	}
	defer f.Close()
	content, err := ioutil.ReadAll(source)
	if err != nil {
		return "", err
	}
	_, err = f.Write(content)
	if err != nil {
		return "", err
	}
	return f.Name(), nil
}
