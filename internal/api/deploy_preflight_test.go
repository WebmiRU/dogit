package api

import (
	"strings"
	"testing"

	"github.com/ewolf/dogit/internal/pipeline"
)

func TestValidateBuildImageVariables(t *testing.T) {
	available := map[string]struct{}{
		"BUILD_WORKER_IMAGE_URL":  {},
		"BUILD_WEB_API_IMAGE_URL": {},
	}
	if err := validateBuildImageVariables("image: ${BUILD_WORKER_IMAGE_URL}\n", available); err != nil {
		t.Fatalf("known image variable rejected: %v", err)
	}
	for _, body := range []string{
		"image: ${BUILD_UNKNOWN_IMAGE_URL}\n",
		"image: ${BUILD_WORKER_IMAGE_URL\n",
	} {
		if err := validateBuildImageVariables(body, available); err == nil {
			t.Errorf("invalid variable reference was accepted: %q", body)
		}
	}
}

func TestSubstituteBuildImageVariablesDoesNotEvaluateOtherVariables(t *testing.T) {
	body := "image: ${BUILD_WORKER_IMAGE_URL}\nvalue: ${CI_COMMIT_SHA}\n"
	got, err := substituteBuildImageVariables(body, map[string]string{
		"BUILD_WORKER_IMAGE_URL": "registry.example/app@sha256:abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "image: registry.example/app@sha256:abc") ||
		!strings.Contains(got, "value: ${CI_COMMIT_SHA}") {
		t.Fatalf("unexpected substitution: %q", got)
	}
}

func TestNamedImageSubstitutionRejectsLegacyPlaceholder(t *testing.T) {
	if !hasLegacyImagePlaceholder("containers:\n  - name: api\n    image: IMAGE\n") {
		t.Fatal("legacy image placeholder was not found")
	}
	if hasLegacyImagePlaceholder("image: ${BUILD_API_IMAGE_URL}\n") {
		t.Fatal("named image variable was classified as legacy placeholder")
	}
}


func TestValidateManifestYAMLRejectsInvalidSyntaxAndEmptyFiles(t *testing.T) {
	for _, body := range []string{
		"apiVersion: apps/v1\nkind: [Deployment\n",
		"",
		"# comments only\n",
	} {
		if err := validateManifestYAML("k8s/deployment.yaml", body); err == nil {
			t.Errorf("invalid manifest YAML was accepted: %q", body)
		}
	}
	if err := validateManifestYAML("k8s/deployment.yaml", "apiVersion: apps/v1\nkind: Deployment\n---\napiVersion: v1\nkind: Service\n"); err != nil {
		t.Fatalf("multi-document YAML rejected: %v", err)
	}
}

func TestValidateNamedBuildOutputChecksCommitPaths(t *testing.T) {
	files := map[string]struct{}{
		"Dockerfile": {},
		"services/worker/Dockerfile": {},
		"services/worker/main.go": {},
	}
	good := pipeline.BuildOutputSpec{Name: "worker", Build: map[string]any{
		"image": "registry.example/myapp/worker", "context": "services/worker", "dockerfile": "Dockerfile", "tag": "sha",
	}}
	if err := validateNamedBuildOutput(good, files); err != nil {
		t.Fatalf("valid build output rejected: %v", err)
	}
	bad := pipeline.BuildOutputSpec{Name: "worker", Build: map[string]any{
		"image": "registry.example/myapp/worker", "context": "missing", "dockerfile": "Dockerfile",
	}}
	if err := validateNamedBuildOutput(bad, files); err == nil {
		t.Fatal("missing build context was accepted")
	}
}


func TestValidateNamedBuildOutputRegistries(t *testing.T) {
	sameDefaultRegistry := []pipeline.BuildOutputSpec{
		{Name: "api", Build: map[string]any{"image": "team/api"}},
		{Name: "worker", Build: map[string]any{"image": "team/worker"}},
	}
	if err := validateNamedBuildOutputRegistries(sameDefaultRegistry); err != nil {
		t.Fatalf("same default registry rejected: %v", err)
	}

	sameNamedRegistry := []pipeline.BuildOutputSpec{
		{Name: "api", Build: map[string]any{"image": "registry.example/team/api"}},
		{Name: "worker", Build: map[string]any{"image": "registry.example/team/worker"}},
	}
	if err := validateNamedBuildOutputRegistries(sameNamedRegistry); err != nil {
		t.Fatalf("same named registry rejected: %v", err)
	}

	differentRegistries := []pipeline.BuildOutputSpec{
		{Name: "api", Build: map[string]any{"image": "ghcr.io/team/api"}},
		{Name: "worker", Build: map[string]any{"image": "registry.example/team/worker"}},
	}
	if err := validateNamedBuildOutputRegistries(differentRegistries); err == nil {
		t.Fatal("different registries were accepted")
	}
}
