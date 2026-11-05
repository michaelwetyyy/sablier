package kubernetes

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const maintenanceFenceAnnotation = "maintenance.tensorhost.com/fence"

func isMaintenancePrimary(config ParsedName) bool {
	return config.Kind == "deployment" &&
		config.Namespace == "gitea-gitops-actions" &&
		config.Name == "gitops-validation-runner"
}

// scaleMaintenancePrimary participates only in the broker's fixed-primary fence.
// Any reserved marker is a conflict: Sablier never parses, expires or clears it.
// The broker must atomically fence the full Deployment and its replicas. Carry
// this one Deployment snapshot's UID and opaque RV into UpdateScale so a delayed
// ordinary write conflicts after that CAS. Never refresh GetScale or retry here.
// Actual API-server scale-subresource coupling requires separate integration proof.
func (p *Provider) scaleMaintenancePrimary(ctx context.Context, config ParsedName, replicas int32) error {
	deployments := p.Client.AppsV1().Deployments(config.Namespace)
	d, err := deployments.Get(ctx, config.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if err := checkMaintenancePrimarySnapshot(config, d); err != nil {
		return err
	}

	s := &autoscalingv1.Scale{
		TypeMeta: metav1.TypeMeta{APIVersion: "autoscaling/v1", Kind: "Scale"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       d.Namespace,
			Name:            d.Name,
			UID:             d.UID,
			ResourceVersion: d.ResourceVersion,
		},
		Spec: autoscalingv1.ScaleSpec{Replicas: replicas},
	}
	_, err = deployments.UpdateScale(ctx, config.Name, s, metav1.UpdateOptions{})
	return err
}

// Each distinct primary mutation checks its own nearest full-object snapshot.
// A prior successful replica write never authorizes a later resource patch.
func checkMaintenancePrimarySnapshot(config ParsedName, d *appsv1.Deployment) error {
	if d == nil {
		return fmt.Errorf("maintenance primary Deployment snapshot is missing")
	}
	if _, marked := d.Annotations[maintenanceFenceAnnotation]; marked {
		return apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "deployments"}, config.Name,
			fmt.Errorf("maintenance operation conflict: reserved fence annotation is present"))
	}
	if d.Namespace != config.Namespace || d.Name != config.Name || d.UID == "" || d.ResourceVersion == "" {
		return fmt.Errorf("maintenance primary Deployment snapshot has incomplete or mismatched identity")
	}

	return nil
}
