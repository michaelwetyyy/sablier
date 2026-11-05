package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// resourceLimits validates only the explicitly supplied CPU/memory limits.
func resourceLimits(cpu, memory string) (corev1.ResourceList, error) {
	limits := corev1.ResourceList{}

	if cpu != "" {
		q, err := resource.ParseQuantity(cpu)
		if err != nil {
			return nil, fmt.Errorf("invalid CPU value %q: %w", cpu, err)
		}
		limits[corev1.ResourceCPU] = q
	}

	if memory != "" {
		q, err := resource.ParseQuantity(memory)
		if err != nil {
			return nil, fmt.Errorf("invalid memory value %q: %w", memory, err)
		}
		limits[corev1.ResourceMemory] = q
	}
	return limits, nil
}

// buildResourcePatch preserves the existing strategic merge for other targets.
func buildResourcePatch(containerName, cpu, memory string) ([]byte, error) {
	limits, err := resourceLimits(cpu, memory)
	if err != nil {
		return nil, err
	}

	patch := map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []map[string]any{
						{
							"name": containerName,
							"resources": map[string]any{
								"limits": limits,
							},
						},
					},
				},
			},
		},
	}

	return json.Marshal(patch)
}

// scaleResources patches the resource limits of the first container in a
// deployment or statefulset without changing the replica count.
func (p *Provider) scaleResources(ctx context.Context, config ParsedName, cpu, memory string) error {
	switch config.Kind {
	case "deployment":
		return p.scaleDeploymentResources(ctx, config, cpu, memory)
	case "statefulset":
		return p.scaleStatefulSetResources(ctx, config, cpu, memory)
	default:
		return fmt.Errorf("unsupported kind %q for resource scaling", config.Kind)
	}
}

func (p *Provider) scaleDeploymentResources(ctx context.Context, config ParsedName, cpu, memory string) error {
	d, err := p.Client.AppsV1().Deployments(config.Namespace).Get(ctx, config.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("cannot get deployment: %w", err)
	}
	if isMaintenancePrimary(config) {
		if err := checkMaintenancePrimarySnapshot(config, d); err != nil {
			return err
		}
	}

	if len(d.Spec.Template.Spec.Containers) == 0 {
		return fmt.Errorf("deployment %s/%s has no containers", config.Namespace, config.Name)
	}

	patchType := types.StrategicMergePatchType
	var patchData []byte
	if isMaintenancePrimary(config) {
		patchType = types.JSONPatchType
		patchData, err = buildMaintenanceResourcePatch(d, cpu, memory)
	} else {
		patchData, err = buildResourcePatch(d.Spec.Template.Spec.Containers[0].Name, cpu, memory)
	}
	if err != nil {
		return err
	}

	_, err = p.Client.AppsV1().Deployments(config.Namespace).Patch(
		ctx, config.Name, patchType, patchData, metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("cannot patch deployment resources: %w", err)
	}
	return nil
}

// The full-object UID/RV tests bind the resource write to the same unmarked
// snapshot. Replacing the complete typed Resources value preserves requests,
// other limits and claims, and works even when the resources field was absent.
// Do not retry a failed test or reuse the preceding replica write's snapshot.
func buildMaintenanceResourcePatch(d *appsv1.Deployment, cpu, memory string) ([]byte, error) {
	limits, err := resourceLimits(cpu, memory)
	if err != nil {
		return nil, err
	}
	container := d.Spec.Template.Spec.Containers[0]
	resources := container.Resources.DeepCopy()
	if resources.Limits == nil {
		resources.Limits = corev1.ResourceList{}
	}
	for name, quantity := range limits {
		resources.Limits[name] = quantity
	}
	type operation struct {
		Op    string `json:"op"`
		Path  string `json:"path"`
		Value any    `json:"value"`
	}
	return json.Marshal([]operation{
		{Op: "test", Path: "/metadata/uid", Value: d.UID},
		{Op: "test", Path: "/metadata/resourceVersion", Value: d.ResourceVersion},
		{Op: "test", Path: "/spec/template/spec/containers/0/name", Value: container.Name},
		{Op: "add", Path: "/spec/template/spec/containers/0/resources", Value: resources},
	})
}

func (p *Provider) scaleStatefulSetResources(ctx context.Context, config ParsedName, cpu, memory string) error {
	ss, err := p.Client.AppsV1().StatefulSets(config.Namespace).Get(ctx, config.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("cannot get statefulset: %w", err)
	}

	if len(ss.Spec.Template.Spec.Containers) == 0 {
		return fmt.Errorf("statefulset %s/%s has no containers", config.Namespace, config.Name)
	}

	patchData, err := buildResourcePatch(ss.Spec.Template.Spec.Containers[0].Name, cpu, memory)
	if err != nil {
		return err
	}

	_, err = p.Client.AppsV1().StatefulSets(config.Namespace).Patch(
		ctx, config.Name, types.StrategicMergePatchType, patchData, metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("cannot patch statefulset resources: %w", err)
	}
	return nil
}

// getWorkloadLabels retrieves the Sablier configuration of a deployment or
// statefulset, merging labels with "sablier.*" annotations (annotations take
// precedence). This lets scale-mode values that are invalid as label values
// (e.g. comma-separated blkio device lists) be provided via annotations.
func (p *Provider) getWorkloadLabels(ctx context.Context, config ParsedName) (map[string]string, error) {
	switch config.Kind {
	case "deployment":
		d, err := p.Client.AppsV1().Deployments(config.Namespace).Get(ctx, config.Name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return sablierConfig(d.Labels, d.Annotations), nil
	case "statefulset":
		ss, err := p.Client.AppsV1().StatefulSets(config.Namespace).Get(ctx, config.Name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return sablierConfig(ss.Labels, ss.Annotations), nil
	default:
		return nil, fmt.Errorf("unsupported kind %q", config.Kind)
	}
}
