package app

import (
	"github.com/mitchellh/go-homedir"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Defaults for root command options
const (
	DefaultNodeName             = "virtual-kubelet"
	DefaultOperatingSystem      = "linux"
	DefaultInformerResyncPeriod = 1 * time.Minute
	DefaultMetricsAddr          = ":10255"
	DefaultListenPort           = 10250 // TODO(cpuguy83)(VK1.0): Change this to an addr instead of just a port.. we should not be listening on all interfaces.
	DefaultPodSyncWorkers       = 10
	DefaultKubeNamespace        = corev1.NamespaceAll
	DefaultKubeClusterDomain    = "cluster.local"

	DefaultTaintEffect           = string(corev1.TaintEffectNoSchedule)
	DefaultTaintKey              = "virtual-kubelet.io/provider"
	DefaultStreamIdleTimeout     = 30 * time.Second
	DefaultStreamCreationTimeout = 30 * time.Second
)

// Opts stores all the options for configuring the root virtual-kubelet command.
// It is used for setting flag values.
//
// You can set the default options by creating a new `Opts` struct and passing
// it into `SetDefaultOpts`
type Opts struct {
	// k8s master url
	Master string
	// k8s kube config
	KubeConfig string

	// Namespace to watch for pods and other resources
	KubeNamespace string
	// Domain suffix to append to search domains for the pods created by virtual-kubelet
	KubeClusterDomain string

	// Number of workers to use to handle pod notifications
	PodSyncWorkers int
	// min sync period in reflectors
	InformerResyncPeriod time.Duration

	// Node name to use when creating a node in Kubernetes
	NodeName string
	// serverless cluster name
	ClusterName string

	// taint to be applied to vk node
	TaintKey     string
	TaintEffect  string
	DisableTaint bool

	// Operating system to run pods for
	OperatingSystem string

	// Provider name and config
	Provider           string
	ProviderConfigPath string

	// Use node leases when supported by Kubernetes (instead of node status updates)
	EnableNodeLease bool

	//TraceExporters  []string
	//TraceSampleRate string
	//TraceConfig     TracingExporterOptions

	// Startup Timeout is how long to wait for the kubelet to start
	StartupTimeout time.Duration

	// Sets the port to listen for requests from the Kubernetes API server
	ListenPort  int32
	MetricsAddr string
	VKCertPath  string
	VKKeyPath   string

	// StreamIdleTimeout is the maximum time a streaming connection
	// can be idle before the connection is automatically closed.
	StreamIdleTimeout time.Duration
	// StreamCreationTimeout is the maximum time for streaming connection
	StreamCreationTimeout time.Duration

	// hralthz/readyz endpoint
	HealthProbeAddr string

	Version string
}

// SetDefaultOpts sets default options for unset values on the passed in option struct.
// Fields tht are already set will not be modified.
func SetDefaultOpts(c *Opts) error {
	if c.OperatingSystem == "" {
		c.OperatingSystem = DefaultOperatingSystem
	}

	if c.NodeName == "" {
		c.NodeName = getEnv("DEFAULT_NODE_NAME", DefaultNodeName)
	}

	if c.InformerResyncPeriod == 0 {
		c.InformerResyncPeriod = DefaultInformerResyncPeriod
	}

	if c.MetricsAddr == "" {
		c.MetricsAddr = DefaultMetricsAddr
	}

	if c.PodSyncWorkers == 0 {
		c.PodSyncWorkers = DefaultPodSyncWorkers
	}

	//if c.TraceConfig.ServiceName == "" {
	//	c.TraceConfig.ServiceName = DefaultNodeName
	//}

	if c.ListenPort == 0 {
		if kp := os.Getenv("KUBELET_PORT"); kp != "" {
			p, err := strconv.Atoi(kp)
			if err != nil {
				return errors.Wrap(err, "error parsing KUBELET_PORT environment variable")
			}
			c.ListenPort = int32(p)
		} else {
			c.ListenPort = DefaultListenPort
		}
	}

	if c.KubeNamespace == "" {
		c.KubeNamespace = DefaultKubeNamespace
	}

	if c.KubeClusterDomain == "" {
		c.KubeClusterDomain = DefaultKubeClusterDomain
	}

	if c.TaintKey == "" {
		c.TaintKey = DefaultTaintKey
	}
	if c.TaintEffect == "" {
		c.TaintEffect = DefaultTaintEffect
	}

	if c.KubeConfig == "" {
		c.KubeConfig = os.Getenv("KUBECONFIG")
		if c.KubeConfig == "" {
			home, _ := homedir.Dir()
			if home != "" {
				c.KubeConfig = filepath.Join(home, ".kube", "config")
			}
		}
	}

	if c.StreamIdleTimeout == 0 {
		c.StreamIdleTimeout = DefaultStreamIdleTimeout
	}

	if c.StreamCreationTimeout == 0 {
		c.StreamCreationTimeout = DefaultStreamCreationTimeout
	}

	return nil
}
