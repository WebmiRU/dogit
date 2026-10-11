package api

import (
	"context"
	"fmt"
	"strings"

	"github.com/ewolf/dogit/internal/pipeline"
)

// validateBuildImageReferences runs before pipeline jobs are created. It checks deployment
// files from the run's own commit, when the expected output names are already knowable even
// though the resulting image addresses are not.
func (s *Server) validateBuildImageReferences(ctx context.Context, repoDir, sha string, config *pipeline.Config) error {
	outputs := config.NamedBuildOutputs()
	if len(outputs) == 0 {
		return nil
	}

	available := make(map[string]struct{}, len(outputs))
	for _, output := range outputs {
		available[pipeline.ImageVariableName(output.Name)] = struct{}{}
	}
	check := func(path, body string) error {
		if err := validateBuildImageVariables(body, available); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if hasLegacyImagePlaceholder(body) {
			return fmt.Errorf("%s still uses the legacy IMAGE placeholder; use a named ${BUILD_<NAME>_IMAGE_URL} variable", path)
		}
		return nil
	}

	for _, spec := range config.Deploys {
		for _, path := range spec.Manifests {
			body, _, _, _, err := s.readManifest(ctx, repoDir, sha, path)
			if err != nil {
				return err
			}
			if err := check(path, body); err != nil {
				return err
			}
		}
		for _, step := range append(append([]pipeline.DeployStep{}, spec.Pre...), spec.Post...) {
			if path := strings.TrimSpace(step.Manifest); path != "" {
				body, _, _, _, err := s.readManifest(ctx, repoDir, sha, path)
				if err != nil {
					return err
				}
				if err := check(path, body); err != nil {
					return err
				}
			}
			if step.Image != "" {
				if err := validateBuildImageVariables(step.Image, available); err != nil {
					return fmt.Errorf("deploy step image %q: %w", step.Image, err)
				}
			}
		}
	}
	return nil
}

// validateBuildImageVariables checks Dogit's generated image-variable namespace only.
// Other project variables belong to their own layer; a typo in BUILD_*_IMAGE_URL is
// knowably wrong before any runner starts.
func validateBuildImageVariables(text string, available map[string]struct{}) error {
	remaining := text
	for {
		start := strings.Index(remaining, "${BUILD_")
		if start < 0 {
			return nil
		}
		after := remaining[start+2:]
		end := strings.IndexByte(after, '}')
		if end < 0 {
			return fmt.Errorf("unterminated generated image variable near %q", after)
		}
		name := after[:end]
		if strings.HasSuffix(name, "_IMAGE_URL") {
			if _, ok := available[name]; !ok {
				return fmt.Errorf("unknown generated image variable ${%s}", name)
			}
		}
		remaining = after[end+1:]
	}
}

// hasLegacyImagePlaceholder avoids silently applying a named-output deployment with the old
// single-image token still present in an image field.
func hasLegacyImagePlaceholder(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "image:") {
			continue
		}
		value := strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "image:")), "\"'")
		if value == "IMAGE" {
			return true
		}
	}
	return false
}

// substituteBuildImageVariables expands only the generated image variables. It does not run
// a shell and does not remove unrelated project variables from the manifest.
func substituteBuildImageVariables(text string, values map[string]string) (string, error) {
	for name, value := range values {
		text = strings.ReplaceAll(text, "${"+name+"}", value)
	}
	available := make(map[string]struct{}, len(values))
	for name := range values {
		available[name] = struct{}{}
	}
	if err := validateBuildImageVariables(text, available); err != nil {
		return "", err
	}
	if hasLegacyImagePlaceholder(text) {
		return "", fmt.Errorf("legacy IMAGE placeholder remains in a named-output deployment")
	}
	return text, nil
}

func substituteBuildImageVariablesInRequest(request *deployRequest, values map[string]string) error {
	for index := range request.Manifests {
		body, err := substituteBuildImageVariables(request.Manifests[index].Body, values)
		if err != nil {
			return fmt.Errorf("%s: %w", request.Manifests[index].Path, err)
		}
		request.Manifests[index].Body = body
	}
	for _, steps := range []*[]deployStep{&request.Pre, &request.Post} {
		for index := range *steps {
			step := &(*steps)[index]
			if step.Body != "" {
				body, err := substituteBuildImageVariables(step.Body, values)
				if err != nil {
					return fmt.Errorf("%s: %w", step.Name, err)
				}
				step.Body = body
			}
			if step.Image != "" {
				image, err := substituteBuildImageVariables(step.Image, values)
				if err != nil {
					return fmt.Errorf("deploy step image: %w", err)
				}
				step.Image = image
			}
		}
	}
	return nil
}
