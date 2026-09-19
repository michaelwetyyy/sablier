package kubernetes

import (
	"context"
	"testing"

	"github.com/neilotoole/slogt"
	"github.com/sablierapp/sablier/pkg/provider"
	"github.com/sablierapp/sablier/pkg/sablier"
	"go.opentelemetry.io/otel"
	"gotest.tools/v3/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func newVirtualMachineObj(namespace, name string, labels map[string]string, runStrategy string, running *bool, ready bool, printableStatus string) *unstructured.Unstructured {
	vm := &unstructured.Unstructured{}
	vm.SetAPIVersion("kubevirt.io/v1")
	vm.SetKind("VirtualMachine")
	vm.SetNamespace(namespace)
	vm.SetName(name)
	if labels != nil {
		vm.SetLabels(labels)
	}
	if runStrategy != "" {
		_ = unstructured.SetNestedField(vm.Object, runStrategy, "spec", "runStrategy")
	}
	if running != nil {
		_ = unstructured.SetNestedField(vm.Object, *running, "spec", "running")
	}
	_ = unstructured.SetNestedSlice(vm.Object, []any{
		map[string]any{
			"name": "rootdisk",
			"containerDisk": map[string]any{
				"image": "quay.io/kubevirt/cirros-container-disk-demo:latest",
			},
		},
	}, "spec", "template", "spec", "volumes")
	_ = unstructured.SetNestedField(vm.Object, ready, "status", "ready")
	if printableStatus != "" {
		_ = unstructured.SetNestedField(vm.Object, printableStatus, "status", "printableStatus")
	}
	return vm
}

func newFakeKubeVirtProvider(t *testing.T, objs ...runtime.Object) *Provider {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{virtualMachineGVR: "VirtualMachineList"},
		objs...,
	)
	return &Provider{
		dynamic:   dyn,
		delimiter: "_",
		l:         slogt.New(t),
		tracer:    otel.Tracer("test"),
	}
}

func TestVirtualMachineDesiredRunning(t *testing.T) {
	t.Parallel()

	trueValue := true
	falseValue := false
	tests := []struct {
		name        string
		runStrategy string
		running     *bool
		want        bool
	}{
		{name: "always", runStrategy: kubeVirtRunStrategyAlways, want: true},
		{name: "rerun on failure without VMI is stopped", runStrategy: "RerunOnFailure", want: false},
		{name: "manual without VMI is stopped", runStrategy: "Manual", want: false},
		{name: "halted", runStrategy: kubeVirtRunStrategyHalted, want: false},
		{name: "legacy running true", running: &trueValue, want: true},
		{name: "legacy running false", running: &falseValue, want: false},
		{name: "unset defaults stopped", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			vm := newVirtualMachineObj("default", "vm", nil, tc.runStrategy, tc.running, false, "")
			assert.Equal(t, virtualMachineDesiredRunning(vm), tc.want)
		})
	}

	t.Run("manual with existing VMI is running", func(t *testing.T) {
		vm := newVirtualMachineObj("default", "vm", nil, "Manual", nil, false, "Running")
		_ = unstructured.SetNestedField(vm.Object, true, "status", "created")
		assert.Equal(t, virtualMachineDesiredRunning(vm), true)
	})

	t.Run("once after completion is stopped", func(t *testing.T) {
		vm := newVirtualMachineObj("default", "vm", nil, "Once", nil, false, "Stopped")
		_ = unstructured.SetNestedField(vm.Object, false, "status", "created")
		assert.Equal(t, virtualMachineDesiredRunning(vm), false)
	})
}

func TestProvider_VirtualMachineInspect(t *testing.T) {
	t.Parallel()

	enabled := map[string]string{"sablier.enable": "true", "sablier.group": "windows"}
	tests := []struct {
		name            string
		runStrategy     string
		ready           bool
		printableStatus string
		wantStatus      sablier.InstanceStatus
		wantReplicas    int32
	}{
		{name: "halted is stopped", runStrategy: "Halted", printableStatus: "Stopped", wantStatus: sablier.InstanceStatusStopped},
		{name: "booting is starting", runStrategy: "Always", printableStatus: "Starting", wantStatus: sablier.InstanceStatusStarting},
		{name: "running ready VM is ready", runStrategy: "Always", ready: true, printableStatus: "Running", wantStatus: sablier.InstanceStatusReady, wantReplicas: 1},
		{name: "running printable status is ready", runStrategy: "Always", printableStatus: "Running", wantStatus: sablier.InstanceStatusReady, wantReplicas: 1},
		{name: "unschedulable is error", runStrategy: "Always", printableStatus: "Unschedulable", wantStatus: sablier.InstanceStatusError},
		{name: "missing PVC is error", runStrategy: "Always", printableStatus: "PvcNotFound", wantStatus: sablier.InstanceStatusError},
		{name: "data volume failure is error", runStrategy: "Always", printableStatus: "DataVolumeError", wantStatus: sablier.InstanceStatusError},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			vm := newVirtualMachineObj("parhelion", "windows-01", enabled, tc.runStrategy, nil, tc.ready, tc.printableStatus)
			p := newFakeKubeVirtProvider(t, vm)
			parsed := VirtualMachineName("parhelion", "windows-01", ParseOptions{Delimiter: "_"})

			info, err := p.VirtualMachineInspect(context.Background(), parsed)
			assert.NilError(t, err)
			assert.Equal(t, info.Status, tc.wantStatus)
			assert.Equal(t, info.CurrentReplicas, tc.wantReplicas)
			assert.Equal(t, info.DesiredReplicas, int32(1))
			assert.Equal(t, info.Provider, sablier.ProviderKubernetes)
			assert.Assert(t, info.Kubernetes != nil)
			assert.Equal(t, info.Kubernetes.Kind, KindVirtualMachine)
			assert.Equal(t, info.Kubernetes.Namespace, "parhelion")
			assert.Equal(t, info.Kubernetes.Image, "quay.io/kubevirt/cirros-container-disk-demo:latest")
			assert.DeepEqual(t, info.Groups, []string{"windows"})
		})
	}
}

func TestProvider_VirtualMachineStartStop(t *testing.T) {
	t.Parallel()

	get := func(t *testing.T, p *Provider) *unstructured.Unstructured {
		t.Helper()
		vm, err := p.dynamic.Resource(virtualMachineGVR).Namespace("parhelion").Get(context.Background(), "windows-01", metav1.GetOptions{})
		assert.NilError(t, err)
		return vm
	}

	t.Run("stop sets Halted", func(t *testing.T) {
		t.Parallel()
		vm := newVirtualMachineObj("parhelion", "windows-01", nil, "Always", nil, true, "Running")
		p := newFakeKubeVirtProvider(t, vm)
		name := VirtualMachineName("parhelion", "windows-01", ParseOptions{Delimiter: "_"}).Original
		assert.NilError(t, p.InstanceStop(context.Background(), name))
		strategy, found, err := unstructured.NestedString(get(t, p).Object, "spec", "runStrategy")
		assert.NilError(t, err)
		assert.Assert(t, found)
		assert.Equal(t, strategy, "Halted")
	})

	t.Run("start sets Always", func(t *testing.T) {
		t.Parallel()
		vm := newVirtualMachineObj("parhelion", "windows-01", nil, "Halted", nil, false, "Stopped")
		p := newFakeKubeVirtProvider(t, vm)
		name := VirtualMachineName("parhelion", "windows-01", ParseOptions{Delimiter: "_"}).Original
		assert.NilError(t, p.InstanceStart(context.Background(), name))
		strategy, found, err := unstructured.NestedString(get(t, p).Object, "spec", "runStrategy")
		assert.NilError(t, err)
		assert.Assert(t, found)
		assert.Equal(t, strategy, "Always")
	})

	t.Run("legacy running field is removed", func(t *testing.T) {
		t.Parallel()
		running := false
		vm := newVirtualMachineObj("parhelion", "windows-01", nil, "", &running, false, "Stopped")
		p := newFakeKubeVirtProvider(t, vm)
		name := VirtualMachineName("parhelion", "windows-01", ParseOptions{Delimiter: "_"}).Original
		assert.NilError(t, p.InstanceStart(context.Background(), name))
		updated := get(t, p)
		_, found, err := unstructured.NestedBool(updated.Object, "spec", "running")
		assert.NilError(t, err)
		assert.Assert(t, !found)
		strategy, found, err := unstructured.NestedString(updated.Object, "spec", "runStrategy")
		assert.NilError(t, err)
		assert.Assert(t, found)
		assert.Equal(t, strategy, "Always")
	})
}

func TestProvider_VirtualMachineListAndGroups(t *testing.T) {
	t.Parallel()

	running := newVirtualMachineObj("parhelion", "windows-01",
		map[string]string{"sablier.enable": "true", "sablier.group": "parhelion"}, "Always", nil, true, "Running")
	halted := newVirtualMachineObj("lab", "toolbox",
		map[string]string{"sablier.enable": "true", "sablier.group": "tools"}, "Halted", nil, false, "Stopped")
	disabled := newVirtualMachineObj("lab", "ignored", nil, "Always", nil, true, "Running")
	p := newFakeKubeVirtProvider(t, running, halted, disabled)

	all, err := p.VirtualMachineList(context.Background(), provider.InstanceListOptions{All: true})
	assert.NilError(t, err)
	assert.Equal(t, len(all), 2)

	runningOnly, err := p.VirtualMachineList(context.Background(), provider.InstanceListOptions{All: false})
	assert.NilError(t, err)
	assert.Equal(t, len(runningOnly), 1)
	assert.Equal(t, runningOnly[0].Name, VirtualMachineName("parhelion", "windows-01", ParseOptions{Delimiter: "_"}).Original)

	groups, err := p.VirtualMachineGroups(context.Background())
	assert.NilError(t, err)
	assert.DeepEqual(t, groups["parhelion"], []string{VirtualMachineName("parhelion", "windows-01", ParseOptions{Delimiter: "_"}).Original})
	assert.DeepEqual(t, groups["tools"], []string{VirtualMachineName("lab", "toolbox", ParseOptions{Delimiter: "_"}).Original})
}
