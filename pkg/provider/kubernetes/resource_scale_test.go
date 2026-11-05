package kubernetes

// Unit tests for buildResourcePatch and resource scaling helpers.
// These run without a real Kubernetes cluster.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	"gotest.tools/v3/assert"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestBuildResourcePatch_CPUAndMemory(t *testing.T) {
	data, err := buildResourcePatch("mycontainer", "500m", "128Mi")
	assert.NilError(t, err)

	var doc map[string]any
	assert.NilError(t, json.Unmarshal(data, &doc))

	spec := doc["spec"].(map[string]any)
	template := spec["template"].(map[string]any)
	podSpec := template["spec"].(map[string]any)
	containers := podSpec["containers"].([]any)
	assert.Equal(t, len(containers), 1)

	c := containers[0].(map[string]any)
	assert.Equal(t, c["name"].(string), "mycontainer")

	resources := c["resources"].(map[string]any)
	limits := resources["limits"].(map[string]any)
	assert.Equal(t, limits["cpu"].(string), "500m")
	assert.Equal(t, limits["memory"].(string), "128Mi")
}

func TestBuildResourcePatch_CPUOnly(t *testing.T) {
	data, err := buildResourcePatch("app", "0.5", "")
	assert.NilError(t, err)

	var doc map[string]any
	assert.NilError(t, json.Unmarshal(data, &doc))

	spec := doc["spec"].(map[string]any)
	template := spec["template"].(map[string]any)
	podSpec := template["spec"].(map[string]any)
	containers := podSpec["containers"].([]any)
	c := containers[0].(map[string]any)
	resources := c["resources"].(map[string]any)
	limits := resources["limits"].(map[string]any)

	// CPU should be set, memory should not appear in the patch
	_, hasCPU := limits["cpu"]
	assert.Assert(t, hasCPU, "cpu should be in limits")
	_, hasMemory := limits["memory"]
	assert.Assert(t, !hasMemory, "memory should not appear when empty")
}

func TestBuildResourcePatch_InvalidCPU(t *testing.T) {
	_, err := buildResourcePatch("app", "bad-cpu", "")
	assert.Assert(t, err != nil, "expected error for invalid CPU")
}

func TestBuildResourcePatch_InvalidMemory(t *testing.T) {
	_, err := buildResourcePatch("app", "", "bad-memory")
	assert.Assert(t, err != nil, "expected error for invalid memory")
}

func newFakeProvider() *Provider {
	return &Provider{
		Client:    fake.NewSimpleClientset(),
		delimiter: "_",
		l:         slog.Default(),
	}
}

func TestScaleResources_UnsupportedKind(t *testing.T) {
	p := newFakeProvider()
	err := p.scaleResources(context.Background(), ParsedName{Kind: "daemonset"}, "100m", "")
	assert.ErrorContains(t, err, "unsupported kind")
}

func TestGetWorkloadLabels_UnsupportedKind(t *testing.T) {
	p := newFakeProvider()
	_, err := p.getWorkloadLabels(context.Background(), ParsedName{Kind: "daemonset"})
	assert.ErrorContains(t, err, "unsupported kind")
}

func maintenanceResourceFixture() (ParsedName, *appsv1.Deployment) {
	config, d := maintenancePrimaryFixture()
	d.Spec.Template.Spec.Containers = []corev1.Container{
		{Name: "runner", Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi")},
			Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m"), corev1.ResourceMemory: resource.MustParse("256Mi"), "example.com/gpu": resource.MustParse("1")},
		}},
		{Name: "sidecar", Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")}}},
	}
	return config, d
}

func TestMaintenanceResourceFenceMarkedPrimaryNeverPatches(t *testing.T) {
	for _, marker := range []string{"", "malformed", "{", `{"deadline":0}`} {
		config, d := maintenanceResourceFixture()
		d.Annotations = map[string]string{maintenanceFenceAnnotation: marker}
		client := fake.NewSimpleClientset(d)
		err := maintenanceTestProvider(client).scaleResources(context.Background(), config, "500m", "512Mi")
		if !apierrors.IsConflict(err) {
			t.Fatalf("resource marker presence must conflict: %v", err)
		}
		requireOnlyDeploymentGet(t, client)
	}
}

func TestMaintenanceResourceFencePatchPreservesOtherResources(t *testing.T) {
	for _, limits := range [][2]string{{"500m", ""}, {"", "512Mi"}, {"500m", "512Mi"}} {
		config, d := maintenanceResourceFixture()
		before, current := d.DeepCopy(), d.DeepCopy()
		client := fake.NewSimpleClientset(d)
		client.PrependReactor("patch", "deployments", func(a clienttesting.Action) (bool, runtime.Object, error) {
			patchAction := a.(clienttesting.PatchAction)
			if patchAction.GetPatchType() != types.JSONPatchType || a.GetSubresource() != "" {
				t.Fatal("primary resource mutation must be conditional JSON Patch")
			}
			var operations []struct {
				Op, Path string
				Value    any
			}
			assert.NilError(t, json.Unmarshal(patchAction.GetPatch(), &operations))
			if len(operations) != 4 || operations[0].Op != "test" || operations[0].Path != "/metadata/uid" || operations[0].Value != string(d.UID) || operations[1].Op != "test" || operations[1].Path != "/metadata/resourceVersion" || operations[1].Value != d.ResourceVersion {
				t.Fatalf("missing original UID/opaque RV tests: %s", patchAction.GetPatch())
			}
			patch, err := jsonpatch.DecodePatch(patchAction.GetPatch())
			assert.NilError(t, err)
			raw, err := json.Marshal(current)
			assert.NilError(t, err)
			after, err := patch.Apply(raw)
			assert.NilError(t, err)
			assert.NilError(t, json.Unmarshal(after, current))
			return true, current.DeepCopy(), nil
		})
		assert.NilError(t, maintenanceTestProvider(client).scaleResources(context.Background(), config, limits[0], limits[1]))
		if len(client.Actions()) != 2 {
			t.Fatalf("expected one nearest GET and one PATCH, got %#v", client.Actions())
		}
		got := current.Spec.Template.Spec.Containers
		assert.DeepEqual(t, got[0].Resources.Requests, before.Spec.Template.Spec.Containers[0].Resources.Requests)
		assert.DeepEqual(t, got[1], before.Spec.Template.Spec.Containers[1])
		assert.DeepEqual(t, got[0].Resources.Limits["example.com/gpu"], resource.MustParse("1"))
		wantCPU, wantMemory := "200m", "256Mi"
		if limits[0] != "" {
			wantCPU = limits[0]
		}
		if limits[1] != "" {
			wantMemory = limits[1]
		}
		cpu, memory := got[0].Resources.Limits[corev1.ResourceCPU], got[0].Resources.Limits[corev1.ResourceMemory]
		if cpu.Cmp(resource.MustParse(wantCPU)) != 0 || memory.Cmp(resource.MustParse(wantMemory)) != 0 {
			t.Fatalf("wrong resource update: %#v", got[0].Resources)
		}
	}
}

func TestMaintenanceResourceFenceDelayedPatchCannotCrossMarkerOrReplacement(t *testing.T) {
	for _, change := range []string{"marker-cas", "replacement-uid"} {
		t.Run(change, func(t *testing.T) {
			config, d := maintenanceResourceFixture()
			current := d.DeepCopy()
			client := fake.NewSimpleClientset()
			gets, patches := 0, 0
			client.PrependReactor("get", "deployments", func(a clienttesting.Action) (bool, runtime.Object, error) {
				gets++
				snapshot := current.DeepCopy()
				if change == "marker-cas" {
					current.ResourceVersion = "opaque:marker/B"
					current.Annotations = map[string]string{maintenanceFenceAnnotation: "broker-owned"}
				} else {
					current.UID = "replacement-deployment-uid"
				}
				return true, snapshot, nil
			})
			client.PrependReactor("patch", "deployments", func(a clienttesting.Action) (bool, runtime.Object, error) {
				patches++
				patch, err := jsonpatch.DecodePatch(a.(clienttesting.PatchAction).GetPatch())
				assert.NilError(t, err)
				raw, err := json.Marshal(current)
				assert.NilError(t, err)
				if _, err := patch.Apply(raw); err == nil {
					t.Fatal("stale JSON Patch tests unexpectedly passed")
				}
				return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "deployments"}, config.Name, errors.New("modeled failed JSON Patch precondition"))
			})
			err := maintenanceTestProvider(client).scaleResources(context.Background(), config, "500m", "512Mi")
			if !apierrors.IsConflict(err) || gets != 1 || patches != 1 {
				t.Fatalf("stale resource write retried or lost error: %v, gets=%d patches=%d", err, gets, patches)
			}
			assert.DeepEqual(t, current.Spec, d.Spec)
		})
	}
}

func TestMaintenanceResourceFenceChecksAgainAfterReplicaWrite(t *testing.T) {
	for _, start := range []bool{false, true} {
		config, d := maintenanceResourceFixture()
		d.Labels = map[string]string{"sablier.active.cpu": "500m", "sablier.idle.replicas": "1", "sablier.idle.cpu": "100m"}
		current := d.DeepCopy()
		client := fake.NewSimpleClientset()
		gets, scales := 0, 0
		client.PrependReactor("get", "deployments", func(a clienttesting.Action) (bool, runtime.Object, error) {
			gets++
			if a.GetSubresource() != "" {
				t.Fatal("must not refresh through GetScale")
			}
			return true, current.DeepCopy(), nil
		})
		client.PrependReactor("update", "deployments", func(a clienttesting.Action) (bool, runtime.Object, error) {
			scales++
			s := a.(clienttesting.UpdateAction).GetObject().(*autoscalingv1.Scale)
			if a.GetSubresource() != "scale" || s.UID != current.UID || s.ResourceVersion != current.ResourceVersion {
				t.Fatal("replica write lost its own snapshot")
			}
			// Model broker fence installation after successful replica scaling.
			current.ResourceVersion = "opaque:fence/after-scale"
			current.Annotations = map[string]string{maintenanceFenceAnnotation: "broker-owned"}
			return true, s, nil
		})
		p := maintenanceTestProvider(client)
		name := "deployment_" + config.Namespace + "_" + config.Name + "_1"
		var err error
		if start {
			err = p.InstanceStart(context.Background(), name)
		} else {
			err = p.InstanceStop(context.Background(), name)
		}
		if !apierrors.IsConflict(err) || gets != 3 || scales != 1 {
			t.Fatalf("later mutation reused earlier authority: %v gets=%d scales=%d", err, gets, scales)
		}
		for _, a := range client.Actions() {
			if a.GetVerb() == "patch" {
				t.Fatal("resource patch must not follow new fence")
			}
		}
	}
}

func TestMaintenanceResourceFenceOtherDeploymentKeepsStrategicMerge(t *testing.T) {
	config, d := maintenanceResourceFixture()
	config.Name, d.Name = "other", "other"
	d.Annotations = map[string]string{maintenanceFenceAnnotation: "not-the-primary"}
	client := fake.NewSimpleClientset(d)
	want, err := buildResourcePatch(d.Spec.Template.Spec.Containers[0].Name, "500m", "")
	assert.NilError(t, err)
	client.PrependReactor("patch", "deployments", func(a clienttesting.Action) (bool, runtime.Object, error) {
		p := a.(clienttesting.PatchAction)
		if p.GetPatchType() != types.StrategicMergePatchType || string(p.GetPatch()) != string(want) {
			t.Fatalf("other Deployment patch changed: %s", p.GetPatch())
		}
		return true, d, nil
	})
	assert.NilError(t, maintenanceTestProvider(client).scaleResources(context.Background(), config, "500m", ""))
	if len(client.Actions()) != 2 {
		t.Fatal("other Deployment behavior changed")
	}
}

func TestMaintenanceResourceFenceMissingIdentityOrBadLimitNeverPatches(t *testing.T) {
	for _, bad := range []string{"missing-rv", "bad-limit", "no-containers"} {
		config, d := maintenanceResourceFixture()
		cpu := "500m"
		if bad == "missing-rv" {
			d.ResourceVersion = ""
		}
		if bad == "bad-limit" {
			cpu = "invalid-cpu"
		}
		if bad == "no-containers" {
			d.Spec.Template.Spec.Containers = nil
		}
		client := fake.NewSimpleClientset(d)
		if err := maintenanceTestProvider(client).scaleResources(context.Background(), config, cpu, ""); err == nil {
			t.Fatal("invalid resource mutation did not fail closed")
		}
		requireOnlyDeploymentGet(t, client)
	}
}

func TestMaintenanceResourceFenceAbsentResourcesField(t *testing.T) {
	_, d := maintenanceResourceFixture()
	d.Spec.Template.Spec.Containers[0].Resources = corev1.ResourceRequirements{}
	raw, err := json.Marshal(d)
	assert.NilError(t, err)
	remove, err := jsonpatch.DecodePatch([]byte(`[{"op":"remove","path":"/spec/template/spec/containers/0/resources"}]`))
	assert.NilError(t, err)
	raw, err = remove.Apply(raw)
	assert.NilError(t, err)
	data, err := buildMaintenanceResourcePatch(d, "500m", "")
	assert.NilError(t, err)
	patch, err := jsonpatch.DecodePatch(data)
	assert.NilError(t, err)
	raw, err = patch.Apply(raw)
	assert.NilError(t, err)
	var updated appsv1.Deployment
	assert.NilError(t, json.Unmarshal(raw, &updated))
	cpu := updated.Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceCPU]
	if cpu.Cmp(resource.MustParse("500m")) != 0 {
		t.Fatal("absent resources field was not safely added")
	}
}

func TestMaintenanceResourceFenceUnknownPatchOutcomeIsNotRetried(t *testing.T) {
	config, d := maintenanceResourceFixture()
	client := fake.NewSimpleClientset(d)
	want := errors.New("resource patch outcome unavailable")
	client.PrependReactor("patch", "deployments", func(a clienttesting.Action) (bool, runtime.Object, error) { return true, nil, want })
	if got := maintenanceTestProvider(client).scaleResources(context.Background(), config, "500m", ""); !errors.Is(got, want) || len(client.Actions()) != 2 {
		t.Fatalf("resource patch was retried or lost original error: %v %#v", got, client.Actions())
	}
}
