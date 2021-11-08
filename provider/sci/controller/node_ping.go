package controller

import (
	"context"
	"github.com/ligao-cloud-native/vitual-kubelet/provider"
	"k8s.io/klog/v2"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	"k8s.io/apimachinery/pkg/util/wait"
)

// nodePingController is responsible for node pinging behaviour
type nodePingController struct {
	nodeProvider provider.NodeProvider

	pingInterval time.Duration
	pingTimeout  *time.Duration

	firstPingCompleted chan struct{}

	sync.Mutex
	result *pingResult
}

// pingResult encapsulates the result of the last ping. It is the time the ping was started, and the error.
// If there is a timeout, the error will be context.DeadlineExceeded
type pingResult struct {
	time  time.Time
	error error
}

// newNodePingController creates a new node ping controller. pingInterval must be non-zero. Optionally, a timeout may be specfied on
// how long to wait for the provider to respond
func newNodePingController(node provider.NodeProvider, pingInterval time.Duration, timeout *time.Duration) *nodePingController {
	if pingInterval == 0 {
		panic("Node ping interval is 0")
	}

	if timeout != nil && *timeout == 0 {
		panic("Node ping timeout is 0")
	}

	return &nodePingController{
		nodeProvider:       node,
		pingInterval:       pingInterval,
		pingTimeout:        timeout,
		firstPingCompleted: make(chan struct{}),
	}
}

// Run runs the controller until context is cancelled
func (npc *nodePingController) Run(ctx context.Context) {
	const key = "key"
	sf := &singleflight.Group{}

	// 1. If the node is "stuck" and not responding to pings, we want to set the status
	//    to that the node provider has timed out responding to pings
	// 2. We want it so that the context is cancelled, and whatever the node might have
	//    been stuck on uses context so it might be unstuck
	// 3. We want to retry pinging the node, but we do not ever want more than one
	//    ping in flight at a time.

	mkContextFunc := context.WithCancel

	if npc.pingTimeout != nil {
		mkContextFunc = func(ctx2 context.Context) (context.Context, context.CancelFunc) {
			return context.WithTimeout(ctx2, *npc.pingTimeout)
		}
	}

	checkFunc := func(ctx context.Context) {
		ctx, cancel := mkContextFunc(ctx)
		defer cancel()

		doChan := sf.DoChan(key, func() (interface{}, error) {
			now := time.Now()
			err := npc.nodeProvider.Ping(ctx)

			return now, err
		})

		var pingResult pingResult
		select {
		case <-ctx.Done():
			pingResult.error = ctx.Err()
			klog.Error("Failed to ping node due to context cancellation")
		case result := <-doChan:
			pingResult.error = result.Err
			pingResult.time = result.Val.(time.Time)
		}

		npc.Lock()
		defer npc.Unlock()
		npc.result = &pingResult
	}

	// Run the first check manually
	checkFunc(ctx)

	close(npc.firstPingCompleted)

	wait.UntilWithContext(ctx, checkFunc, npc.pingInterval)
}

// GetResult returns the current ping result in a non-blocking fashion except for the first ping. It waits for the
// first ping to be successful before returning. If the context is cancelled while waiting for that value, it will
// return immediately.
func (npc *nodePingController) getResult(ctx context.Context) (*pingResult, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-npc.firstPingCompleted:
		klog.Infof("first ping completed: %v", npc.result)
	}

	npc.Lock()
	defer npc.Unlock()
	return npc.result, nil
}
