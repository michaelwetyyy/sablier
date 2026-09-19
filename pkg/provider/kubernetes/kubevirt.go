package kubernetes

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// KindVirtualMachine is the workload kind used in Sablier instance names for
// KubeVirt VirtualMachine resources, for example:
// virtualmachine_default_windows-01_1.
const KindVirtualMachine = "virtualmachine"

var virtualMachineGVR = schema.GroupVersionResource{
	Group:    "kubevirt.io",
	Version:  "v1",
	Resource: "virtualmachines",
}

const (
	kubeVirtRunStrategyAlways = "Always"
	kubeVirtRunStrategyHalted = "Halted"
)

func VirtualMachineName(namespace, name string, opts ParseOptions) ParsedName {
	original := fmt.Sprintf("%s%s%s%s%s%s%d", KindVirtualMachine, opts.Delimiter, namespace, opts.Delimiter, name, opts.Delimiter, 1)
	return ParsedName{
		Original:  original,
		Kind:      KindVirtualMachine,
		Namespace: namespace,
		Name:      name,
		Replicas:  1,
	}
}

// virtualMachineDesiredRunning supports both the current runStrategy API and
// the legacy spec.running field. KubeVirt rejects objects that set both, but
// reading both here makes discovery work across older VM manifests too.
func virtualMachineDesiredRunning(vm *unstructured.Unstructured) bool {
	if strategy, found, _ := unstructured.NestedString(vm.Object, "spec", "runStrategy"); found {
		switch strategy {
		case kubeVirtRunStrategyAlways:
			return true
		case kubeVirtRunStrategyHalted:
			return false
		default:
			// Manual, Once and RerunOnFailure do not fully encode current
			// running state. KubeVirt status.created means a VMI exists.
			if created, ok, _ := unstructured.NestedBool(vm.Object, "status", "created"); ok {
				return created
			}
			if printable, ok, _ := unstructured.NestedString(vm.Object, "status", "printableStatus"); ok {
				return printable != "Stopped"
			}
			return false
		}
	}
	if running, found, _ := unstructured.NestedBool(vm.Object, "spec", "running"); found {
		return running
	}
	if created, found, _ := unstructured.NestedBool(vm.Object, "status", "created"); found {
		return created
	}
	return false
}

func virtualMachineImage(vm *unstructured.Unstructured) string {
	volumes, found, _ := unstructured.NestedSlice(vm.Object, "spec", "template", "spec", "volumes")
	if !found {
		return ""
	}
	for _, raw := range volumes {
		volume, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		containerDisk, ok := volume["containerDisk"].(map[string]any)
		if !ok {
			continue
		}
		if image, ok := containerDisk["image"].(string); ok {
			return image
		}
	}
	return ""
}
