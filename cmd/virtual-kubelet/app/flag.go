package app

import (
	"flag"
	"github.com/spf13/pflag"
	"k8s.io/klog/v2"
	"os"
)

func installFlags(flags *pflag.FlagSet, c *Opts) {
	flags.StringVar(&c.KubeConfig, "kubeconfig", c.KubeConfig, "kube config file to use for connecting to the Kubernetes API server")
	flags.StringVar(&c.KubeNamespace, "namespace", c.KubeNamespace, "kubernetes namespace (default is 'all')")
	flags.StringVar(&c.NodeName, "nodename", c.NodeName, "kubernetes node name")
	flags.StringVar(&c.OperatingSystem, "os", c.OperatingSystem, "Operating System (Linux/Windows)")

	flags.StringVar(&c.Provider, "provider", c.Provider, "cloud provider")
	if err := flags.MarkDeprecated("provider", "this flag is not used, alibabacloud is the only supported provider"); err != nil {
		panic(err)
	}

	flags.StringVar(&c.ProviderConfigPath, "provider-config", c.ProviderConfigPath, "cloud provider configuration file")
	flags.StringVar(&c.MetricsAddr, "metrics-addr", c.MetricsAddr, "address to listen for metrics/stats requests")

	flags.StringVar(&c.TaintKey, "taint", c.TaintKey, "Set node taint key")
	flags.BoolVar(&c.DisableTaint, "disable-taint", c.DisableTaint, "disable the virtual-kubelet node taint")
	//flags.MarkDeprecated("taint", "Taint key should now be configured using the VK_TAINT_KEY environment variable")

	flags.IntVar(&c.PodSyncWorkers, "pod-sync-workers", c.PodSyncWorkers, `set the number of pod synchronization workers`)
	flags.BoolVar(&c.EnableNodeLease, "enable-node-lease", c.EnableNodeLease, `use node leases (1.13) for node heartbeats`)

	//flags.StringSliceVar(&c.TraceExporters, "trace-exporter", c.TraceExporters, fmt.Sprintf("sets the tracing exporter to use, available exporters: %s", AvailableTraceExporters()))
	//flags.StringVar(&c.TraceConfig.ServiceName, "trace-service-name", c.TraceConfig.ServiceName, "sets the name of the service used to register with the trace exporter")
	//flags.Var(mapVar(c.TraceConfig.Tags), "trace-tag", "add tags to include with traces in key=value form")
	//flags.StringVar(&c.TraceSampleRate, "trace-sample-rate", c.TraceSampleRate, "set probability of tracing samples")

	flags.DurationVar(&c.InformerResyncPeriod, "full-resync-period", c.InformerResyncPeriod, "how often to perform a full resync of pods between kubernetes and the provider")
	flags.DurationVar(&c.StartupTimeout, "startup-timeout", c.StartupTimeout, "How long to wait for the virtual-kubelet to start")

	flagset := flag.NewFlagSet("klog", flag.PanicOnError)
	klog.InitFlags(flagset)
	flagset.VisitAll(func(f *flag.Flag) {
		f.Name = "klog." + f.Name
		flags.AddGoFlag(f)
	})
}

func getEnv(key, defaultValue string) string {
	value, found := os.LookupEnv(key)
	if found {
		return value
	}
	return defaultValue
}
