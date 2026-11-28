//go:build chart

package chart

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"testing"

	botv1 "github.com/garamsh/garam-bot/api/declaration/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestChart_ShipsInterfaceOnesCRDs(t *testing.T) {
	want := map[string][]byte{}
	if err := fs.WalkDir(botv1.CRDs, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := fs.ReadFile(botv1.CRDs, name)
		want[path.Base(name)] = data
		return err
	}); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(chartDir, "crds", "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(want) {
		t.Errorf("crds/ holds %d files, want interface 1's %d", len(files), len(want))
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, want[filepath.Base(file)]) {
			t.Errorf("crds/%s is not interface 1's; run `make chart-crds`", filepath.Base(file))
		}
	}
}

func TestChart_Install(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Connector chart install")
}

// The chart installed into an envtest API server with no CRDs, whose namespaces enforce PodSecurity
// restricted: once with --skip-crds, then a second release watching a namespace the first claimed,
// then a release claiming a namespace of its own.
var _ = Describe("The connector chart installed into an API server", Ordered, func() {
	var (
		ctx        = context.Background()
		k8sClient  client.Client
		kubeconfig string
	)

	install := func(name, namespace string, args ...string) (string, error) {
		cmd := exec.Command(helm, append([]string{
			"install", name, chartDir, "--namespace", namespace, "--values", fullValues, "--kubeconfig", kubeconfig,
		}, args...)...)
		cmd.Env = append(os.Environ(), "HELM_CACHE_HOME="+GinkgoT().TempDir(),
			"HELM_CONFIG_HOME="+GinkgoT().TempDir(), "HELM_DATA_HOME="+GinkgoT().TempDir())
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	crdInstalled := func() bool {
		err := k8sClient.Get(ctx, client.ObjectKey{Name: "bots.bot.garam.sh"}, &apiextensionsv1.CustomResourceDefinition{})
		if !apierrors.IsNotFound(err) {
			Expect(err).NotTo(HaveOccurred())
		}
		return err == nil
	}

	BeforeAll(func() {
		environment := &envtest.Environment{}
		config, err := environment.Start()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(environment.Stop)

		scheme := runtime.NewScheme()
		Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
		Expect(apiextensionsv1.AddToScheme(scheme)).To(Succeed())
		k8sClient, err = client.New(config, client.Options{Scheme: scheme})
		Expect(err).NotTo(HaveOccurred())

		user, err := environment.AddUser(envtest.User{Name: "chart-check", Groups: []string{"system:masters"}}, nil)
		Expect(err).NotTo(HaveOccurred())
		data, err := user.KubeConfig()
		Expect(err).NotTo(HaveOccurred())
		kubeconfig = filepath.Join(GinkgoT().TempDir(), "kubeconfig")
		Expect(os.WriteFile(kubeconfig, data, 0o600)).To(Succeed())

		namespaces := []string{releaseNamespace, "second-system", "third-system", "third-team", watched[0], watched[1]}
		for _, name := range namespaces {
			Expect(k8sClient.Create(ctx, &corev1.Namespace{
				Name:   name,
				Labels: map[string]string{"pod-security.kubernetes.io/enforce": "restricted"}})).To(Succeed())
		}
	})

	It("installs with --skip-crds and leaves interface 1's CRDs out", func() {
		out, err := install(releaseName, releaseNamespace, "--skip-crds")
		Expect(err).NotTo(HaveOccurred(), out)
		Expect(crdInstalled()).To(BeFalse())
	})

	It("refuses a second release watching a namespace the first claimed, and keeps the first's claim", func() {
		out, err := install("second", "second-system", "--set-json", `watchNamespaces=["`+watched[1]+`"]`)
		Expect(err).To(HaveOccurred())
		Expect(out).To(ContainSubstring(`Lease "%s" in namespace "%s" exists and cannot be imported`, claimName, watched[1]))

		var claim coordinationv1.Lease
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: watched[1], Name: claimName}, &claim)).To(Succeed())
		Expect(claim.Spec.HolderIdentity).To(Equal(ptr.To(releaseNamespace + "/" + releaseName)))
	})

	It("installs a release claiming a namespace of its own, with interface 1's CRDs", func() {
		out, err := install("third", "third-system", "--set-json", `watchNamespaces=["third-team"]`)
		Expect(err).NotTo(HaveOccurred(), out)
		Expect(crdInstalled()).To(BeTrue())
	})

	It("runs each program's Pod as a namespace enforcing PodSecurity restricted admits it", func() {
		var deployments appsv1.DeploymentList
		Expect(k8sClient.List(ctx, &deployments, client.InNamespace(releaseNamespace))).To(Succeed())
		Expect(deployments.Items).To(HaveLen(2))
		for _, deployment := range deployments.Items {
			pod := &corev1.Pod{
				Name: deployment.Name, Namespace: releaseNamespace,
				Spec: deployment.Spec.Template.Spec,
			}
			Expect(k8sClient.Create(ctx, pod, client.DryRunAll)).To(Succeed(), deployment.Name)

			// The control: the same Pod without its containers' security contexts is refused, so the
			// namespace enforces restricted.
			control := pod.DeepCopy()
			for i := range control.Spec.Containers {
				control.Spec.Containers[i].SecurityContext = nil
			}
			Expect(k8sClient.Create(ctx, control, client.DryRunAll)).To(
				MatchError(ContainSubstring(`violates PodSecurity "restricted`)), deployment.Name)
		}
	})
})
