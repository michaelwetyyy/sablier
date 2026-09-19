package kubernetes

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func (p *Provider) virtualMachineSetRunStrategy(ctx context.Context, config ParsedName, strategy string) error {
	if p.dynamic == nil {
		return fmt.Errorf("virtualmachine support requires a dynamic client, none configured")
	}
	if strategy != kubeVirtRunStrategyAlways && strategy != kubeVirtRunStrategyHalted {
		return fmt.Errorf("unsupported kubevirt runStrategy %q", strategy)
	}

	// spec.running and spec.runStrategy are mutually exclusive in KubeVirt.
	// Nulling the legacy field makes the transition safe for older VM manifests.
	patch := []byte(fmt.Sprintf(`{"spec":{"running":null,"runStrategy":%q}}`, strategy))
	_, err := p.dynamic.Resource(virtualMachineGVR).Namespace(config.Namespace).Patch(
		ctx, config.Name, types.MergePatchType, patch, metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("cannot set runStrategy=%s on virtualmachine %s/%s: %w", strategy, config.Namespace, config.Name, err)
	}
	return nil
}
