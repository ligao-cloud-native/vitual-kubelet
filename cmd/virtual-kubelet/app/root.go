package app

import (
	"context"
	"github.com/ligao-cloud-native/vitual-kubelet/pkg/manager"
	"github.com/ligao-cloud-native/vitual-kubelet/provider"
	"github.com/ligao-cloud-native/vitual-kubelet/provider/sci"
	"github.com/ligao-cloud-native/vitual-kubelet/provider/sci/controller"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/virtual-kubelet/virtual-kubelet/errdefs"
	"github.com/virtual-kubelet/virtual-kubelet/log"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/kubernetes/typed/coordination/v1beta1"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"os"
	"path"
	"time"
)

const (
	VKNodeUserAgent = "virtual-kubelet"
)

// NewCommand creates a new top-level command.
// This command is used to start the virtual-kubelet daemon
func NewCommand(ctx context.Context, name string, c Opts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   name,
		Short: name + " provides a virtual kubelet interface for your kubernetes cluster.",
		Long: name + ` implements the Kubelet interface with a pluggable
backend implementation allowing users to create kubernetes nodes without running the kubelet.
This allows users to schedule kubernetes workloads on nodes that aren't running Kubernetes.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRootCommand(ctx, c)
		},
	}

	installFlags(cmd.Flags(), &c)
	return cmd
}

func runRootCommand(ctx context.Context, c Opts) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if ok := provider.ValidOperatingSystems[c.OperatingSystem]; !ok {
		return errdefs.InvalidInputf("operating system %q is not supported", c.OperatingSystem)
	}

	if c.PodSyncWorkers == 0 {
		return errdefs.InvalidInput("pod sync workers must be greater than 0")
	}

	var taint *corev1.Taint
	if !c.DisableTaint {
		var err error

		taint, err = getTaint(c)
		if err != nil {
			return err
		}
	}

	// just should used in vk node have taint
	//if !c.DisableTaint {
	//	go func() {
	//		mux := http.NewServeMux()
	//	}()
	//}

	//if err := setupTracing(ctx, c); err != nil {
	//	return err
	//}

	// Create provider k8s cluster client using provider k8s kubeconfig
	client, err := newClient(c.Master, c.KubeConfig)
	if err != nil {
		return err
	}

	// Create a shared informer factory for Kubernetes pods in the current namespace (if specified) and scheduled to the current node.
	podInformerFactory := kubeinformers.NewSharedInformerFactoryWithOptions(
		client,
		c.InformerResyncPeriod,
		kubeinformers.WithNamespace(c.KubeNamespace),
		kubeinformers.WithTweakListOptions(func(options *metav1.ListOptions) {
			options.FieldSelector = fields.OneTermEqualSelector("spec.nodeName", c.NodeName).String()
		}))
	podInformer := podInformerFactory.Core().V1().Pods()

	// Create another shared informer factory for Kubernetes secrets and configmaps (not subject to any selectors).
	scmInformerFactory := kubeinformers.NewSharedInformerFactoryWithOptions(client, c.InformerResyncPeriod)
	// Create a secret informer and a config map informer so we can pass their listers to the resource manager.
	secretInformer := scmInformerFactory.Core().V1().Secrets()
	configMapInformer := scmInformerFactory.Core().V1().ConfigMaps()
	serviceInformer := scmInformerFactory.Core().V1().Services()
	// to add
	namespaceInformer := scmInformerFactory.Core().V1().Namespaces()
	pvcInformer := scmInformerFactory.Core().V1().PersistentVolumeClaims()

	// start provider k8s cluster resource
	go podInformerFactory.Start(ctx.Done())
	go scmInformerFactory.Start(ctx.Done())

	// to lister resource
	rm, err := manager.NewResourceManager(
		podInformer.Lister(),
		secretInformer.Lister(),
		configMapInformer.Lister(),
		serviceInformer.Lister(),
		namespaceInformer.Lister(),
		pvcInformer.Lister(),
	)
	if err != nil {
		return errors.Wrap(err, "could not create resource manager")
	}

	// New provider
	p, err := sci.NewSCIProvider(
		c.ProviderConfigPath,
		c.ClusterName,
		c.ClusterName,
		c.Version,
		os.Getenv("VKUBELET_POD_IP"),
		c.ListenPort,
		client,
		rm)
	if err != nil {
		return errors.Wrapf(err, "error new provider %s", c.Provider)
	}

	// 从provider构建node
	pNode := sci.NodeFromProvider(p, taint)

	// start node controller
	var leaseClient v1beta1.LeaseInterface
	if c.EnableNodeLease {
		leaseClient = client.CoordinationV1beta1().Leases(corev1.NamespaceNodeLease)
	}
	nodeController, err := controller.NewNodeController(
		p, //实现NodeProvider接口
		pNode,
		client.CoreV1().Nodes(),
		controller.WithNodeEnableLeaseV1Beta1(leaseClient, 0),
		controller.WithNodeStatusUpdateErrorHandler(func(ctx context.Context, err error) error {
			if !k8serrors.IsNotFound(err) {
				return err
			}

			klog.Info("node not found, will create it ")
			newNode := pNode.DeepCopy()
			newNode.ResourceVersion = ""
			_, err = client.CoreV1().Nodes().Create(context.TODO(), newNode, metav1.CreateOptions{})
			if err != nil {
				return err
			}
			klog.Info("created new node")
			return nil
		}),
	)
	if err != nil {
		klog.Fatal(err)
	}

	// start pod controller
	eb := record.NewBroadcaster()
	eb.StartLogging(log.G(ctx).Infof)
	eb.StartRecordingToSink(&corev1client.EventSinkImpl{Interface: client.CoreV1().Events(c.KubeNamespace)})

	pc, err := controller.NewPodController(controller.PodControllerConfig{
		Provider:          p, //实现PodLifecycleHandler接口
		ClusterName:       c.ClusterName,
		NodeTaint:         taint,
		PodClient:         client.CoreV1(),
		EventRecorder:     eb.NewRecorder(scheme.Scheme, corev1.EventSource{Component: path.Join(pNode.Name, "pod-controller")}),
		PodInformer:       podInformer,
		SecretInformer:    secretInformer,
		ConfigMapInformer: configMapInformer,
		ServiceInformer:   serviceInformer,
		NamespaceInformer: namespaceInformer,
		EventInformer:     scmInformerFactory.Core().V1().Events(),
	})
	if err != nil {
		return errors.Wrap(err, "error setting up pod controller")
	}
	go func() {
		if err := pc.Run(ctx, c.PodSyncWorkers); err != nil && errors.Cause(err) != context.Canceled {
			klog.Fatal(err)
		}
	}()
	if c.StartupTimeout > 0 {
		// If there is a startup timeout, it does two things:
		// 1. It causes the VK to shutdown if we haven't gotten into an operational state in a time period
		// 2. It prevents node advertisement from happening until we're in an operational state
		err = waitFor(ctx, c.StartupTimeout, pc.Ready())
		if err != nil {
			return err
		}
	}

	// node controller需要最后启动
	go func() {
		if err := nodeController.Run(ctx); err != nil {
			log.G(ctx).Fatal(err)
		}
	}()
	nodeController.NotifyVKNodeResource(c.ProviderConfigPath, pNode, client)

	// start http server
	apiConfig, err := getAPIConfig(c)
	if err != nil {
		return err
	}
	cancelHTTP, err := setupHTTPServer(ctx, p, apiConfig)
	if err != nil {
		return err
	}
	defer cancelHTTP()

	klog.Info("Initialized")

	<-ctx.Done()
	return nil

}

func newClient(master, configPath string) (*kubernetes.Clientset, error) {
	var config *rest.Config

	// Check if the kubeConfig file exists.
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		// Get the kubeconfig from the filepath.
		config, err = clientcmd.BuildConfigFromFlags(master, configPath)
		if err != nil {
			return nil, errors.Wrap(err, "error building client config")
		}
	} else {
		// Set to in-cluster config.
		config, err = rest.InClusterConfig()
		if err != nil {
			return nil, errors.Wrap(err, "error building in cluster config")
		}
	}

	if masterURI := os.Getenv("MASTER_URI"); masterURI != "" {
		config.Host = masterURI
	}

	return kubernetes.NewForConfig(rest.AddUserAgent(config, VKNodeUserAgent))
}

func waitFor(ctx context.Context, time time.Duration, ready <-chan struct{}) error {
	ctx, cancel := context.WithTimeout(ctx, time)
	defer cancel()

	// Wait for the VK / PC close the the ready channel, or time out and return
	log.G(ctx).Info("Waiting for pod controller / VK to be ready")

	select {
	case <-ready:
		return nil
	case <-ctx.Done():
		return errors.Wrap(ctx.Err(), "Error while starting up VK")
	}
}
