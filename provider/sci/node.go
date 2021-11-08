package sci

import (
	"context"
	"fmt"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"strings"
)

// NodeFromProvider builds a kubernetes node object from a provider
// This is a temporary solution until node stuff actually split off from the provider interface itself.
func NodeFromProvider(p *SCIProvider, taint *corev1.Taint) *corev1.Node {
	taints := make([]corev1.Taint, 0)
	if taint != nil {
		taints = append(taints, *taint)
	}

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: p.config.NodeName,
			Labels: map[string]string{
				"type":                                     "virtual-kubelet",
				"kubernetes.io/role":                       "agent",
				"kubernetes.io/hostname":                   p.config.NodeName,
				"beta.kubernetes.io/os":                    strings.ToLower(p.config.OperatingSystem),
				"kubernetes.io/os":                         strings.ToLower(p.config.OperatingSystem),
				"beta.kubernetes.io/arch":                  strings.ToLower(p.config.SystemArch),
				"kubernetes.io/arch":                       strings.ToLower(p.config.SystemArch),
				"virtual-kubelet.io/provider-cluster-name": p.config.ClusterName,
				"alpha.service-controller.kubernetes.io/exclude-balancer": "true",
			},
		},
		Spec: corev1.NodeSpec{
			Taints: taints,
		},

		Status: corev1.NodeStatus{
			NodeInfo: corev1.NodeSystemInfo{
				OperatingSystem: strings.ToLower(p.config.OperatingSystem),
				Architecture:    strings.ToLower(p.config.SystemArch),
				KubeletVersion:  p.config.KubeletVersion,
			},
			Capacity:        p.Capacity(),
			Allocatable:     p.Capacity(),
			Conditions:      p.NodeConditions(),
			Addresses:       p.NodeAddresses(),
			DaemonEndpoints: p.NodeDaemonEndpoints(),
		},
	}
	return node

}

func (p *SCIProvider) Capacity() corev1.ResourceList {
	return corev1.ResourceList{
		"cpu":    resource.MustParse(p.config.Cpu),
		"memory": resource.MustParse(p.config.Memory),
		"pods":   resource.MustParse(p.config.Pods),
	}
}

func (p *SCIProvider) NodeConditions() []corev1.NodeCondition {
	return []corev1.NodeCondition{
		{
			Type:               "Ready",
			Status:             corev1.ConditionTrue,
			LastHeartbeatTime:  metav1.Now(),
			LastTransitionTime: metav1.Now(),
			Reason:             "KubeletReady",
			Message:            "kubelet is ready",
		},
		{
			Type:               "PIDPressure",
			Status:             corev1.ConditionFalse,
			LastHeartbeatTime:  metav1.Now(),
			LastTransitionTime: metav1.Now(),
			Reason:             "KubeletHasSufficientPID",
			Message:            "kubelet has sufficient PID available",
		},
		{
			Type:               "DiskPressure",
			Status:             corev1.ConditionFalse,
			LastHeartbeatTime:  metav1.Now(),
			LastTransitionTime: metav1.Now(),
			Reason:             "KubeletHasNoDiskPressure",
			Message:            "kubelet has no disk pressure",
		},
		{
			Type:               "MemoryPressure",
			Status:             corev1.ConditionFalse,
			LastHeartbeatTime:  metav1.Now(),
			LastTransitionTime: metav1.Now(),
			Reason:             "KubeletHasSufficientMemory",
			Message:            "kubelet has sufficient memory available",
		},
		{
			Type:               "NetworkUnavailable",
			Status:             corev1.ConditionFalse,
			LastHeartbeatTime:  metav1.Now(),
			LastTransitionTime: metav1.Now(),
			Reason:             "RouteCreated",
			Message:            "RouteController created a route",
		},
	}
}

func (p *SCIProvider) NodeAddresses() []corev1.NodeAddress {
	return []corev1.NodeAddress{
		{
			Type:    corev1.NodeInternalIP,
			Address: p.internalIP,
		},
		{
			Type:    corev1.NodeHostName,
			Address: p.config.NodeName,
		},
	}
}

func (p *SCIProvider) NodeDaemonEndpoints() corev1.NodeDaemonEndpoints {
	return corev1.NodeDaemonEndpoints{
		KubeletEndpoint: corev1.DaemonEndpoint{
			Port: p.daemonEndpointPort,
		},
	}
}

func (p *SCIProvider) Ping(context.Context) error {
	serverVersion, err := p.masterClient.Discovery().ServerVersion()
	if err != nil {
		klog.Error("ping error")
		return fmt.Errorf("can not get master apiserver status: %v", err)
	}

	klog.Infof("get master apiserver version: %s", serverVersion)
	return nil
}

func (p *SCIProvider) NotifyNodeStatus(ctx context.Context, cb func(*corev1.Node)) {
	go func() {
		select {
		//case <- p.
		case <-ctx.Done():
			return
		}
	}()
}
