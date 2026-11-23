package conformance_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"

	"github.com/garamsh/garam-bot/api/declaration/v1alpha1/conformance"
	"github.com/garamsh/garam-bot/api/declaration/v1alpha1/manifest"
)

// fileForm admits a manifest as garam-bot reads one from a file.
type fileForm struct{}

func (fileForm) Admit(_ context.Context, _ string, data []byte) ([]byte, error) {
	object, err := manifest.Parse(data)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	var fields struct {
		Spec json.RawMessage `json:"spec"`
	}
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return nil, err
	}

	return fields.Spec, nil
}

func TestFileForm(t *testing.T) {
	conformance.Run(t, fileForm{})
}

// apiServer admits a manifest by a dry-run create under strict field validation, against an API
// server serving the CustomResourceDefinitions.
type apiServer struct {
	client client.Client
}

func (a apiServer) Admit(ctx context.Context, namespace string, data []byte) ([]byte, error) {
	object := &unstructured.Unstructured{}
	if err := yaml.Unmarshal(data, &object.Object); err != nil {
		return nil, err
	}
	object.SetNamespace(namespace)
	if err := a.client.Create(ctx, object, client.DryRunAll, client.FieldValidation(metav1.FieldValidationStrict)); err != nil {
		return nil, err
	}
	spec, found, err := unstructured.NestedFieldNoCopy(object.Object, "spec")
	if err != nil || !found {
		return nil, fmt.Errorf("the admitted object has no spec: %v", err)
	}

	return json.Marshal(spec)
}

// TestAPIServer needs KUBEBUILDER_ASSETS, the API server and etcd binaries; `make test` sets it.
func TestAPIServer(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset: no API server to run the suite against")
	}
	environment := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "crds")},
		ErrorIfCRDPathMissing: true,
	}
	config, err := environment.Start()
	if err != nil {
		t.Fatalf("start the API server: %v", err)
	}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Errorf("stop the API server: %v", err)
		}
	})
	c, err := client.New(config, client.Options{})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: conformance.Namespace}}
	if err := c.Create(t.Context(), namespace); err != nil {
		t.Fatalf("create namespace %s: %v", conformance.Namespace, err)
	}

	conformance.Run(t, apiServer{client: c})
}
