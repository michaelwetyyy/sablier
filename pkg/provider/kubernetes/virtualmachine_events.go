package kubernetes

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"

	"github.com/sablierapp/sablier/pkg/provider"
	"github.com/sablierapp/sablier/pkg/sablier"
)

func (p *Provider) virtualMachineCRDInstalled(ctx context.Context) bool {
	if p.dynamic == nil || p.Client == nil {
		return false
	}
	resources, err := p.Client.Discovery().ServerResourcesForGroupVersion(virtualMachineGVR.GroupVersion().String())
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false
		}
		p.l.WarnContext(ctx, "could not verify kubevirt CRD presence, enabling virtualmachine watcher anyway", "error", err)
		return true
	}
	for _, resource := range resources.APIResources {
		if resource.Name == virtualMachineGVR.Resource {
			return true
		}
	}
	return false
}

func virtualMachineFromObject(obj any) (*unstructured.Unstructured, bool) {
	return eventObject[*unstructured.Unstructured](obj)
}

func (p *Provider) watchVirtualMachines(ctx context.Context, events chan<- sablier.InstanceEvent, wantStopped, wantStarted, wantCreated, wantRemoved bool) cache.SharedIndexInformer {
	handler := p.virtualMachineEventHandler(ctx, events, wantStopped, wantStarted, wantCreated, wantRemoved)
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(p.dynamic, 2*time.Second, metav1.NamespaceAll, nil)
	informer := factory.ForResource(virtualMachineGVR).Informer()
	_, _ = informer.AddEventHandler(handler)
	return informer
}

func (p *Provider) virtualMachineEventHandler(ctx context.Context, events chan<- sablier.InstanceEvent, wantStopped, wantStarted, wantCreated, wantRemoved bool) cache.ResourceEventHandlerFuncs {
	return cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			if !wantCreated {
				return
			}
			vm, ok := virtualMachineFromObject(obj)
			if !ok {
				return
			}
			parsed := VirtualMachineName(vm.GetNamespace(), vm.GetName(), ParseOptions{Delimiter: p.delimiter})
			info, err := p.InstanceInspect(ctx, parsed.Original)
			if err != nil {
				p.l.WarnContext(ctx, "inspect after add event failed, using bare info", "virtualmachine", parsed.Original, "error", err)
				info = sablier.InstanceInfo{Name: parsed.Original, Provider: sablier.ProviderKubernetes}
			}
			events <- sablier.InstanceEvent{Type: provider.InstanceEventCreated, Info: info}
		},
		UpdateFunc: func(oldObj, newObj any) {
			oldVM, ok := virtualMachineFromObject(oldObj)
			if !ok {
				return
			}
			newVM, ok := virtualMachineFromObject(newObj)
			if !ok || oldVM.GetResourceVersion() == newVM.GetResourceVersion() {
				return
			}

			wasRunning := virtualMachineDesiredRunning(oldVM)
			isRunning := virtualMachineDesiredRunning(newVM)
			var eventType provider.InstanceEventType
			switch {
			case wantStopped && wasRunning && !isRunning:
				eventType = provider.InstanceEventStopped
			case wantStarted && !wasRunning && isRunning:
				eventType = provider.InstanceEventStarted
			default:
				return
			}

			parsed := VirtualMachineName(newVM.GetNamespace(), newVM.GetName(), ParseOptions{Delimiter: p.delimiter})
			info, err := p.InstanceInspect(ctx, parsed.Original)
			if err != nil {
				p.l.WarnContext(ctx, "inspect after virtualmachine lifecycle event failed, using bare info", "virtualmachine", parsed.Original, "error", err)
				status := sablier.InstanceStatusStarting
				if eventType == provider.InstanceEventStopped {
					status = sablier.InstanceStatusStopped
				}
				info = sablier.InstanceInfo{Name: parsed.Original, Status: status, Provider: sablier.ProviderKubernetes}
			}
			events <- sablier.InstanceEvent{Type: eventType, Info: info}
		},
		DeleteFunc: func(obj any) {
			if !wantRemoved && !wantStopped {
				return
			}
			vm, ok := virtualMachineFromObject(obj)
			if !ok {
				return
			}
			parsed := VirtualMachineName(vm.GetNamespace(), vm.GetName(), ParseOptions{Delimiter: p.delimiter})
			labels := vm.GetLabels()
			info := sablier.InstanceInfo{
				Name:     parsed.Original,
				Status:   sablier.InstanceStatusStopped,
				Provider: sablier.ProviderKubernetes,
				Kubernetes: &sablier.KubernetesWorkloadInfo{
					Namespace: vm.GetNamespace(),
					Kind:      KindVirtualMachine,
					Image:     virtualMachineImage(vm),
					Labels:    labels,
				},
			}
			sablier.PopulateEnabledAndGroup(&info, sablierConfig(labels, vm.GetAnnotations()))
			if wantRemoved {
				events <- sablier.InstanceEvent{Type: provider.InstanceEventRemoved, Info: info}
			}
			if wantStopped {
				events <- sablier.InstanceEvent{Type: provider.InstanceEventStopped, Info: info}
			}
		},
	}
}
