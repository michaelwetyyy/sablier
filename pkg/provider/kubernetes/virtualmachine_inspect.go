package kubernetes

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/sablierapp/sablier/pkg/sablier"
)

func (p *Provider) VirtualMachineInspect(ctx context.Context, config ParsedName) (sablier.InstanceInfo, error) {
	if p.dynamic == nil {
		return sablier.InstanceInfo{}, fmt.Errorf("virtualmachine support requires a dynamic client, none configured")
	}
	vm, err := p.dynamic.Resource(virtualMachineGVR).Namespace(config.Namespace).Get(ctx, config.Name, metav1.GetOptions{})
	if err != nil {
		return sablier.InstanceInfo{}, fmt.Errorf("error getting virtualmachine: %w", err)
	}

	desiredRunning := virtualMachineDesiredRunning(vm)
	ready, _, _ := unstructured.NestedBool(vm.Object, "status", "ready")
	printableStatus, _, _ := unstructured.NestedString(vm.Object, "status", "printableStatus")

	status := sablier.InstanceStatusStarting
	switch {
	case !desiredRunning || strings.EqualFold(printableStatus, "Stopped"):
		status = sablier.InstanceStatusStopped
	case ready || strings.EqualFold(printableStatus, "Running"):
		status = sablier.InstanceStatusReady
	case virtualMachineStatusIsError(printableStatus):
		status = sablier.InstanceStatusError
	}

	current := int32(0)
	if status == sablier.InstanceStatusReady {
		current = 1
	}
	info := sablier.InstanceInfo{
		Name:            config.Original,
		CurrentReplicas: current,
		DesiredReplicas: config.Replicas,
		Status:          status,
		Provider:        sablier.ProviderKubernetes,
	}
	labels := vm.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	sablier.PopulateEnabledAndGroup(&info, sablierConfig(labels, vm.GetAnnotations()))
	info.Kubernetes = &sablier.KubernetesWorkloadInfo{
		Namespace: config.Namespace,
		Kind:      KindVirtualMachine,
		Image:     virtualMachineImage(vm),
		Labels:    labels,
	}
	return info, nil
}

func virtualMachineStatusIsError(status string) bool {
	switch strings.ToLower(status) {
	case "errorunschedulable", "unschedulable", "errimagepull", "imagepullbackoff",
		"crashloopbackoff", "pvcnotfound", "datavolumeerror", "unknown":
		return true
	default:
		return false
	}
}
