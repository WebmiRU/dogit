package api

import (
	"strings"
	"testing"
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
