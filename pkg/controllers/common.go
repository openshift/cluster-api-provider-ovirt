package ovirt

import (
	"fmt"
	"time"

	"github.com/go-logr/logr"
	ovirtClient "github.com/openshift/cluster-api-provider-ovirt/pkg/clients/ovirt"
	"github.com/openshift/cluster-api-provider-ovirt/pkg/utils"
	ovirtsdk "github.com/ovirt/go-ovirt"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const requeueDefaultTime = 30 * time.Second

func ResultRequeueAfter(sec int) reconcile.Result {
	return reconcile.Result{RequeueAfter: time.Duration(sec) * time.Second}
}

func ResultRequeueDefault() reconcile.Result {
	return reconcile.Result{RequeueAfter: requeueDefaultTime}
}

func ResultNoRequeue() reconcile.Result {
	return reconcile.Result{Requeue: false}
}

type BaseController struct {
	Log             logr.Logger
	Client          client.Client
	ovirtConnection *ovirtsdk.Connection
}

func (b *BaseController) GetConnection() (*ovirtsdk.Connection, error) {
	if b.ovirtConnection == nil || b.ovirtConnection.Test() != nil {
		creds, err := ovirtClient.GetCredentialsSecret(b.Client, utils.NAMESPACE, utils.OvirtCloudCredsSecretName)
		if err != nil {
			return nil, fmt.Errorf("failed getting credentials for namespace %s, %w", utils.NAMESPACE, err)
		}
		// session expired or some other error, re-login.
		b.ovirtConnection, err = ovirtClient.CreateAPIConnection(creds)
		if err != nil {
			return nil, fmt.Errorf("failed creating ovirt connection %w", err)
		}
	}
	return b.ovirtConnection, nil
}
