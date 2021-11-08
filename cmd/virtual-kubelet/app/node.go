package app

import (
	"github.com/virtual-kubelet/virtual-kubelet/errdefs"
	corev1 "k8s.io/api/core/v1"
)

// getTaint creates a taint using the provided key/value.
// Taint effect is read from the environment
// The taint key/value may be overwritten by the environment.
func getTaint(c Opts) (*corev1.Taint, error) {
	value := c.Provider

	key := c.TaintKey
	if key == "" {
		key = DefaultTaintKey
	}

	if c.TaintEffect == "" {
		c.TaintEffect = DefaultTaintEffect
	}

	key = getEnv("VKUBELET_TAINT_KEY", key)
	value = getEnv("VKUBELET_TAINT_VALUE", value)
	effectEnv := getEnv("VKUBELET_TAINT_EFFECT", string(c.TaintEffect))

	var effect corev1.TaintEffect
	switch effectEnv {
	case "NoSchedule":
		effect = corev1.TaintEffectNoSchedule
	case "NoExecute":
		effect = corev1.TaintEffectNoExecute
	case "PreferNoSchedule":
		effect = corev1.TaintEffectPreferNoSchedule
	default:
		return nil, errdefs.InvalidInputf("taint effect %q is not supported", effectEnv)
	}

	return &corev1.Taint{
		Key:    key,
		Value:  value,
		Effect: effect,
	}, nil
}
