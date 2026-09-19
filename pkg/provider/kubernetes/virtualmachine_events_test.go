package kubernetes

import (
	"context"
	"testing"

	"github.com/neilotoole/slogt"
	"github.com/sablierapp/sablier/pkg/provider"
	"github.com/sablierapp/sablier/pkg/sablier"
	"gotest.tools/v3/assert"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestProvider_VirtualMachineCRDInstalled(t *testing.T) {
	t.Parallel()

	groupVersion := virtualMachineGVR.GroupVersion().String()

	t.Run("false when dynamic client is nil", func(t *testing.T) {
		t.Parallel()
		p := &Provider{Client: k8sfake.NewSimpleClientset(), l: slogt.New(t)}
		assert.Equal(t, p.virtualMachineCRDInstalled(context.Background()), false)
	})

	t.Run("false when typed client is nil", func(t *testing.T) {
		t.Parallel()
		p := newFakeKubeVirtProvider(t)
		assert.Equal(t, p.virtualMachineCRDInstalled(context.Background()), false)
	})

	t.Run("true when virtualmachines are served", func(t *testing.T) {
		t.Parallel()
		client := k8sfake.NewSimpleClientset()
		client.Resources = []*metav1.APIResourceList{{
			GroupVersion: groupVersion,
			APIResources: []metav1.APIResource{{Name: "virtualmachineinstances"}, {Name: virtualMachineGVR.Resource}},
		}}
		p := newFakeKubeVirtProvider(t)
		p.Client = client
		assert.Equal(t, p.virtualMachineCRDInstalled(context.Background()), true)
	})

	t.Run("false when kubevirt is served without virtualmachines", func(t *testing.T) {
		t.Parallel()
		client := k8sfake.NewSimpleClientset()
		client.Resources = []*metav1.APIResourceList{{
			GroupVersion: groupVersion,
			APIResources: []metav1.APIResource{{Name: "virtualmachineinstances"}},
		}}
		p := newFakeKubeVirtProvider(t)
		p.Client = client
		assert.Equal(t, p.virtualMachineCRDInstalled(context.Background()), false)
	})

	t.Run("true on non-NotFound discovery error", func(t *testing.T) {
		t.Parallel()
		client := k8sfake.NewSimpleClientset()
		client.PrependReactor("get", "resource", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{}, "", nil)
		})
		p := newFakeKubeVirtProvider(t)
		p.Client = client
		assert.Equal(t, p.virtualMachineCRDInstalled(context.Background()), true)
	})
}

func TestProvider_VirtualMachineLifecycleEvents(t *testing.T) {
	t.Parallel()

	t.Run("running to halted emits stopped", func(t *testing.T) {
		t.Parallel()
		oldVM := newVirtualMachineObj("parhelion", "windows-01", map[string]string{"sablier.enable": "true"}, "Always", nil, true, "Running")
		oldVM.SetResourceVersion("1")
		newVM := newVirtualMachineObj("parhelion", "windows-01", map[string]string{"sablier.enable": "true"}, "Halted", nil, false, "Stopped")
		newVM.SetResourceVersion("2")
		p := newFakeKubeVirtProvider(t, newVM.DeepCopy())
		events := make(chan sablier.InstanceEvent, 1)
		p.virtualMachineEventHandler(context.Background(), events, true, true, false, false).UpdateFunc(oldVM, newVM)
		event := <-events
		assert.Equal(t, event.Type, provider.InstanceEventStopped)
		assert.Equal(t, event.Info.Status, sablier.InstanceStatusStopped)
	})

	t.Run("halted to running emits started", func(t *testing.T) {
		t.Parallel()
		oldVM := newVirtualMachineObj("parhelion", "windows-01", map[string]string{"sablier.enable": "true"}, "Halted", nil, false, "Stopped")
		oldVM.SetResourceVersion("1")
		newVM := newVirtualMachineObj("parhelion", "windows-01", map[string]string{"sablier.enable": "true"}, "Always", nil, false, "Starting")
		newVM.SetResourceVersion("2")
		p := newFakeKubeVirtProvider(t, newVM.DeepCopy())
		events := make(chan sablier.InstanceEvent, 1)
		p.virtualMachineEventHandler(context.Background(), events, true, true, false, false).UpdateFunc(oldVM, newVM)
		event := <-events
		assert.Equal(t, event.Type, provider.InstanceEventStarted)
		assert.Equal(t, event.Info.Status, sablier.InstanceStatusStarting)
	})

	t.Run("irrelevant update emits nothing", func(t *testing.T) {
		t.Parallel()
		oldVM := newVirtualMachineObj("parhelion", "windows-01", nil, "Always", nil, true, "Running")
		oldVM.SetResourceVersion("1")
		newVM := oldVM.DeepCopy()
		newVM.SetResourceVersion("2")
		p := newFakeKubeVirtProvider(t, newVM.DeepCopy())
		events := make(chan sablier.InstanceEvent, 1)
		p.virtualMachineEventHandler(context.Background(), events, true, true, false, false).UpdateFunc(oldVM, newVM)
		select {
		case event := <-events:
			t.Fatalf("unexpected event: %#v", event)
		default:
		}
	})
}
