package api

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/ewolf/dogit/internal/pipeline"
)

// validateBuildImageReferences runs before pipeline jobs are created. It checks deployment
// files from the run's own commit, when the expected output names are already knowable even
// though the resulting image addresses are not.
func (s *Server) validateBuildImageReferences(ctx context.Context, repoDir, sha string, config *pipeline.Config) error {
	outputs := config.NamedBuildOutputs()
	available := make(map[string]struct{}, len(outputs))
	for _, output := range outputs {
		available[pipeline.ImageVariableName(output.Name)] = struct{}{}
	}

	// Check build paths against this commit's tree, not a moving branch or working directory.
	if len(outputs) > 0 {
		entries, err := s.git.ListTree(ctx, repoDir, sha, "", true)
		if err != nil {
			return fmt.Errorf("list files at %s: %w", shortSHA(sha), err)
		}
		files := make(map[string]struct{}, len(entries))
		for _, entry := range entries {
			if entry.Type == "blob" {
				files[entry.Path] = struct{}{}
			}
		}
		for _, output := range outputs {
			if err := validateNamedBuildOutput(output, files); err != nil {
				return err
			}
		}
		// The current Kubernetes pull-secret contract can describe one registry per
		// deployment. Reject a known mismatch here, before any runner spends time building.
		for _, spec := range config.Deploys {
			if !isKubernetesDeployModule(spec.Module) {
				continue
			}
			if err := validateNamedBuildOutputRegistries(outputs); err != nil {
				return err
			}
		}
	}
	check := func(file, body string) error {
		if err := validateManifestYAML(file, body); err != nil {
			return err
		}
		if len(outputs) == 0 {
			return nil
		}
		if err := validateBuildImageVariables(body, available); err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
		if hasLegacyImagePlaceholder(body) {
			return fmt.Errorf("%s still uses the legacy IMAGE placeholder; use a named ${BUILD_<NAME>_IMAGE_URL} variable", file)
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
			if step.Image != "" && len(outputs) > 0 {
				if err := validateBuildImageVariables(step.Image, available); err != nil {
					return fmt.Errorf("deploy step image %q: %w", step.Image, err)
				}
			}
		}
	}
	return nil
}

func isKubernetesDeployModule(module string) bool {
	module = strings.TrimSpace(strings.TrimPrefix(module, "deploy:"))
	return module == "kubernetes"
}

// validateNamedBuildOutputRegistries checks literal registry hosts. Images that use a
// variable in their host cannot be resolved at config-read time and are checked again
// at deployment time. Empty host means the default Docker registry, not the output name.
func validateNamedBuildOutputRegistries(outputs []pipeline.BuildOutputSpec) error {
	firstName := ""
	firstHost := ""
	for _, output := range outputs {
		image, ok := output.Build["image"].(string)
		if !ok || strings.ContainsAny(image, "$") {
			continue
		}
		host := imageHost(strings.TrimSpace(image))
		if firstName == "" {
			firstName, firstHost = output.Name, host
			continue
		}
		if host != firstHost {
			return fmt.Errorf("named build outputs for a single Kubernetes deployment must use the same registry; %q and %q point to different registries", firstName, output.Name)
		}
	}
	return nil
}

// validateManifestYAML checks every YAML document before a pipeline runner starts.
// A live cluster is still needed for schema and admission validation, but syntax can be checked here.
func validateManifestYAML(file, body string) error {
	decoder := yaml.NewDecoder(strings.NewReader(body))
	documents := 0
	for {
		var document yaml.Node
		err := decoder.Decode(&document)
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("%s is not valid YAML: %w", file, err)
		}
		if len(document.Content) == 0 {
			continue
		}
		documents++
	}
	if documents == 0 {
		return fmt.Errorf("%s contains no YAML document", file)
	}
	return nil
}

// validateNamedBuildOutput checks required build fields and confirms that its Dockerfile and
// context directory exist in the repository commit the pipeline is running against.
func validateNamedBuildOutput(output pipeline.BuildOutputSpec, files map[string]struct{}) error {
	image, ok := output.Build["image"].(string)
	if !ok || strings.TrimSpace(image) == "" {
		return fmt.Errorf("stages.build.%s.image must be a non-empty string", output.Name)
	}

	contextDir := "."
	if value, exists := output.Build["context"]; exists {
		var ok bool
		contextDir, ok = value.(string)
		if !ok || strings.TrimSpace(contextDir) == "" {
			return fmt.Errorf("stages.build.%s.context must be a non-empty relative directory", output.Name)
		}
	}
	contextDir = strings.TrimSpace(strings.TrimPrefix(contextDir, "./"))
	if path.IsAbs(contextDir) || path.Clean(contextDir) == ".." || strings.HasPrefix(path.Clean(contextDir), "../") {
		return fmt.Errorf("stages.build.%s.context must stay inside the repository", output.Name)
	}
	contextDir = path.Clean(contextDir)
	if contextDir != "." {
		found := false
		for file := range files {
			if strings.HasPrefix(file, contextDir+"/") {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("stages.build.%s.context directory %q does not exist at this commit", output.Name, contextDir)
		}
	}

	dockerfile := "Dockerfile"
	if value, exists := output.Build["dockerfile"]; exists {
		var ok bool
		dockerfile, ok = value.(string)
		if !ok || strings.TrimSpace(dockerfile) == "" {
			return fmt.Errorf("stages.build.%s.dockerfile must be a non-empty relative path", output.Name)
		}
	}
	dockerfile = strings.TrimSpace(dockerfile)
	if path.IsAbs(dockerfile) || path.Clean(dockerfile) == ".." || strings.HasPrefix(path.Clean(dockerfile), "../") {
		return fmt.Errorf("stages.build.%s.dockerfile must stay inside the build context", output.Name)
	}
	expected := path.Clean(dockerfile)
	if contextDir != "." {
		expected = path.Join(contextDir, expected)
	}
	if _, exists := files[expected]; !exists {
		return fmt.Errorf("stages.build.%s Dockerfile %q does not exist at this commit", output.Name, expected)
	}
	if value, exists := output.Build["tag"]; exists {
		if _, ok := value.(string); !ok {
			return fmt.Errorf("stages.build.%s.tag must be a string", output.Name)
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
		if !strings.HasSuffix(name, "_IMAGE_URL") {
			return fmt.Errorf("invalid generated image variable ${%s}: expected the _IMAGE_URL suffix", name)
		}
		if _, ok := available[name]; !ok {
			return fmt.Errorf("unknown generated image variable ${%s}", name)
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
