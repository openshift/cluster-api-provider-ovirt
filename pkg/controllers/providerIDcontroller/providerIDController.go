package providerIDcontroller

import (
	"context"
	"fmt"

	ovirtClient "github.com/openshift/cluster-api-provider-ovirt/pkg/clients/ovirt"
	common "github.com/openshift/cluster-api-provider-ovirt/pkg/controllers"
	"github.com/openshift/cluster-api-provider-ovirt/pkg/utils"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/klogr"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

var _ reconcile.Reconciler = &providerIDController{}

const (
	controllerName = "ProviderIDController"
)

type providerIDController struct {
	common.BaseController
}

// Reconcile implements controller runtime Reconciler interface.
func (r *providerIDController) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	r.Log.Info("ProviderIDController, Reconciling", "Node", request.NamespacedName)

	// Fetch the Node instance
	node := corev1.Node{}
	err := r.Client.Get(ctx, request.NamespacedName, &node)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			return common.ResultNoRequeue(), nil
		}
		// Error reading the object - requeue the request.
		return common.ResultRequeueDefault(), errors.Wrap(err, "error getting node: %v")
	}
	if node.Spec.ProviderID == "" {
		r.Log.Info("Node spec.ProviderID is empty, fetching from ovirt", "node", node.Name)
		id, err := r.fetchOvirtVmID(node.Name)
		if err != nil {
			return common.ResultRequeueDefault(),
				fmt.Errorf("failed getting VM %s from oVirt requeue: %w", node.Name, err)
		}
		if id == "" {
			r.Log.Info("Node not found in oVirt", "node", node.Name)
			return common.ResultNoRequeue(), nil
		}
		node.Spec.ProviderID = utils.ProviderIDPrefix + id
		err = r.Client.Update(ctx, &node)
		if err != nil {
			return common.ResultRequeueDefault(), fmt.Errorf("failed updating node %s: %w", node.Name, err)
		}
	}
	return common.ResultNoRequeue(), nil
}

// fetchOvirtVmID returns the id of the oVirt VM which correlates to the node
func (r *providerIDController) fetchOvirtVmID(nodeName string) (string, error) {
	c, err := r.GetConnection()
	if err != nil {
		return "", errors.Wrap(err, "error getting connection to oVirt")
	}
	ovirtC := ovirtClient.NewOvirtClient(c)

	vm, err := ovirtC.GetVMByName(nodeName)
	if err != nil {
		r.Log.Error(err, "Error occurred will searching VM", "VM name", nodeName)
		return "", fmt.Errorf("failed getting VM %s from oVirt: %w", nodeName, err)
	}
	if vm == nil {
		return "", nil
	}
	return vm.MustId(), nil
}

// Creates a new ProviderID Controller and adds it to the manager
// The ProviderID Controller watches changes on Node objects in the cluster
func Add(mgr manager.Manager) error {
	pic := NewProviderIDController(mgr)
	c, err := controller.New(controllerName, mgr, controller.Options{Reconciler: pic})
	if err != nil {
		return errors.Wrap(err, "error setting up watch on node changes")
	}
	//Watch node changes
	err = c.Watch(&source.Kind{Type: &corev1.Node{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}
	return nil
}

func NewProviderIDController(mgr manager.Manager) *providerIDController {
	log.SetLogger(klogr.New())
	return &providerIDController{
		BaseController: common.BaseController{
			Log:    log.Log.WithName("controllers").WithName(controllerName),
			Client: mgr.GetClient(),
		},
	}
}
