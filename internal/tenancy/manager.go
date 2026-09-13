package tenancy

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	governv1alpha1 "github.com/marstack-labs/marstack-govern/api/v1alpha1"
)

func NewScheme() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()

	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("register kubernetes types: %w", err)
	}
	if err := governv1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("register govern types: %w", err)
	}

	return scheme, nil
}
