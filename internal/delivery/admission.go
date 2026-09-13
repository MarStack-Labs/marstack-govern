package delivery

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type DryRun struct {
	Client client.Client
}

func (d *DryRun) Admits(ctx context.Context, namespace, manifest string) (bool, []Finding, error) {
	object := &unstructured.Unstructured{}
	if err := yaml.Unmarshal([]byte(manifest), &object.Object); err != nil {
		return false, []Finding{{
			Severity: "block",
			Check:    "parse",
			Message:  fmt.Sprintf("the rendered manifest is not valid yaml: %v", err),
		}}, nil
	}

	object.SetNamespace(object.GetNamespace())

	if _, err := d.Client.RESTMapper().RESTMapping(
		object.GroupVersionKind().GroupKind(), object.GroupVersionKind().Version,
	); err != nil {
		if meta.IsNoMatchError(err) {
			return false, []Finding{{
				Severity: "block",
				Check:    "kind",
				Message: fmt.Sprintf("the cluster has no %s; install its controller before scaffolding onto it",
					object.GetKind()),
			}}, nil
		}

		return false, nil, fmt.Errorf("resolve %s: %w", object.GetKind(), err)
	}

	err := d.Client.Create(ctx, object, client.DryRunAll,
		client.FieldOwner("margov-scaffold"))
	if err != nil {
		return false, []Finding{{
			Severity: "block",
			Check:    "admission",
			Message:  err.Error(),
		}}, nil
	}

	return true, nil, nil
}
