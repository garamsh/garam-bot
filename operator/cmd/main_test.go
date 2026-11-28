package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	botv1 "github.com/garamsh/garam-bot/api/declaration/v1alpha1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"
)

// TestServedKinds_FindsTheReconciledKindsTheAPIServerServes runs discovery against an API server that
// first serves interface 1's group without the Bot, then the Bot too, as the connector chart installs
// them, and then Agent too.
func TestServedKinds_FindsTheReconciledKindsTheAPIServerServes(t *testing.T) {
	var bot *apiextensionsv1.CustomResourceDefinition
	var others []*apiextensionsv1.CustomResourceDefinition
	for _, definition := range interfaceOneDefinitions(t) {
		if definition.Spec.Names.Kind == "Bot" {
			bot = definition
		} else {
			others = append(others, definition)
		}
	}
	if bot == nil || len(others) == 0 {
		t.Fatal("interface 1 defines no Bot, or nothing beside it")
	}
	env := &envtest.Environment{CRDs: others}
	if dir := envtestBinaryDir(); dir != "" {
		env.BinaryAssetsDirectory = dir
	}
	config, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })

	found, err := servedKinds(config)
	if err != nil {
		t.Fatal(err)
	}
	if found != (served{}) {
		t.Fatalf("with interface 1's group served without the Bot, found %+v, want neither", found)
	}

	install(t, config, envtest.CRDInstallOptions{CRDs: []*apiextensionsv1.CustomResourceDefinition{bot}})
	discoverEventually(t, config, served{bot: true}, "with the Bot installed")

	install(t, config, envtest.CRDInstallOptions{Paths: []string{filepath.Join("..", "config", "crd", "bases")}})
	discoverEventually(t, config, served{agent: true, bot: true}, "with the Agent installed too")
}

func install(t *testing.T, config *rest.Config, options envtest.CRDInstallOptions) {
	t.Helper()
	options.ErrorIfPathMissing = true
	if _, err := envtest.InstallCRDs(config, options); err != nil {
		t.Fatalf("install CRDs: %v", err)
	}
}

// discoverEventually waits for discovery to find want, as the API server serves a new kind once its
// definition is established.
func discoverEventually(t *testing.T, config *rest.Config, want served, when string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		found, err := servedKinds(config)
		if err != nil {
			t.Fatal(err)
		}
		if found == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s, found %+v, want %+v", when, found, want)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func interfaceOneDefinitions(t *testing.T) []*apiextensionsv1.CustomResourceDefinition {
	t.Helper()
	var out []*apiextensionsv1.CustomResourceDefinition
	err := fs.WalkDir(botv1.CRDs, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		read, err := fs.ReadFile(botv1.CRDs, path)
		if err != nil {
			return err
		}
		definition := &apiextensionsv1.CustomResourceDefinition{}
		if err := yaml.Unmarshal(read, definition); err != nil {
			return err
		}
		out = append(out, definition)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// envtestBinaryDir finds the binaries `make setup-envtest` downloads, so the test runs without
// KUBEBUILDER_ASSETS set.
func envtestBinaryDir() string {
	base := filepath.Join("..", "bin", "k8s")
	entries, err := os.ReadDir(base)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(base, entry.Name())
		}
	}
	return ""
}
