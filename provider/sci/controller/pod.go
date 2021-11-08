// Copyright © 2017 The virtual-kubelet authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controller

import (
	"context"
	"fmt"
	"github.com/ligao-cloud-native/vitual-kubelet/pkg/manager"
	"github.com/ligao-cloud-native/vitual-kubelet/provider"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/klog/v2"
	"sync"
	"time"

	"github.com/google/go-cmp/cmp"
	pkgerrors "github.com/pkg/errors"
	"github.com/virtual-kubelet/virtual-kubelet/errdefs"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	corev1informers "k8s.io/client-go/informers/core/v1"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
)

// PodController is the controller implementation for Pod resources.
type PodController struct {
	provider provider.PodLifecycleHandler

	// podsInformer is an informer for Pod resources.
	podsInformer corev1informers.PodInformer
	// podsLister is able to list/get Pod resources from a shared informer's store.
	podsLister corev1listers.PodLister

	//add
	eventsInformer corev1informers.EventInformer
	eventsLister   corev1listers.EventLister

	namespaceListerSynced cache.InformerSynced

	// recorder is an event recorder for recording Event resources to the Kubernetes API.
	recorder record.EventRecorder

	client corev1client.PodsGetter

	resourceManager *manager.ResourceManager

	nodeTaint *corev1.Taint

	// deletePodsFromKubernetes is a queue on which pods are reconciled, and we check if pods are in API server after
	// the grace period
	syncPodsFromKubernetesQueue    workqueue.RateLimitingInterface
	deletePodsFromKubernetesQueue  workqueue.RateLimitingInterface
	syncPodStatusFromProviderQueue workqueue.RateLimitingInterface

	// From the time of creation, to termination the knownPods map will contain the pods key
	// (derived from Kubernetes' cache library) -> a *knownPod struct.
	knownPods sync.Map

	podEventFilterFunc PodEventFilterFunc

	// ready is a channel which will be closed once the pod controller is fully up and running.
	// this channel will never be closed if there is an error on startup.
	ready chan struct{}
	// done is closed when Run returns
	// Once done is closed `err` may be set to a non-nil value
	done chan struct{}

	mu sync.Mutex
	// err is set if there is an error while while running the pod controller.
	// Typically this would be errors that occur during startup.
	// Once err is set, `Run` should return.
	//
	// This is used since `pc.Run()` is typically called in a goroutine and managing
	// this can be non-trivial for callers.
	err error

	ctx context.Context
}

// PodEventFilterFunc is used to filter pod events received from Kubernetes.
//
// Filters that return true means the event handler will be run
// Filters that return false means the filter will *not* be run.
type PodEventFilterFunc func(context.Context, *corev1.Pod) bool

type QueueHandler func(ctx context.Context, key string) error

type knownPod struct {
	// You cannot read (or modify) the fields in this struct without taking the lock. The individual fields
	// should be immutable to avoid having to hold the lock the entire time you're working with them
	sync.Mutex
	lastPodStatusReceivedFromProvider *corev1.Pod
	lastPodUsed                       *corev1.Pod
	lastPodStatusUpdateSkipped        bool
}

// PodControllerConfig is used to configure a new PodController.
type PodControllerConfig struct {
	Provider provider.PodLifecycleHandler

	// PodClient is used to perform actions on the k8s API, such as updating pod status
	// This field is required
	PodClient corev1client.PodsGetter

	ClusterName string
	NodeTaint   *corev1.Taint

	// PodInformer is used as a local cache for pods
	// This should be configured to only look at pods scheduled to the node which the controller will be managing
	// If the informer does not filter based on node, then you must provide a `PodEventFilterFunc` parameter so event handlers
	//   can filter pods not assigned to this node.
	PodInformer corev1informers.PodInformer

	EventRecorder record.EventRecorder

	// Informers used for filling details for things like downward API in pod spec.
	//
	// We are using informers here instead of listers because we'll need the
	// informer for certain features (like notifications for updated ConfigMaps)
	ConfigMapInformer corev1informers.ConfigMapInformer
	SecretInformer    corev1informers.SecretInformer
	ServiceInformer   corev1informers.ServiceInformer

	//add
	EventInformer     corev1informers.EventInformer
	NamespaceInformer corev1informers.NamespaceInformer

	//// SyncPodsFromKubernetesRateLimiter defines the rate limit for the SyncPodsFromKubernetes queue
	//SyncPodsFromKubernetesRateLimiter workqueue.RateLimiter
	//// SyncPodsFromKubernetesShouldRetryFunc allows for a custom retry policy for the SyncPodsFromKubernetes queue
	//SyncPodsFromKubernetesShouldRetryFunc ShouldRetryFunc
	//
	//// DeletePodsFromKubernetesRateLimiter defines the rate limit for the DeletePodsFromKubernetesRateLimiter queue
	//DeletePodsFromKubernetesRateLimiter workqueue.RateLimiter
	//// DeletePodsFromKubernetesShouldRetryFunc allows for a custom retry policy for the SyncPodsFromKubernetes queue
	//DeletePodsFromKubernetesShouldRetryFunc ShouldRetryFunc
	//
	//// SyncPodStatusFromProviderRateLimiter defines the rate limit for the SyncPodStatusFromProviderRateLimiter queue
	//SyncPodStatusFromProviderRateLimiter workqueue.RateLimiter
	//// SyncPodStatusFromProviderShouldRetryFunc allows for a custom retry policy for the SyncPodStatusFromProvider queue
	//SyncPodStatusFromProviderShouldRetryFunc ShouldRetryFunc
	//
	//// Add custom filtering for pod informer event handlers
	//// Use this for cases where the pod informer handles more than pods assigned to this node
	////
	//// For example, if the pod informer is not filtering based on pod.Spec.NodeName, you should
	//// set that filter here so the pod controller does not handle events for pods assigned to other nodes.
	//PodEventFilterFunc PodEventFilterFunc
}

// NewPodController creates a new pod controller with the provided config.
func NewPodController(cfg PodControllerConfig) (*PodController, error) {
	if cfg.PodClient == nil {
		return nil, errdefs.InvalidInput("missing core client")
	}
	if cfg.EventRecorder == nil {
		return nil, errdefs.InvalidInput("missing event recorder")
	}
	if cfg.PodInformer == nil {
		return nil, errdefs.InvalidInput("missing pod informer")
	}
	if cfg.ConfigMapInformer == nil {
		return nil, errdefs.InvalidInput("missing config map informer")
	}
	if cfg.SecretInformer == nil {
		return nil, errdefs.InvalidInput("missing secret informer")
	}
	if cfg.ServiceInformer == nil {
		return nil, errdefs.InvalidInput("missing service informer")
	}
	if cfg.Provider == nil {
		return nil, errdefs.InvalidInput("missing provider")
	}
	//if cfg.SyncPodsFromKubernetesRateLimiter == nil {
	//	cfg.SyncPodsFromKubernetesRateLimiter = workqueue.DefaultControllerRateLimiter()
	//}
	//if cfg.DeletePodsFromKubernetesRateLimiter == nil {
	//	cfg.DeletePodsFromKubernetesRateLimiter = workqueue.DefaultControllerRateLimiter()
	//}
	//if cfg.SyncPodStatusFromProviderRateLimiter == nil {
	//	cfg.SyncPodStatusFromProviderRateLimiter = workqueue.DefaultControllerRateLimiter()
	//}
	//rm, err := manager.NewResourceManager(cfg.PodInformer.Lister(), cfg.SecretInformer.Lister(), cfg.ConfigMapInformer.Lister(), cfg.ServiceInformer.Lister())
	//if err != nil {
	//	return nil, pkgerrors.Wrap(err, "could not create resource manager")
	//}

	pc := &PodController{
		provider:              cfg.Provider,
		client:                cfg.PodClient,
		recorder:              cfg.EventRecorder,
		podsInformer:          cfg.PodInformer,
		podsLister:            cfg.PodInformer.Lister(),
		eventsInformer:        cfg.EventInformer,
		eventsLister:          cfg.EventInformer.Lister(),
		namespaceListerSynced: cfg.NamespaceInformer.Informer().HasSynced,
		//resourceManager:    rm,
		ready: make(chan struct{}),
		done:  make(chan struct{}),
		//podEventFilterFunc: cfg.PodEventFilterFunc,
		nodeTaint: cfg.NodeTaint,
	}

	pc.syncPodsFromKubernetesQueue = workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "syncPodsFromKubernetes")
	pc.deletePodsFromKubernetesQueue = workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "deletePodsFromKubernetes")
	pc.syncPodStatusFromProviderQueue = workqueue.NewNamedRateLimitingQueue(workqueue.NewItemExponentialFailureRateLimiter(3*time.Second, 100*time.Second), "syncPodStatusFromProvider")

	pc.podsInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			pod, isPod := obj.(*corev1.Pod)
			if !isPod {
				return
			}

			key := fmt.Sprintf("%s/%s/%s", pod.Namespace, pod.Name, cfg.ClusterName)
			pc.syncPodStatusFromProviderQueue.Forget(key)
			//加入队列
			pc.syncPodsFromKubernetesQueue.AddRateLimited(key)
			pc.syncPodStatusFromProviderQueue.AddRateLimited(key)

		},
		DeleteFunc: func(obj interface{}) {
			pod, isPod := obj.(*corev1.Pod)
			if !isPod {
				return
			}

			key := fmt.Sprintf("%s/%s/%s", pod.Namespace, pod.Name, cfg.ClusterName)
			pc.syncPodStatusFromProviderQueue.Forget(key)

			pc.deletePodsFromKubernetesQueue.Forget(key)

		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			oldPod := oldObj.(*corev1.Pod)
			newPod := newObj.(*corev1.Pod)
			if oldPod.ResourceVersion == newPod.ResourceVersion {
				return
			}

			key := fmt.Sprintf("%s/%s/%s", newPod.Namespace, newPod.Name, cfg.ClusterName)
			pc.syncPodStatusFromProviderQueue.Forget(key)
			pc.syncPodStatusFromProviderQueue.AddRateLimited(key)

			if podShouldEnqueue(oldPod, newPod) {
				pc.syncPodsFromKubernetesQueue.AddRateLimited(key)
			}
		},
	})

	return pc, nil
}

// Run will set up the event handlers for types we are interested in, as well
// as syncing informer caches and starting workers.  It will block until the
// context is cancelled, at which point it will shutdown the work queue and
// wait for workers to finish processing their current work items prior to
// returning.
//
// Once this returns, you should not re-use the controller.
func (pc *PodController) Run(ctx context.Context, podSyncWorkers int) (retErr error) {
	// Shutdowns are idempotent, so we can call it multiple times. This is in case we have to bail out early for some reason.
	// This is to make extra sure that any workers we started are terminated on exit
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	defer func() {
		pc.mu.Lock()
		pc.err = retErr
		close(pc.done)
		pc.mu.Unlock()
	}()

	pc.ctx = ctx

	// Wait for the caches to be synced *before* starting to do work.
	if ok := cache.WaitForCacheSync(ctx.Done(),
		pc.podsInformer.Informer().HasSynced,
		pc.eventsInformer.Informer().HasSynced,
		pc.namespaceListerSynced); !ok {
		return pkgerrors.New("failed to wait for caches to sync")
	}
	klog.Info("Pod cache in-sync")

	// Perform a reconciliation step that deletes any dangling pods from the provider.
	// This happens only when the virtual-kubelet is starting, and operates on a "best-effort" basis.
	// If by any reason the provider fails to delete a dangling pod, it will stay in the provider and deletion won't be retried.
	pc.deleteDanglingPods(ctx, podSyncWorkers)

	klog.Info("starting workers")

	wg := sync.WaitGroup{}

	for i := 0; i < podSyncWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			wait.Until(pc.runSyncPodStatusFromProviderWorker, 5*time.Second, ctx.Done())
		}()
	}

	for i := 0; i < podSyncWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pc.runSyncPodsFromK8sWorker(ctx, pc.syncPodsFromKubernetesQueue)
		}()
	}

	for i := 0; i < podSyncWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pc.runDeletePodFromK8sWorker(ctx, pc.deletePodsFromKubernetesQueue)
		}()
	}

	klog.Info("started workers")
	close(pc.ready)

	<-ctx.Done()
	klog.Info("shutting down workers")

	return nil
}

// Ready returns a channel which gets closed once the PodController is ready to handle scheduled pods.
// This channel will never close if there is an error on startup.
// The status of this channel after shutdown is indeterminate.
func (pc *PodController) Ready() <-chan struct{} {
	return pc.ready
}

// Done returns a channel receiver which is closed when the pod controller has exited.
// Once the pod controller has exited you can call `pc.Err()` to see if any error occurred.
func (pc *PodController) Done() <-chan struct{} {
	return pc.done
}

// Err returns any error that has occurred and caused the pod controller to exit.
func (pc *PodController) Err() error {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	return pc.err
}

// runSyncPodStatusFromProviderWorker tries and reconciles the state of a pod by comparing its Kubernetes representation and the provider's representation.
func (pc *PodController) runSyncPodStatusFromProviderWorker() {
	for handleQueueItem(pc.ctx, pc.syncPodStatusFromProviderQueue, pc.podStatusHandler) {
	}
}

// runSyncPodsFromK8sWorker compares the actual state with the desired, and attempts to converge the two.
func (pc *PodController) runSyncPodsFromK8sWorker(ctx context.Context, q workqueue.RateLimitingInterface) {
	for handleQueueItem(ctx, q, pc.syncHandler) {
	}
}

func (pc *PodController) runDeletePodFromK8sWorker(ctx context.Context, q workqueue.RateLimitingInterface) {
	for handleQueueItem(ctx, q, pc.deletePodHandler) {
	}
}

func (pc *PodController) podStatusHandler(ctx context.Context, key string) (retErr error) {
	shouldAddQueue := true
	defer func() {
		if !shouldAddQueue {
			return
		}

		pc.syncPodStatusFromProviderQueue.AddRateLimited((key))
		if retErr != nil {
			klog.Errorf("Error processing pod status update, %v", retErr)
		}
	}()

	// Convert the namespace/name string into a distinct namespace and name.
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		// Log the error as a warning, but do not requeue the key as it is invalid.
		return pkgerrors.Wrapf(err, "invalid resource key: %q", key)
	}

	pod, err := pc.podsLister.Pods(namespace).Get(name)
	if err != nil {
		if errors.IsNotFound(err) {
			pc.syncPodStatusFromProviderQueue.Forget(key)
			shouldAddQueue = false
			return nil
		}

		return pkgerrors.Wrapf(err, "not find pod")
	}

	// If the pod('s containers) is no longer in a running state then we force-delete the pod from API server
	// more context is here: https://github.com/virtual-kubelet/virtual-kubelet/pull/760
	if pod.DeletionTimestamp != nil && !running(&pod.Status) {
		klog.Infof("Force deleting pod from API Server as it is no longer running")
		pc.syncPodStatusFromProviderQueue.Forget(key)
		shouldAddQueue = false
		return nil
	}

	if pc.podFilterFunc(pod) {
		shouldAddQueue = false
		return nil
	}

	retErr = pc.updatePodStatus(ctx, pod)
	return
}

func (pc *PodController) syncHandler(ctx context.Context, key string) error {
	startTime := time.Now()
	defer func() {
		klog.Infof("finished sync pod %q (%v)", key, time.Now().Sub(startTime))
	}()

	// Convert the namespace/name string into a distinct namespace and name.
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		// Log the error as a warning, but do not requeue the key as it is invalid.
		klog.Error(pkgerrors.Wrapf(err, "invalid resource key: %q", key))
		return pkgerrors.Wrap(err, "error splitting cache key")
	}

	// Get the Pod resource with this namespace/name.
	pod, err := pc.podsLister.Pods(namespace).Get(name)
	switch {
	case errors.IsNotFound(err):
		klog.Infof("pod(%v) has deleted, will delete provider pod", key)
		deleteProviderPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
		}

		err = pc.provider.DeletePod(ctx, deleteProviderPod)
		if err != nil && !errors.IsNotFound(err) {
			klog.Error(pkgerrors.Wrapf(err, "failed delete provider pod key: %q", key))
			return pkgerrors.Wrapf(err, "failed delete provider pod key: %q", key)
		}

		return nil
	case err != nil:
		klog.Infof("unable to retrieve pods(%v) from store: %v", key, err)
		return pkgerrors.Wrapf(err, "failed get lister pod %q, error: %v", key, err)
	default:
		return pc.syncProviderPod(ctx, pod, key)
	}
}

func (pc *PodController) deletePodHandler(ctx context.Context, key string) error {
	// Convert the namespace/name string into a distinct namespace and name.
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		// Log the error as a warning, but do not requeue the key as it is invalid.
		klog.Error(pkgerrors.Wrapf(err, "invalid resource key: %q", key))
		return nil
	}

	cachedPod, err := pc.podsLister.Pods(namespace).Get(name)
	if err != nil && errors.IsNotFound(err) {
		return err
	}
	if cachedPod != nil && running(&cachedPod.Status) {
		klog.Errorf("force delete running pod(%s/%s)", namespace, name)
	}

	err = pc.client.Pods(namespace).Delete(context.TODO(), name, *metav1.NewDeleteOptions(0))
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	err = pc.provider.DeletePod(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	})
	if err != nil && errors.IsNotFound(err) {
		return err
	}

	return nil
}

func (pc *PodController) syncProviderPod(ctx context.Context, pod *corev1.Pod, key string) error {
	klog.Infof("process k8s pod(%s)", loggablePodName(pod))

	if pod.DeletionTimestamp != nil && !running(&pod.Status) {
		klog.Infof("force delete pod as no longer running")
		pc.deletePodsFromKubernetesQueue.Add(key)
		return nil
	}

	if pod.DeletionTimestamp != nil {
		klog.Infof("delete provider pod(%s/%s)", pod.Namespace, pod.Name)
		err := pc.deleteProviderPod(ctx, pod)
		if err != nil && !errors.IsNotFound(err) {
			klog.Errorf("Failed to delete provider pod, %v", loggablePodName(pod))
			return pkgerrors.Wrapf(err, "Failed to delete provider pod, %v", loggablePodName(pod))
		}

		// delete k8s pod
		pc.deletePodsFromKubernetesQueue.AddAfter(key, time.Second*time.Duration(*pod.DeletionGracePeriodSeconds))
		return nil
	}

	if pc.podFilterFunc(pod) {
		return nil
	}

	klog.Infof("create or update provider pod(%s)", loggablePodName(pod))
	if err := pc.createOrUpdatePod(ctx, pod); err != nil {
		klog.Errorf("sync provider pod %s error: %v", loggablePodName(pod), err)
		return pkgerrors.Wrapf(err, "failed to sync provider pod %q", loggablePodName(pod))
	}

	return nil
}

func (pc *PodController) deleteProviderPod(ctx context.Context, providerPod *corev1.Pod) error {
	if err := pc.provider.DeletePod(ctx, providerPod.DeepCopy()); err != nil {
		pc.recorder.Event(providerPod, corev1.EventTypeWarning, "ProviderDeleteFailed", err.Error())
		return err
	}

	pc.recorder.Event(providerPod, corev1.EventTypeNormal, "ProviderDeleteSuccess", "delete provider pods success")
	return nil
}

func (pc *PodController) createOrUpdatePod(ctx context.Context, pod *corev1.Pod) error {
	providerPod := pod.DeepCopy()

	ppod, err := pc.provider.GetPod(ctx, providerPod.Namespace, providerPod.Name)
	if errors.IsNotFound(err) {
		if pod.DeletionTimestamp != nil {
			return nil
		}

		klog.Infof("create provider pod(%s/%s)", providerPod.Namespace, providerPod.Name)
		if createErr := pc.provider.CreatePod(ctx, providerPod); createErr != nil {
			pc.recorder.Event(providerPod, corev1.EventTypeWarning, "ProviderCreateFailed", createErr.Error())
			return createErr
		}
		pc.recorder.Event(providerPod, corev1.EventTypeNormal, "ProviderCreateSuccess", "create provider pods success")
		return nil
	}

	if err != nil {
		pc.recorder.Event(providerPod, corev1.EventTypeWarning, "ProviderFailed", "get provider pod error")
	}

	klog.Infof("provider pod(%s/%s) exists, to update", providerPod.Namespace, providerPod.Name)
	if updateErr := pc.provider.UpdatePod(ctx, ppod); updateErr != nil {
		pc.recorder.Event(providerPod, corev1.EventTypeWarning, "ProviderUpdateFailed", updateErr.Error())
		return updateErr
	}
	pc.recorder.Event(providerPod, corev1.EventTypeNormal, "ProviderUpdateSuccess", "update provider pod success")
	return nil
}

// deleteDanglingPods checks whether the provider knows about any pods which Kubernetes doesn't know about, and deletes them.
func (pc *PodController) deleteDanglingPods(ctx context.Context, threadiness int) {
	// Grab the list of pods known to the provider.
	pps, err := pc.provider.GetPods(ctx)
	if err != nil {
		err := pkgerrors.Wrap(err, "failed to fetch the list of pods from the provider")
		klog.Error(err)
		return
	}

	// Create a slice to hold the pods we will be deleting from the provider.
	ptd := make([]*corev1.Pod, 0)

	// Iterate over the pods known to the provider, marking for deletion those that don't exist in Kubernetes.
	// Take on this opportunity to populate the list of key that correspond to pods known to the provider.
	for _, pp := range pps {
		providerPodName := pp.Labels["virtual-kubelet.io/provider-resource-name"]
		providerPodNamespace := pp.Labels["virtual-kubelet.io/provider-resource-namespace"]
		if providerPodName == "" || providerPodNamespace == "" {
			continue
		}

		if _, err := pc.podsLister.Pods(pp.Namespace).Get(pp.Name); err != nil {
			if errors.IsNotFound(err) {
				// The current pod does not exist in Kubernetes, so we mark it for deletion.
				ptd = append(ptd, pp)
				continue
			}
			// For some reason we couldn't fetch the pod from the lister, so we propagate the error.
			err := pkgerrors.Wrap(err, "failed to fetch pod from the lister")
			klog.Error(err)
		}
	}

	// We delete each pod in its own goroutine, allowing a maximum of "threadiness" concurrent deletions.
	semaphore := make(chan struct{}, threadiness)
	var wg sync.WaitGroup
	wg.Add(len(ptd))

	// Iterate over the slice of pods to be deleted and delete them in the provider.
	for _, pod := range ptd {
		go func(ctx context.Context, pod *corev1.Pod) {
			defer wg.Done()

			semaphore <- struct{}{}
			defer func() {
				<-semaphore
			}()

			// Actually delete the pod.
			if err := pc.provider.DeletePod(ctx, pod.DeepCopy()); err != nil && !errdefs.IsNotFound(err) {
				klog.Errorf("failed to delete pod %q in provider", loggablePodName(pod))
			} else {
				klog.Infof("deleted leaked pod %q in provider", loggablePodName(pod))
			}
		}(ctx, pod)
	}

	// Wait for all pods to be deleted.
	wg.Wait()

	return
}

func handleQueueItem(ctx context.Context, q workqueue.RateLimitingInterface, handler QueueHandler) bool {
	obj, shutdown := q.Get()
	if shutdown {
		return false
	}

	err := func(obj interface{}) error {
		defer q.Done(obj)

		key, isString := obj.(string)
		if !isString {
			q.Forget(obj)
			klog.Warningf("get excepted object from queue, %#v", obj)
			return nil
		}

		if err := handler(ctx, key); err != nil {
			if q.NumRequeues(key) < 3 {
				klog.Warningf("re-enqueue %q due to sync failed, %v", key, err)
				q.AddRateLimited(key)
				return nil
			}

			q.Forget(obj)
			return pkgerrors.Wrapf(err, "forgeting %q due to max retry reached", key)
		}

		q.Forget(obj)
		return nil
	}(obj)
	if err != nil {
		runtime.HandleError(err)
		return true
	}

	return true
}

func (pc *PodController) podFilterFunc(pod *corev1.Pod) bool {
	disableTain := pc.nodeTaint == nil
	if filterPodFunc(disableTain, pod) {
		msg := fmt.Sprintf("Pod(%s/%s) not match match VK node filter rules", pod.Namespace, pod.Name)
		pc.recorder.Event(pod, corev1.EventTypeWarning, "ProviderVKNodeFilter", msg)
		return true
	}

	return false
}

func (pc *PodController) updatePodStatus(ctx context.Context, providerPod *corev1.Pod) error {
	klog.Infof("update provider pod(%s/%s)", providerPod.Namespace, providerPod.Name)

	podStatus, err := pc.provider.GetPodStatus(ctx, providerPod.Namespace, providerPod.Name)
	if errors.IsNotFound(err) {
		cachePod, checkErr := pc.podsLister.Pods(providerPod.Namespace).Get(providerPod.Name)
		if errors.IsNotFound(checkErr) || cachePod.DeletionTimestamp != nil {
			return nil
		}

		newProviderPod := cachePod.DeepCopy()
		if creatErr := pc.provider.CreatePod(ctx, newProviderPod); creatErr != nil {
			klog.Errorf("create provider pod error: %v", creatErr)
		}

		return err
	} else if err != nil {
		return err
	}

	if podStatusEffectiveEqual(&providerPod.Status, podStatus) {
		return nil
	}

	newProviderPod := providerPod.DeepCopy()
	newProviderPod.Status = *podStatus
	newProviderPod.Status.HostIP = func() string {
		if providerPod.Status.HostIP != "" {
			return providerPod.Status.HostIP
		}
		return ""
	}()
	newProviderPod.Status.StartTime = &providerPod.CreationTimestamp

	if _, err := pc.client.Pods(providerPod.Namespace).UpdateStatus(
		context.TODO(), newProviderPod, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update pod(%s/%s) status error: %v",
			providerPod.Namespace, providerPod.Name, err)
	}

	return nil
}

// loggablePodName returns the "namespace/name" key for the specified pod.
// If the key cannot be computed, "(unknown)" is returned.
// This method is meant to be used for logging purposes only.
func loggablePodName(pod *corev1.Pod) string {
	k, err := cache.MetaNamespaceKeyFunc(pod)
	if err != nil {
		return "(unknown)"
	}
	return k
}

// loggablePodNameFromCoordinates returns the "namespace/name" key for the pod identified by the specified namespace and name (coordinates).
func loggablePodNameFromCoordinates(namespace, name string) string {
	return fmt.Sprintf("%s/%s", namespace, name)
}

// podsEffectivelyEqual compares two pods, and ignores the pod status, and the resource version
func podsEffectivelyEqual(p1, p2 *corev1.Pod) bool {
	filterForResourceVersion := func(p cmp.Path) bool {
		if p.String() == "ObjectMeta.ResourceVersion" {
			return true
		}
		if p.String() == "Status" {
			return true
		}
		return false
	}

	return cmp.Equal(p1, p2, cmp.FilterPath(filterForResourceVersion, cmp.Ignore()))
}

// borrowed from https://github.com/kubernetes/kubernetes/blob/f64c631cd7aea58d2552ae2038c1225067d30dde/pkg/kubelet/kubelet_pods.go#L944-L953
// running returns true, unless if every status is terminated or waiting, or the status list
// is empty.
func running(podStatus *corev1.PodStatus) bool {
	statuses := podStatus.ContainerStatuses
	for _, status := range statuses {
		if status.State.Terminated == nil && status.State.Waiting == nil {
			return true
		}
	}
	return false
}

// podShouldEnqueue checks if two pods equal according according to podsEqual func and DeleteTimeStamp
func podShouldEnqueue(oldPod, newPod *corev1.Pod) bool {
	if !podsEqual(oldPod, newPod) {
		return true
	}
	if !deleteGraceTimeEqual(oldPod.DeletionGracePeriodSeconds, newPod.DeletionGracePeriodSeconds) {
		return true
	}
	if !oldPod.DeletionTimestamp.Equal(newPod.DeletionTimestamp) {
		return true
	}
	return false
}

// podsEqual checks if two pods are equal according to the fields we know that are allowed
// to be modified after startup time.
func podsEqual(pod1, pod2 *corev1.Pod) bool {
	// Pod Update Only Permits update of:
	// - `spec.containers[*].image`
	// - `spec.initContainers[*].image`
	// - `spec.activeDeadlineSeconds`
	// - `spec.tolerations` (only additions to existing tolerations)
	// - `objectmeta.labels`
	// - `objectmeta.annotations`
	// compare the values of the pods to see if the values actually changed

	return cmp.Equal(pod1.Spec.Containers, pod2.Spec.Containers) &&
		cmp.Equal(pod1.Spec.InitContainers, pod2.Spec.InitContainers) &&
		cmp.Equal(pod1.Spec.ActiveDeadlineSeconds, pod2.Spec.ActiveDeadlineSeconds) &&
		cmp.Equal(pod1.Spec.Tolerations, pod2.Spec.Tolerations) &&
		cmp.Equal(pod1.ObjectMeta.Labels, pod2.Labels) &&
		cmp.Equal(pod1.ObjectMeta.Annotations, pod2.Annotations)

}

func deleteGraceTimeEqual(old, new *int64) bool {
	if old == nil && new == nil {
		return true
	}
	if old != nil && new != nil {
		return *old == *new
	}
	return false
}

func filterPodFunc(disableTaint bool, unscheduledPod *corev1.Pod) bool {
	if !disableTaint && unscheduledPod.Spec.NodeName == "" {
		if v, ok := unscheduledPod.ObjectMeta.Annotations["virtual-kubelet.io/burst-to-sci"]; !ok || v != "true" {
			return true
		}
	}

	for _, vol := range unscheduledPod.Spec.Volumes {
		if vol.HostPath != nil {
			return true
		}
	}

	if unscheduledPod.Spec.SecurityContext != nil {
		sc := unscheduledPod.Spec.SecurityContext
		if sc.SELinuxOptions != nil {
			return true
		}
		if sc.WindowsOptions != nil {
			return true
		}
		if sc.RunAsUser != nil {
			return true
		}
		if sc.RunAsGroup != nil {
			return true
		}
		if sc.RunAsNonRoot != nil {
			return true
		}
		if sc.FSGroup != nil {
			return true
		}
		if sc.SupplementalGroups != nil {
			if len(sc.SupplementalGroups) > 0 {
				return true
			}
		}
		if sc.Sysctls != nil {
			if len(sc.Sysctls) > 0 {
				return true
			}
		}
	}

	return unscheduledPod.Spec.HostNetwork ||
		unscheduledPod.Spec.HostIPC ||
		unscheduledPod.Spec.HostPID
}

func podStatusEffectiveEqual(status1, status2 *corev1.PodStatus) bool {
	status := func(p cmp.Path) bool {
		if p.String() == "Status.StartTime" {
			return true
		}
		if p.String() == "Status.HostIP" {
			return true
		}
		return false
	}

	return cmp.Equal(status1, status2, cmp.FilterPath(status, cmp.Ignore()))
}
