package kubernetes

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel/trace"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func maintenancePrimaryFixture() (ParsedName, *appsv1.Deployment) {
	config := ParsedName{Kind: "deployment", Namespace: "gitea-gitops-actions", Name: "gitops-validation-runner"}
	replicas := int32(1)
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Namespace: config.Namespace, Name: config.Name,
		UID: "original-deployment-uid", ResourceVersion: "opaque:before/A",
	}, Spec: appsv1.DeploymentSpec{Replicas: &replicas}}
	return config, d
}

func maintenanceTestProvider(client *fake.Clientset) *Provider {
	return &Provider{Client: client, delimiter: "_", l: slog.Default(), tracer: trace.NewNoopTracerProvider().Tracer("maintenance-fence-test")}
}

func requireOnlyDeploymentGet(t *testing.T, client *fake.Clientset) {
	t.Helper()
	if len(client.Actions()) != 1 || client.Actions()[0].GetVerb() != "get" || client.Actions()[0].GetSubresource() != "" {
		t.Fatalf("expected exactly one full Deployment GET, got %#v", client.Actions())
	}
}

func TestMaintenanceFenceMarkedPrimaryRejectsEveryScale(t *testing.T) {
	for _, marker := range []string{"", "malformed", "{", `{"deadline":0}`, `{"unknown":"marker"}`} {
		for _, replicas := range []int32{0, 1, 3} {
			t.Run(marker+"/"+string(rune('0'+replicas)), func(t *testing.T) {
				config, d := maintenancePrimaryFixture()
				d.Annotations = map[string]string{maintenanceFenceAnnotation: marker}
				client := fake.NewSimpleClientset(d)
				err := maintenanceTestProvider(client).scale(context.Background(), config, replicas)
				if !apierrors.IsConflict(err) {
					t.Fatalf("marker presence must be operation conflict, got %v", err)
				}
				requireOnlyDeploymentGet(t, client)
			})
		}
	}
}

func TestMaintenanceFenceUnmarkedPrimaryCarriesSameSnapshot(t *testing.T) {
	for _, replicas := range []int32{0, 1, 3} {
		config, d := maintenancePrimaryFixture()
		client := fake.NewSimpleClientset(d)
		updates := 0
		client.PrependReactor("update", "deployments", func(a clienttesting.Action) (bool, runtime.Object, error) {
			if a.GetSubresource() != "scale" {
				t.Fatal("ordinary primary scale must not update full Deployment")
			}
			s := a.(clienttesting.UpdateAction).GetObject().(*autoscalingv1.Scale)
			if s.UID != d.UID || s.ResourceVersion != d.ResourceVersion || s.Name != d.Name || s.Namespace != d.Namespace || s.Spec.Replicas != replicas {
				t.Fatalf("scale lost exact snapshot identity or replicas: %#v", s)
			}
			updates++
			return true, s.DeepCopy(), nil
		})
		if err := maintenanceTestProvider(client).scale(context.Background(), config, replicas); err != nil {
			t.Fatal(err)
		}
		if updates != 1 || len(client.Actions()) != 2 || client.Actions()[0].GetSubresource() != "" {
			t.Fatalf("expected one Deployment GET then one scale update; got %#v", client.Actions())
		}
	}
}

func TestMaintenanceFenceDelayedScaleConflictsWithoutRefresh(t *testing.T) {
	// Fake reactors model the required server precondition, not proof that an
	// actual API server couples Deployment and scale resourceVersions/UIDs.
	for _, change := range []string{"marker-cas", "replacement-uid"} {
		t.Run(change, func(t *testing.T) {
			config, original := maintenancePrimaryFixture()
			current := original.DeepCopy()
			client := fake.NewSimpleClientset()
			gets, updates := 0, 0
			client.PrependReactor("get", "deployments", func(a clienttesting.Action) (bool, runtime.Object, error) {
				if a.GetSubresource() != "" {
					t.Fatal("must not refresh through GetScale")
				}
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
			client.PrependReactor("update", "deployments", func(a clienttesting.Action) (bool, runtime.Object, error) {
				if a.GetSubresource() != "scale" {
					t.Fatal("unexpected full Deployment update")
				}
				updates++
				s := a.(clienttesting.UpdateAction).GetObject().(*autoscalingv1.Scale)
				if s.UID != original.UID || s.ResourceVersion != original.ResourceVersion {
					t.Fatal("delayed request must retain original UID and opaque RV")
				}
				if s.UID != current.UID || s.ResourceVersion != current.ResourceVersion {
					return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "deployments"}, config.Name, errors.New("simulated precondition conflict"))
				}
				t.Fatal("stale request unexpectedly matched the modeled server")
				return true, nil, nil
			})
			err := maintenanceTestProvider(client).scale(context.Background(), config, 0)
			if !apierrors.IsConflict(err) || gets != 1 || updates != 1 || *current.Spec.Replicas != 1 {
				t.Fatalf("stale request retried or lost conflict: err=%v gets=%d updates=%d", err, gets, updates)
			}
		})
	}
}

func TestMaintenanceFenceReadFailureOrInvalidIdentityNeverWrites(t *testing.T) {
	for _, invalid := range []string{"get-error", "missing-uid", "missing-rv", "wrong-name", "wrong-namespace"} {
		t.Run(invalid, func(t *testing.T) {
			config, d := maintenancePrimaryFixture()
			client := fake.NewSimpleClientset()
			client.PrependReactor("get", "deployments", func(a clienttesting.Action) (bool, runtime.Object, error) {
				if invalid == "get-error" {
					return true, nil, errors.New("unavailable")
				}
				switch invalid {
				case "missing-uid":
					d.UID = ""
				case "missing-rv":
					d.ResourceVersion = ""
				case "wrong-name":
					d.Name = "other"
				case "wrong-namespace":
					d.Namespace = "other"
				}
				return true, d, nil
			})
			if err := maintenanceTestProvider(client).scale(context.Background(), config, 1); err == nil {
				t.Fatal("invalid identity/read must fail closed")
			}
			requireOnlyDeploymentGet(t, client)
		})
	}
}

func TestMaintenanceFenceUpdateErrorIsNotRetried(t *testing.T) {
	config, d := maintenancePrimaryFixture()
	client := fake.NewSimpleClientset(d)
	want := errors.New("update outcome unavailable")
	client.PrependReactor("update", "deployments", func(a clienttesting.Action) (bool, runtime.Object, error) { return true, nil, want })
	if got := maintenanceTestProvider(client).scale(context.Background(), config, 1); !errors.Is(got, want) || len(client.Actions()) != 2 {
		t.Fatalf("unexpected retry or error replacement: %v, %#v", got, client.Actions())
	}
}

func TestMaintenanceFenceOtherTargetsKeepGetScalePath(t *testing.T) {
	for _, config := range []ParsedName{
		{Kind: "deployment", Namespace: "other", Name: "gitops-validation-runner"},
		{Kind: "deployment", Namespace: "gitea-gitops-actions", Name: "other"},
		{Kind: "statefulset", Namespace: "gitea-gitops-actions", Name: "gitops-validation-runner"},
	} {
		client := fake.NewSimpleClientset()
		before := &autoscalingv1.Scale{ObjectMeta: metav1.ObjectMeta{Name: config.Name, Namespace: config.Namespace, ResourceVersion: "legacy:scale/R", Annotations: map[string]string{maintenanceFenceAnnotation: "irrelevant-other-target"}}}
		client.PrependReactor("get", "*", func(a clienttesting.Action) (bool, runtime.Object, error) {
			if a.GetSubresource() != "scale" {
				t.Fatal("other targets must retain existing GetScale behavior")
			}
			return true, before.DeepCopy(), nil
		})
		client.PrependReactor("update", "*", func(a clienttesting.Action) (bool, runtime.Object, error) {
			s := a.(clienttesting.UpdateAction).GetObject().(*autoscalingv1.Scale)
			if a.GetSubresource() != "scale" || s.ResourceVersion != before.ResourceVersion || s.Annotations[maintenanceFenceAnnotation] != before.Annotations[maintenanceFenceAnnotation] || s.Spec.Replicas != 2 {
				t.Fatalf("other target behavior changed: %#v", s)
			}
			return true, s.DeepCopy(), nil
		})
		if err := maintenanceTestProvider(client).scale(context.Background(), config, 2); err != nil || len(client.Actions()) != 2 {
			t.Fatalf("other target scale failed: %v", err)
		}
	}
}

func TestMaintenanceFencePublicStartStopPathsRejectMarker(t *testing.T) {
	for _, mode := range []string{"ordinary", "resource-mode"} {
		for _, start := range []bool{false, true} {
			config, d := maintenancePrimaryFixture()
			d.Annotations = map[string]string{maintenanceFenceAnnotation: "expired-or-malformed"}
			if mode == "resource-mode" {
				d.Labels = map[string]string{"sablier.active.cpu": "4", "sablier.active.memory": "6Gi", "sablier.idle.replicas": "1", "sablier.idle.cpu": "100m"}
			}
			client := fake.NewSimpleClientset(d)
			p := maintenanceTestProvider(client)
			name := "deployment_" + config.Namespace + "_" + config.Name + "_1"
			var err error
			if start {
				err = p.InstanceStart(context.Background(), name)
			} else {
				err = p.InstanceStop(context.Background(), name)
			}
			if !apierrors.IsConflict(err) {
				t.Fatalf("public scale path missed marker: mode=%s start=%v err=%v", mode, start, err)
			}
			for _, a := range client.Actions() {
				if a.GetVerb() != "get" || a.GetSubresource() != "" {
					t.Fatalf("marked public path must not write or GetScale: %#v", client.Actions())
				}
			}
		}
	}
}
