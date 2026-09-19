package kubernetes

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/sablierapp/sablier/pkg/provider"
	"github.com/sablierapp/sablier/pkg/sablier"
)

func (p *Provider) listVirtualMachines(ctx context.Context) ([]unstructured.Unstructured, error) {
	if p.dynamic == nil {
		return nil, nil
	}

	items, err := listEnabled(func(opts metav1.ListOptions) ([]unstructured.Unstructured, error) {
		list, err := p.dynamic.Resource(virtualMachineGVR).Namespace(metav1.NamespaceAll).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		return list.Items, nil
	})
	if err != nil {
		if apierrors.IsNotFound(err) {
			p.l.DebugContext(ctx, "kubevirt CRD not installed, skipping virtualmachine discovery")
			return nil, nil
		}
		return nil, err
	}
	return items, nil
}

func (p *Provider) VirtualMachineList(ctx context.Context, opts provider.InstanceListOptions) ([]sablier.InstanceConfiguration, error) {
	items, err := p.listVirtualMachines(ctx)
	if err != nil {
		return nil, err
	}
	instances := make([]sablier.InstanceConfiguration, 0, len(items))
	for i := range items {
		if !opts.All && !virtualMachineDesiredRunning(&items[i]) {
			continue
		}
		instances = append(instances, p.virtualMachineToInstance(&items[i]))
	}
	return instances, nil
}

func (p *Provider) virtualMachineToInstance(vm *unstructured.Unstructured) sablier.InstanceConfiguration {
	config := sablierConfig(vm.GetLabels(), vm.GetAnnotations())
	enabled := config[sablier.LabelEnable]
	var groups []string
	if enabled == "true" {
		groups = sablier.ParseGroups(config[sablier.LabelGroup])
	}
	parsed := VirtualMachineName(vm.GetNamespace(), vm.GetName(), ParseOptions{Delimiter: p.delimiter})
	return sablier.InstanceConfiguration{Name: parsed.Original, Groups: groups, Enabled: enabled}
}

func (p *Provider) VirtualMachineGroups(ctx context.Context) (map[string][]string, error) {
	items, err := p.listVirtualMachines(ctx)
	if err != nil {
		return nil, err
	}
	groups := make(map[string][]string)
	for i := range items {
		vm := &items[i]
		parsed := VirtualMachineName(vm.GetNamespace(), vm.GetName(), ParseOptions{Delimiter: p.delimiter})
		config := sablierConfig(vm.GetLabels(), vm.GetAnnotations())
		for _, groupName := range sablier.ParseGroups(config[sablier.LabelGroup]) {
			groups[groupName] = append(groups[groupName], parsed.Original)
		}
	}
	return groups, nil
}
