package nodeController

import (
	"context"
	"fmt"
	"strings"

	common "github.com/openshift/cluster-api-provider-ovirt/pkg/controllers"
	"github.com/openshift/cluster-api-provider-ovirt/pkg/utils"
	ovirtC "github.com/ovirt/go-ovirt-client"
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

var _ reconcile.Reconciler = &nodeController{}

const (
	retryIntervalVMDownSec = 60
	controllerName         = "NodeController"
)

type nodeController struct {
	common.BaseController
}

// Reconcile implements controller runtime Reconciler interface.
func (r *nodeController) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	r.Log.Info("NodeController, Reconciling:", "Node", request.NamespacedName)
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
		return common.ResultRequeueDefault(), errors.Wrap(err, "error getting node requeue")
	}
	// Check if the node has a ovirt ProviderID set, if not then ignore it
	if !strings.Contains(node.Spec.ProviderID, utils.ProviderIDPrefix) {
		return common.ResultNoRequeue(), nil
	}
	ovirtClient, err := r.GetoVirtClient()
	if err != nil {
		return common.ResultRequeueDefault(),
			errors.Wrap(err, "error getting connection to oVirt, requeue")
	}

	vm, err := ovirtClient.GetVMByName(node.Name)
	if err != nil {
		if ovirtC.HasErrorCode(err, ovirtC.ENotFound) {
			r.Log.Info(
				"Deleting Node from cluster since it has been removed from the oVirt engine",
				"Node", node.Name)
			if err := r.Client.Delete(ctx, &node); err != nil {
				return common.ResultRequeueDefault(), fmt.Errorf("error deleting node: %v, error: %w", node.Name, err)
			}
		}
		return common.ResultRequeueDefault(),
			fmt.Errorf("failed getting VM %s from oVirt, requeue: %w", node.Name, err)
	} else if vm.Status() == ovirtC.VMStatusDown {
		r.Log.Info("Node VM status is Down, requeuing for 1 min",
			"Node", node.Name, "Vm Status", ovirtC.VMStatusDown)
		return common.ResultRequeueAfter(retryIntervalVMDownSec), nil
	} else {
		hostID := vm.HostID()
		zoneLabel, ok := node.Labels["topology.kubernetes.io/zone"]
		needsUpdate := false
		if hostID != nil {
			if !ok || zoneLabel != *hostID {
				// Set host ID
				node.Labels["topology.kubernetes.io/zone"] = *hostID
				needsUpdate = true
			}
		} else {
			if ok {
				delete(node.Labels, "topology.kubernetes.io/zone")
				needsUpdate = true
			}
		}
		if needsUpdate {
			if err := r.Client.Update(ctx, &node); err != nil {
				return common.ResultRequeueDefault(),
					fmt.Errorf("failed updating node %s object in OpenShift, requeue: %w", node.Name, err)
			}
		}
	}
	return common.ResultNoRequeue(), nil
}

// Creates a new Node Controller and adds it to the manager
// The Node Controller watches changes on Node objects in the cluster
func Add(mgr manager.Manager) error {
	nc := NewNodeController(mgr)
	c, err := controller.New(controllerName, mgr, controller.Options{Reconciler: nc})
	if err != nil {
		return errors.Wrap(err, "error creating node controller")
	}
	//Watch node changes
	err = c.Watch(&source.Kind{Type: &corev1.Node{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return errors.Wrap(err, "error setting up watch on node changes")
	}

	return nil
}

func NewNodeController(mgr manager.Manager) *nodeController {
	log.SetLogger(klogr.New())
	return &nodeController{
		BaseController: common.BaseController{
			Log:    log.Log.WithName("controllers").WithName(controllerName),
			Client: mgr.GetClient(),
		},
	}
}
