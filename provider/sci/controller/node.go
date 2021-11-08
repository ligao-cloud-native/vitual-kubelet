package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/fsnotify/fsnotify"
	"github.com/ligao-cloud-native/vitual-kubelet/provider"
	"github.com/ligao-cloud-native/vitual-kubelet/provider/sci"
	pkgerrors "github.com/pkg/errors"
	coordinationv1 "k8s.io/api/coordination/v1beta1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/typed/coordination/v1beta1"
	v1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	"k8s.io/utils/pointer"
	"sync"
	"time"
)

const (
	// Annotation with the JSON-serialized last applied node conditions. Based on kube ctl apply. Used to calculate
	// the three-way patch
	virtualKubeletLastNodeAppliedNodeStatus = "virtual-kubelet.io/last-applied-node-status"
	virtualKubeletLastNodeAppliedObjectMeta = "virtual-kubelet.io/last-applied-object-meta"
)

// NodeController deals with creating and managing a node object in Kubernetes.
// It can register a node with Kubernetes and periodically update its status.
// NodeController manages a single node entity.
type NodeController struct { // nolint:golint
	p provider.NodeProvider

	// serverNode must be updated each time it is updated in API Server
	serverNodeLock sync.Mutex
	serverNode     *corev1.Node
	nodes          v1.NodeInterface

	disableLease bool
	leases       v1beta1.LeaseInterface

	pingInterval   time.Duration
	statusInterval time.Duration
	chStatusUpdate chan *corev1.Node

	nodeStatusUpdateErrorHandler ErrorHandler

	// chReady is closed once the controller is ready to start the control loop
	chReady chan struct{}
	// chDone is closed once the control loop has exited
	chDone chan struct{}
	errMu  sync.Mutex
	err    error

	nodePingController *nodePingController
	pingTimeout        *time.Duration

	group wait.Group
}

// ErrorHandler is a type of function used to allow callbacks for handling errors.
// It is expected that if a nil error is returned that the error is handled and
// progress can continue (or a retry is possible).
type ErrorHandler func(context.Context, error) error

// The default intervals used for lease and status updates.
const (
	DefaultPingInterval         = 10 * time.Second
	DefaultStatusUpdateInterval = 1 * time.Minute
)

// NewNodeController creates a new node controller.
// This does not have any side-effects on the system or kubernetes.
//
// Use the node's `Run` method to register and run the loops to update the node
// in Kubernetes.
//
// Note: When if there are multiple NodeControllerOpts which apply against the same
// underlying options, the last NodeControllerOpt will win.
func NewNodeController(p provider.NodeProvider, nodeClient *corev1.Node, nodes v1.NodeInterface, opts ...NodeControllerOpt) (*NodeController, error) {
	n := &NodeController{
		p:          p,
		serverNode: nodeClient,
		nodes:      nodes,
		chReady:    make(chan struct{}),
		chDone:     make(chan struct{}),
	}
	for _, o := range opts {
		if err := o(n); err != nil {
			return nil, pkgerrors.Wrap(err, "error applying node option")
		}
	}

	if n.pingInterval == time.Duration(0) {
		n.pingInterval = DefaultPingInterval
	}
	if n.statusInterval == time.Duration(0) {
		n.statusInterval = DefaultStatusUpdateInterval
	}

	n.nodePingController = newNodePingController(n.p, n.pingInterval, n.pingTimeout)

	return n, nil
}

// NodeControllerOpt are the functional options used for configuring a node
type NodeControllerOpt func(*NodeController) error // nolint:golint

// Run registers the node in kubernetes and starts loops for updating the node
// status in Kubernetes.
//
// The node status must be updated periodically in Kubernetes to keep the node
// active. Newer versions of Kubernetes support node leases, which are
// essentially light weight pings. Older versions of Kubernetes require updating
// the node status periodically.
//
// If Kubernetes supports node leases this will use leases with a much slower
// node status update (because some things still expect the node to be updated
// periodically), otherwise it will only use node status update with the configured
// ping interval.
func (n *NodeController) Run(ctx context.Context) (retErr error) {
	// 执行provider的NotifyNodeStatus方法
	n.chStatusUpdate = make(chan *corev1.Node, 1)
	n.p.NotifyNodeStatus(ctx, func(node *corev1.Node) {
		n.chStatusUpdate <- node
	})

	providerNode := n.serverNode.DeepCopy()
	if err := n.ensureNode(ctx, providerNode); err != nil {
		return err
	}

	if n.leases == nil {
		n.disableLease = true
		return n.controlLoop(ctx, providerNode)
	}

	_, err := ensureLease(n.leases, newLease(ctx, n.serverNode, nil, n.pingInterval))
	if err != nil {
		if !errors.IsNotFound(err) {
			return pkgerrors.Wrap(err, "error creating node lease")
		}
		klog.Errorf("node lease not supported, err: %v", err)
		n.disableLease = true
	}
	klog.Info("create node lease")

	return n.controlLoop(ctx, providerNode)

}

// 动态监听配置文件。update vk node resource, as cpu, memory, pod
func (n *NodeController) NotifyVKNodeResource(configFile string, node *corev1.Node, kubeClient kubernetes.Interface) {
	watch, err := fsnotify.NewWatcher()
	if err != nil {
		klog.Error("create fsnotify watcher err: %v", err)
	}
	if err = watch.Add(configFile); err != nil {
		klog.Errorf("watch provider config file error: %v", configFile)
	}

	go func() {
		for {
			select {
			case ev := <-watch.Events:
				klog.Infof("the operator: %s", ev.Op)
				if (ev.Op&fsnotify.Write) == fsnotify.Write ||
					(ev.Op&fsnotify.Remove) == fsnotify.Remove {

					config, err := sci.LoadConfig(configFile, node.Name)
					if err != nil {
						klog.Errorf("loda node config err: %v", err)
					}

					oldNode, _ := kubeClient.CoreV1().Nodes().Get(context.TODO(), node.Name, metav1.GetOptions{})
					oldNode.Status.Capacity = corev1.ResourceList{
						"cpu":    resource.MustParse(config.Cpu),
						"memory": resource.MustParse(config.Memory),
						"pod":    resource.MustParse(config.Pods),
					}
					oldNode.Status.Allocatable = oldNode.Status.Capacity

					n.chStatusUpdate <- oldNode

					err = watch.Add(configFile)
				}
			case err := <-watch.Errors:
				if err != nil {
					klog.Error(err)
				}
			}
		}
	}()

}

func (n *NodeController) controlLoop(ctx context.Context, providerNode *corev1.Node) error {
	pingTimer := time.NewTimer(n.pingInterval)
	defer pingTimer.Stop()

	statusTimer := time.NewTimer(n.statusInterval)
	defer statusTimer.Stop()

	timerResetDuration := n.statusInterval

	if n.disableLease {
		timerResetDuration = n.pingInterval
		if !statusTimer.Stop() {
			<-statusTimer.C
		}
	}

	group := &wait.Group{}
	group.StartWithContext(ctx, n.nodePingController.Run)

	loop := func() bool {
		select {
		case <-ctx.Done():
			return true
		case updated := <-n.chStatusUpdate:
			var t *time.Timer
			if n.disableLease {
				t = pingTimer
			} else {
				t = statusTimer
			}
			klog.Info("Received node status update")

			if !t.Stop() {
				t.Stop()
			}

			providerNode.Status = updated.Status
			providerNode.ObjectMeta.Annotations = updated.Annotations
			providerNode.ObjectMeta.Labels = updated.Labels
			if err := n.updateStatus(ctx, providerNode, false); err != nil {
				klog.Error("Error handling node status update")
			}
			t.Reset(timerResetDuration)

		case <-statusTimer.C:
			if err := n.updateStatus(ctx, providerNode, false); err != nil {
				klog.Errorf("Error handling node status update, %v", err)
			}
			statusTimer.Reset(n.statusInterval)
		case <-pingTimer.C:
			if err := n.handlePing(ctx, providerNode); err != nil {
				klog.Errorf("Error handling node ping update, %v", err)
			} else {
				klog.Info("successful node ping")
			}
			pingTimer.Reset(n.statusInterval)
		}
		return false
	}

	close(n.chReady)
	for {
		shouldTerminate := loop()
		if shouldTerminate {
			return nil
		}
	}

}

func (n *NodeController) handlePing(ctx context.Context, providerNode *corev1.Node) (retErr error) {
	result, err := n.nodePingController.getResult(ctx)
	if err != nil {
		return pkgerrors.Wrap(err, "error while fetching result of node ping")
	}

	if result.error != nil {
		return pkgerrors.Wrap(err, "node ping returned error on ping")
	}

	if n.disableLease {
		return n.updateStatus(ctx, providerNode, false)
	}

	return n.updateLease(ctx)
}

func (n *NodeController) updateLease(ctx context.Context) error {
	_, err := updateNodeLease(ctx, n.leases, newLease(ctx, n.serverNode, nil, n.pingInterval))
	if err != nil {
		return err
	}

	return nil
}

func (n *NodeController) ensureNode(ctx context.Context, providerNode *corev1.Node) (err error) {
	err = n.updateStatus(ctx, providerNode, true)
	if err == nil || !errors.IsNotFound(err) {
		return err
	}

	n.serverNodeLock.Lock()
	serverNode := n.serverNode
	n.serverNodeLock.Unlock()
	node, err := n.nodes.Create(ctx, serverNode, metav1.CreateOptions{})
	if err != nil {
		return pkgerrors.Wrap(err, "error registering node with kubernetes")
	}

	n.serverNodeLock.Lock()
	n.serverNode = node
	n.serverNodeLock.Unlock()
	// Bad things will happen if the node is deleted in k8s and recreated by someone else
	// we rely on this persisting
	providerNode.ObjectMeta.Name = node.Name
	providerNode.ObjectMeta.Namespace = node.Namespace
	providerNode.ObjectMeta.UID = node.UID

	return nil
}

func (n *NodeController) updateStatus(ctx context.Context, providerNode *corev1.Node, skipErrorCb bool) (err error) {
	if result, err := n.nodePingController.getResult(ctx); err != nil {
		return err
	} else if result.error != nil {
		return fmt.Errorf("Not updating node status because node ping failed: %w", result.error)
	}

	updateNodeStatusHeartbeat(providerNode)

	node, err := updateNodeStatus(ctx, n.nodes, providerNode)
	if err != nil {
		if skipErrorCb || n.nodeStatusUpdateErrorHandler == nil {
			return err
		}
		if err := n.nodeStatusUpdateErrorHandler(ctx, err); err != nil {
			return err
		}

		// This might have recreated the node, which may cause problems with our leases until a node update succeeds
		node, err = updateNodeStatus(ctx, n.nodes, providerNode)
		if err != nil {
			return err
		}
	}

	n.serverNodeLock.Lock()
	n.serverNode = node
	n.serverNodeLock.Unlock()
	return nil
}

func updateNodeStatusHeartbeat(n *corev1.Node) {
	now := metav1.NewTime(time.Now())
	for i := range n.Status.Conditions {
		n.Status.Conditions[i].LastHeartbeatTime = now
	}
}

// updateNodeStatus triggers an update to the node status in Kubernetes.
// It first fetches the current node details and then sets the status according
// to the passed in node object.
//
// If you use this function, it is up to you to synchronize this with other operations.
// This reduces the time to second-level precision.
func updateNodeStatus(ctx context.Context, nodes v1.NodeInterface, nodeFromProvider *corev1.Node) (_ *corev1.Node, retErr error) {
	var updatedNode *corev1.Node
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		apiServerNode, err := nodes.Get(ctx, nodeFromProvider.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		klog.Info("got node from api server")

		patchBytes, err := prepareThreewayPatchBytesForNodeStatus(nodeFromProvider, apiServerNode)
		if err != nil {
			return pkgerrors.Wrap(err, "Cannot generate patch")
		}

		updatedNode, err = nodes.Patch(ctx, nodeFromProvider.Name, types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{}, "status")
		if err != nil {
			// We cannot wrap this error because the kubernetes error module doesn't understand wrapping
			klog.Warning("Failed to patch node status")
			return err
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	klog.Infof("update node sutatus, node.resource.Version(%), node.Status.Conditions(%v)",
		updatedNode.ResourceVersion, updatedNode.Status.Conditions)
	return updatedNode, nil

}

func prepareThreewayPatchBytesForNodeStatus(nodeFromProvider, apiServerNode *corev1.Node) ([]byte, error) {
	// We use these two values to calculate a patch. We use a three-way patch in order to avoid
	// causing state regression server side. For example, let's consider the scenario:
	/*
		UML Source:
		@startuml
		participant VK
		participant K8s
		participant ExternalUpdater
		note right of VK: Updates internal node conditions to [A, B]
		VK->K8s: Patch Upsert [A, B]
		note left of K8s: Node conditions are [A, B]
		ExternalUpdater->K8s: Patch Upsert [C]
		note left of K8s: Node Conditions are [A, B, C]
		note right of VK: Updates internal node conditions to [A]
		VK->K8s: Patch: delete B, upsert A\nThis is where things go wrong,\nbecause the patch is written to replace all node conditions\nit overwrites (drops) [C]
		note left of K8s: Node Conditions are [A]\nNode condition C from ExternalUpdater is no longer present
		@enduml
			     ,--.                                                        ,---.          ,---------------.
		     |VK|                                                        |K8s|          |ExternalUpdater|
		     `+-'                                                        `-+-'          `-------+-------'
		      |  ,------------------------------------------!.             |                    |
		      |  |Updates internal node conditions to [A, B]|_\            |                    |
		      |  `--------------------------------------------'            |                    |
		      |                     Patch Upsert [A, B]                    |                    |
		      | ----------------------------------------------------------->                    |
		      |                                                            |                    |
		      |                              ,--------------------------!. |                    |
		      |                              |Node conditions are [A, B]|_\|                    |
		      |                              `----------------------------'|                    |
		      |                                                            |  Patch Upsert [C]  |
		      |                                                            | <-------------------
		      |                                                            |                    |
		      |                           ,-----------------------------!. |                    |
		      |                           |Node Conditions are [A, B, C]|_\|                    |
		      |                           `-------------------------------'|                    |
		      |  ,---------------------------------------!.                |                    |
		      |  |Updates internal node conditions to [A]|_\               |                    |
		      |  `-----------------------------------------'               |                    |
		      | Patch: delete B, upsert A                                  |                    |
		      | This is where things go wrong,                             |                    |
		      | because the patch is written to replace all node conditions|                    |
		      | it overwrites (drops) [C]                                  |                    |
		      | ----------------------------------------------------------->                    |
		      |                                                            |                    |
		     ,----------------------------------------------------------!. |                    |
		     |Node Conditions are [A]                                   |_\|                    |
		     |Node condition C from ExternalUpdater is no longer present  ||                    |
		     `------------------------------------------------------------'+-.          ,-------+-------.
		     |VK|                                                        |K8s|          |ExternalUpdater|
		     `--'                                                        `---'          `---------------'
	*/
	// In order to calculate that last patch to delete B, and upsert C, we need to know that C was added by
	// "someone else". So, we keep track of our last applied value, and our current value. We then generate
	// our patch based on the diff of these and *not* server side state.
	oldVKStatus, ok1 := apiServerNode.Annotations[virtualKubeletLastNodeAppliedNodeStatus]
	oldVKObjectMeta, ok2 := apiServerNode.Annotations[virtualKubeletLastNodeAppliedObjectMeta]

	oldNode := corev1.Node{}
	// Check if there were no labels, which means someone else probably created the node, or this is an upgrade. Either way, we will consider
	// ourselves as never having written the node object before, so oldNode will be left empty. We will overwrite values if
	// our new node conditions / status / objectmeta have them
	if ok1 && ok2 {
		err := json.Unmarshal([]byte(oldVKObjectMeta), &oldNode.ObjectMeta)
		if err != nil {
			return nil, pkgerrors.Wrapf(err, "Cannot unmarshal old node object metadata (key: %q): %q", virtualKubeletLastNodeAppliedObjectMeta, oldVKObjectMeta)
		}
		err = json.Unmarshal([]byte(oldVKStatus), &oldNode.Status)
		if err != nil {
			return nil, pkgerrors.Wrapf(err, "Cannot unmarshal old node status (key: %q): %q", virtualKubeletLastNodeAppliedNodeStatus, oldVKStatus)
		}
	}

	// newNode is the representation of the node the provider "wants"
	newNode := corev1.Node{}
	newNode.ObjectMeta = simplestObjectMetadata(&apiServerNode.ObjectMeta, &nodeFromProvider.ObjectMeta)
	nodeFromProvider.Status.DeepCopyInto(&newNode.Status)

	// virtualKubeletLastNodeAppliedObjectMeta must always be set before virtualKubeletLastNodeAppliedNodeStatus,
	// otherwise we capture virtualKubeletLastNodeAppliedNodeStatus in virtualKubeletLastNodeAppliedObjectMeta,
	// which is wrong
	virtualKubeletLastNodeAppliedObjectMetaBytes, err := json.Marshal(newNode.ObjectMeta)
	if err != nil {
		return nil, pkgerrors.Wrap(err, "Cannot marshal object meta from provider")
	}
	newNode.Annotations[virtualKubeletLastNodeAppliedObjectMeta] = string(virtualKubeletLastNodeAppliedObjectMetaBytes)

	virtualKubeletLastNodeAppliedNodeStatusBytes, err := json.Marshal(newNode.Status)
	if err != nil {
		return nil, pkgerrors.Wrap(err, "Cannot marshal node status from provider")
	}
	newNode.Annotations[virtualKubeletLastNodeAppliedNodeStatus] = string(virtualKubeletLastNodeAppliedNodeStatusBytes)
	// Generate three way patch from oldNode -> newNode, without deleting the changes in api server
	// Should we use the Kubernetes serialization / deserialization libraries here?
	oldNodeBytes, err := json.Marshal(oldNode)
	if err != nil {
		return nil, pkgerrors.Wrap(err, "Cannot marshal old node bytes")
	}
	newNodeBytes, err := json.Marshal(newNode)
	if err != nil {
		return nil, pkgerrors.Wrap(err, "Cannot marshal new node bytes")
	}
	apiServerNodeBytes, err := json.Marshal(apiServerNode)
	if err != nil {
		return nil, pkgerrors.Wrap(err, "Cannot marshal api server node")
	}
	schema, err := strategicpatch.NewPatchMetaFromStruct(&corev1.Node{})
	if err != nil {
		return nil, pkgerrors.Wrap(err, "Cannot get patch schema from node")
	}
	return strategicpatch.CreateThreeWayMergePatch(oldNodeBytes, newNodeBytes, apiServerNodeBytes, schema, true)
}

// Provides the simplest object metadata to match the previous object. Name, namespace, UID. It copies labels and
// annotations from the second object if defined. It exempts the patch metadata
func simplestObjectMetadata(baseObjectMeta, objectMetaWithLabelsAndAnnotations *metav1.ObjectMeta) metav1.ObjectMeta {
	ret := metav1.ObjectMeta{
		Namespace:   baseObjectMeta.Namespace,
		Name:        baseObjectMeta.Name,
		UID:         baseObjectMeta.UID,
		Annotations: make(map[string]string),
	}
	if objectMetaWithLabelsAndAnnotations != nil {
		if objectMetaWithLabelsAndAnnotations.Labels != nil {
			ret.Labels = objectMetaWithLabelsAndAnnotations.Labels
		} else {
			ret.Labels = make(map[string]string)
		}
		if objectMetaWithLabelsAndAnnotations.Annotations != nil {
			// We want to copy over all annotations except the special embedded ones.
			for key := range objectMetaWithLabelsAndAnnotations.Annotations {
				if key == virtualKubeletLastNodeAppliedNodeStatus || key == virtualKubeletLastNodeAppliedObjectMeta {
					continue
				}
				ret.Annotations[key] = objectMetaWithLabelsAndAnnotations.Annotations[key]
			}
		}
	}
	return ret
}

func updateNodeLease(ctx context.Context, leaseClient v1beta1.LeaseInterface, baseLease *coordinationv1.Lease) (*coordinationv1.Lease, error) {
	l, err := leaseClient.Update(context.TODO(), baseLease, metav1.UpdateOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			klog.Infof("lease not found")
			l, err = ensureLease(leaseClient, baseLease)
		}
		if err != nil {
			return nil, err
		}
		klog.Infof("create new lease")
	} else {
		klog.Infof("update lease")
	}

	return l, nil
}

func newLease(ctx context.Context, serverNode *corev1.Node, base *coordinationv1.Lease, leaseRenewalInterval time.Duration) *coordinationv1.Lease {
	// Use the bare minimum set of fields; other fields exist for debugging/legacy,
	// but we don't need to make node heartbeats more complicated by using them.
	var lease *coordinationv1.Lease
	if base == nil {
		lease = &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{
				Name:      serverNode.Name,
				Namespace: corev1.NamespaceNodeLease,
			},
			Spec: coordinationv1.LeaseSpec{
				HolderIdentity:       pointer.StringPtr(serverNode.Name),
				LeaseDurationSeconds: pointer.Int32Ptr(int32(leaseRenewalInterval.Seconds()) * 25),
			},
		}
	} else {
		lease = base.DeepCopy()
	}
	lease.Spec.RenewTime = &metav1.MicroTime{Time: time.Now()}

	// Setting owner reference needs node's UID. Note that it is different from
	// kubelet.nodeRef.UID. When lease is initially created, it is possible that
	// the connection between master and node is not ready yet. So try to set
	// owner reference every time when renewing the lease, until successful.
	if l := len(lease.OwnerReferences); l == 0 {
		lease.OwnerReferences = []metav1.OwnerReference{
			{
				APIVersion: corev1.SchemeGroupVersion.WithKind("Node").Version,
				Kind:       corev1.SchemeGroupVersion.WithKind("Node").Kind,
				Name:       serverNode.Name,
				UID:        serverNode.UID,
			},
		}
	} else if l > 0 {
		var foundAnyNode bool
		for _, ref := range lease.OwnerReferences {
			if ref.APIVersion == corev1.SchemeGroupVersion.WithKind("Node").Version &&
				ref.Kind == corev1.SchemeGroupVersion.WithKind("Node").Kind {
				foundAnyNode = true
				if serverNode.UID == ref.UID && serverNode.Name == ref.Name {
					return lease
				}
				klog.Infof("found that lease had node in owner references that is"+
					"not this node node.UID(%s), ref.UID(%s), node.Name(%s), ref.Name(%s)",
					serverNode.UID, ref.UID, serverNode.Name, ref.Name)
			}
		}
		if !foundAnyNode {
			klog.Warningf("found that lease had owner references, but no node on owner references")
		}
	}

	return lease

}

func ensureLease(leaseClient v1beta1.LeaseInterface, baseLease *coordinationv1.Lease) (*coordinationv1.Lease, error) {
	l, err := leaseClient.Create(context.TODO(), baseLease, metav1.CreateOptions{})
	if err != nil {
		switch {
		case errors.IsNotFound(err):
			klog.Errorf("Node lease not supported, err: %v", err)
			return nil, err
		case errors.IsAlreadyExists(err), errors.IsConflict(err):
			klog.Warningf("error creating lease, deleting and recreating, err: %v", err)
			err := leaseClient.Delete(context.TODO(), baseLease.Name, metav1.DeleteOptions{})
			if err != nil && errors.IsNotFound(err) {
				klog.Errorf("not delete old node lease, err: %v", err)
				return nil, pkgerrors.Wrap(err, "old lease exists but clould not delete")
			}
			l, err = leaseClient.Create(context.TODO(), baseLease, metav1.CreateOptions{})
		}
	}

	return l, nil
}

// WithNodeEnableLeaseV1 enables support for v1 leases.
// V1 Leases share all the same properties as v1beta1 leases, except they do not fallback like
// the v1beta1 lease controller does if the API server does not support it. If the lease duration is not specified (0)
// then DefaultLeaseDuration will be used
func WithNodeEnableLeaseV1Beta1(client v1beta1.LeaseInterface, leaseDurationSeconds int32) NodeControllerOpt {
	return func(n *NodeController) error {
		n.leases = client
		return nil
	}
}

// WithNodeStatusUpdateErrorHandler adds an error handler for cases where there is an error
// when updating the node status.
// This allows the caller to have some control on how errors are dealt with when
// updating a node's status.
//
// The error passed to the handler will be the error received from kubernetes
// when updating node status.
func WithNodeStatusUpdateErrorHandler(h ErrorHandler) NodeControllerOpt {
	return func(n *NodeController) error {
		n.nodeStatusUpdateErrorHandler = h
		return nil
	}
}
