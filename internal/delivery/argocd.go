package delivery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var ErrArgoAbsent = errors.New("argo cd is not installed, so there is no desired state to compare against")

var ApplicationGVK = schema.GroupVersionKind{
	Group: "argoproj.io", Version: "v1alpha1", Kind: "Application",
}

type Application struct {
	Name           string
	Namespace      string
	RepoURL        string
	Path           string
	TargetRevision string
	SyncedRevision string
	SyncStatus     string
	HealthStatus   string
	Resources      []Resource
	LastSyncAt     time.Time
	DestNamespace  string
}

type Resource struct {
	Group     string
	Kind      string
	Namespace string
	Name      string
	Status    string
	Health    string
}

func (r Resource) Drifted() bool {
	return r.Status != "" && r.Status != "Synced"
}

type Argo struct {
	client client.Client
}

func NewArgo(c client.Client) *Argo {
	return &Argo{client: c}
}

func (a *Argo) Available() bool {
	mapper := a.client.RESTMapper()
	if mapper == nil {
		return true
	}

	_, err := mapper.RESTMapping(ApplicationGVK.GroupKind(), ApplicationGVK.Version)

	return err == nil
}

func (a *Argo) ForWorkload(ctx context.Context, namespace, kind, name string) (Application, bool, error) {
	if !a.Available() {
		return Application{}, false, ErrArgoAbsent
	}

	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{
		Group: ApplicationGVK.Group, Version: ApplicationGVK.Version, Kind: ApplicationGVK.Kind + "List",
	})

	if err := a.client.List(ctx, list); err != nil {
		return Application{}, false, fmt.Errorf("list argo applications: %w", err)
	}

	for i := range list.Items {
		application := applicationFrom(&list.Items[i])

		for _, resource := range application.Resources {
			if resource.Kind == kind && resource.Name == name && matchesNamespace(resource, application, namespace) {
				return application, true, nil
			}
		}
	}

	return Application{}, false, nil
}

func applicationFrom(object *unstructured.Unstructured) Application {
	application := Application{
		Name:      object.GetName(),
		Namespace: object.GetNamespace(),
	}

	application.RepoURL, _, _ = unstructured.NestedString(object.Object, "spec", "source", "repoURL")
	application.Path, _, _ = unstructured.NestedString(object.Object, "spec", "source", "path")
	application.TargetRevision, _, _ = unstructured.NestedString(object.Object, "spec", "source", "targetRevision")
	application.DestNamespace, _, _ = unstructured.NestedString(object.Object, "spec", "destination", "namespace")
	application.SyncStatus, _, _ = unstructured.NestedString(object.Object, "status", "sync", "status")
	application.SyncedRevision, _, _ = unstructured.NestedString(object.Object, "status", "sync", "revision")
	application.HealthStatus, _, _ = unstructured.NestedString(object.Object, "status", "health", "status")

	if stamp, found, _ := unstructured.NestedString(object.Object, "status", "operationState", "finishedAt"); found {
		if parsed, err := time.Parse(time.RFC3339, stamp); err == nil {
			application.LastSyncAt = parsed
		}
	}

	resources, found, _ := unstructured.NestedSlice(object.Object, "status", "resources")
	if !found {
		return application
	}

	for _, item := range resources {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}

		resource := Resource{
			Group:     text(entry, "group"),
			Kind:      text(entry, "kind"),
			Namespace: text(entry, "namespace"),
			Name:      text(entry, "name"),
			Status:    text(entry, "status"),
		}

		if health, ok := entry["health"].(map[string]any); ok {
			resource.Health = text(health, "status")
		}

		application.Resources = append(application.Resources, resource)
	}

	return application
}

func matchesNamespace(resource Resource, application Application, namespace string) bool {
	if resource.Namespace != "" {
		return resource.Namespace == namespace
	}

	return application.DestNamespace == namespace
}

func text(source map[string]any, key string) string {
	value, _ := source[key].(string)

	return value
}
