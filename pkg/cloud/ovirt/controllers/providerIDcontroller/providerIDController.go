package providerIDcontroller

import (
	"context"
	"fmt"
	"github.com/openshift/cluster-api-provider-ovirt/pkg/cloud/ovirt"
	common "github.com/openshift/cluster-api-provider-ovirt/pkg/cloud/ovirt/controllers"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/klogr"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

var _ reconcile.Reconciler = &providerIDController{}

type providerIDController struct {
	common.BaseController
	listNodesByFieldFunc func(key, value string) ([]corev1.Node, error)
}

func (r *providerIDController) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	r.Log.Info("ProviderIDController, Reconciling", "Node", request.NamespacedName)

	// Fetch the Node instance
	node := corev1.Node{}
	err := r.Client.Get(ctx, request.NamespacedName, &node)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, fmt.Errorf("error getting node: %v", err)
	}
	if node.Spec.ProviderID == "" {
		r.Log.Info("Node spec.ProviderID is empty, fetching from ovirt", "node", node.Name)
		id, err := r.fetchOvirtVmID(node.Name)
		if err != nil {
			return reconcile.Result{}, fmt.Errorf("failed getting VM from oVirt: %v", err)
		}
		if id == "" {
			r.Log.Info("Node not found in oVirt", "node", node.Name)
			return reconcile.Result{}, nil
		}
		node.Spec.ProviderID = ovirt.ProviderIDPrefix + id
		err = r.Client.Update(ctx, &node)
		if err != nil {
			return reconcile.Result{}, fmt.Errorf("failed updating node %s: %v", node.Name, err)
		}
	}
	return reconcile.Result{}, nil
}

func (r *providerIDController) fetchOvirtVmID(nodeName string) (string, error) {
	c, err := r.GetConnection(common.NAMESPACE, common.CREDENTIALS_SECRET)
	if err != nil {
		return "", err
	}
	send, err := c.SystemService().VmsService().List().Search(fmt.Sprintf("name=%s", nodeName)).Send()
	if err != nil {
		r.Log.Error(err, "Error occurred will searching VM", "VM name", nodeName)
		return "", err
	}
	vms := send.MustVms().Slice()
	if l := len(vms); l > 1 {
		return "", fmt.Errorf("expected to get 1 VM but got %v", l)
	} else if l == 0 {
		return "", nil
	}
	return vms[0].MustId(), nil
}

func Add(mgr manager.Manager, opts manager.Options) error {
	pic, err := NewProviderIDController(mgr)

	if err != nil {
		return fmt.Errorf("error building ProviderIDController: %v", err)
	}

	c, err := controller.New("ProviderIDController", mgr, controller.Options{Reconciler: pic})
	if err != nil {
		return err
	}

	//Watch node changes
	err = c.Watch(&source.Kind{Type: &corev1.Node{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	return nil
}

func NewProviderIDController(mgr manager.Manager) (*providerIDController, error) {
	log.SetLogger(klogr.New())
	return &providerIDController{
		BaseController: common.BaseController{
			Log:    log.Log.WithName("controllers").WithName("providerIDController"),
			Client: mgr.GetClient(),
		},
	}, nil
}
