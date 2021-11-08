package sci

import (
	"github.com/ligao-cloud-native/vitual-kubelet/pkg/manager"
	"k8s.io/client-go/kubernetes"
	"time"
)

const (
	defaultCPUCapacity    = "20"
	defaultMemoryCapacity = "50G"
	defaultPodCapacity    = "110"

	defaultOperatingSystem = "linux"
	defaultSystemArch      = "amd64"
)

// SCIProvider implements the virtual-kubelet provider interface and communicates with k8s APIs.
type SCIProvider struct {
	internalIP         string
	daemonEndpointPort int32

	starTime time.Time

	// the client connect to provider cluster
	masterClient kubernetes.Interface
	// all informer list in cluster vk node
	resourceManager *manager.ResourceManager
	// config hold infos of vk node
	config SCIConfig

	// secret, configMap, Secret
	//rrp RelatedResourcesOfPod
	// operate cluster with http
	//sciAgent *agentApis.SciAgent

	kubeSecretNamespaceKey string
}

// NewSCIProvider creates a new ECIProvider.
func NewSCIProvider(
	providerConfig string,
	clusterName string,
	nodeName string,
	kubeletVersion string,
	internalIP string,
	daemonPort int32,
	kubeClient kubernetes.Interface,
	rm *manager.ResourceManager) (*SCIProvider, error) {

	conf, err := LoadConfig(providerConfig, nodeName)
	if err != nil {
		return nil, err
	}

	if conf.Cpu == "" {
		conf.Cpu = defaultCPUCapacity
	}
	if conf.Memory == "" {
		conf.Memory = defaultMemoryCapacity
	}
	if conf.Pods == "" {
		conf.Pods = defaultPodCapacity
	}
	if conf.OperatingSystem == "" {
		conf.OperatingSystem = defaultOperatingSystem
	}
	if conf.SystemArch == "" {
		conf.SystemArch = defaultSystemArch
	}

	if conf.KubeletVersion == "" {
		conf.KubeletVersion = kubeletVersion
	}
	if conf.NodeName == "" {
		conf.NodeName = nodeName
	}

	conf.ClusterName = clusterName

	return &SCIProvider{
		internalIP:         internalIP,
		daemonEndpointPort: daemonPort,
		starTime:           time.Now(),
		config:             conf,
		masterClient:       kubeClient,
		resourceManager:    rm,
	}, nil
}
