package sci

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"
	"strings"
)

type SCIConfig struct {
	// Operating system to run pods for
	OperatingSystem string `json:"operatingSystem,omitempty"`
	SystemArch      string `json:"systemArch,omitempty"`

	Region         string `json:"region,omitempty"`
	NodeName       string `json:"nodeName,omitempty"`
	KubeletVersion string `json:"kubeletVersion,omitempty"`
	ClusterName    string `json:"clusterName,omitempty"`

	Cpu    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
	Pods   string `json:"pods,omitempty"`
}

func LoadConfig(providerConfig, nodeName string) (config SCIConfig, err error) {
	data, err := ioutil.ReadFile(providerConfig)
	if err != nil {
		return config, err
	}

	// if node in config, use the value, otherwise use the random line
	config, err = getMockConfig(data, nodeName)
	if err != nil {
		return config, err
	}

	if _, err := resource.ParseQuantity(config.Cpu); err != nil {
		return config, fmt.Errorf("Invalid CPU value: %s, error: %v ", config.Cpu, err)
	}
	if _, err := resource.ParseQuantity(config.Memory); err != nil {
		return config, fmt.Errorf("Invalid Memory value: %s, error: %v ", config.Cpu, err)
	}
	if _, err := resource.ParseQuantity(config.Pods); err != nil {
		return config, fmt.Errorf("Invalid Pods value: %s, error: %v ", config.Cpu, err)
	}

	return config, nil
}

func getMockConfig(data []byte, nodeName string) (config SCIConfig, err error) {
	configMap := map[string]SCIConfig{}
	if err := json.Unmarshal(data, &configMap); err != nil {
		return config, err
	}

	var lastConfigNode string
	for name, config := range configMap {
		lastConfigNode = name
		if strings.ToLower(name) == nodeName {
			return config, nil
		}
	}
	if lastConfigNode != "" {
		return configMap[lastConfigNode], nil
	}
	klog.Errorf("no node %s in config file", nodeName)

	return config, fmt.Errorf("no node %s in config file", nodeName)
}
