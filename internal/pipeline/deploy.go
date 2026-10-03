package pipeline

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// What a pipeline says about deploying.
//
// This is the same bargain as `notify:`, and it is deliberate: the thing that decides
// is the thing that owns the code. A project that is deployed has to be able to say
// where it goes, and it cannot say that in a settings page — somebody changing the
// instance's default has no idea which of a hundred projects meant to be affected.
//
//	deploy:
//	 cluster: production-eu          # a row of settings inside the module
//	 namespace: web
//	 manifests:
//	   - k8s/deployment.yaml         # image: IMAGE, substituted by digest
//	   - k8s/service.yaml
//	 expect:                           # what must exist; never written
//	   secrets: [db-credentials]
//	 pre:                             # Jobs that must pass before the rollout
//	   - k8s/migrate-job.yaml
//	 post:                            # after, and separately judged
//	   - command: ["/app", "smoke"]
//	 rollout: true
//	 timeout: 10m
//
// There is no templating here on purpose. The repository's manifests stay declarative,
// and the one thing dogit substitutes is the image it just built — by digest, because a
// tag can move underneath a rollback and turn it into a deployment of something else.
type DeploySpec struct {
	// Present is whether the file said anything about deploying at all. A pipeline
	// with no deploy block is a pipeline that builds, and says nothing about that
	// either way.
	Present bool

	// Target names the deploy module to do it, such as "kubernetes". Written rather
	// than assumed: the same word will mean Nomad or an archive over SSH later, and a
	// file that does not say which cannot be read by somebody else's module.
	Target string `yaml:"target"`

	// Cluster is which of the target's rows to deploy to. Free text, because what
	// identifies a cluster is the module's business and not the core's.
	Cluster string `yaml:"cluster"`

	// Namespace is where in the cluster. Empty means the target's default.
	Namespace string `yaml:"namespace"`

	// Manifests are paths inside the repository, read at the commit being deployed.
	Manifests []string `yaml:"manifests"`

	// Pre and Post are one-shot jobs around the rollout. Pre must pass before anything
	// is applied — that is where a database migration belongs, because a migration that
	// fails halfway through a rolling update leaves half the pods on a new schema.
	Pre  []DeployStep `yaml:"pre"`
	Post []DeployStep `yaml:"post"`

	// Expect names what must already be in the cluster. Checked and never written: a
	// secret somebody created by hand is not dogit's to overwrite, and dogit has no
	// business holding its value.
	Expect DeployExpect `yaml:"expect"`

	// Rollout waits for the new pods to become ready and reports how it went, with the
	// reason when it did not.
	Rollout bool `yaml:"rollout"`

	// Timeout is how long to wait. Empty means the target's own.
	Timeout string `yaml:"timeout"`
}

// DeployStep is one one-shot job, before or after the rollout.
//
// Written either as a path to a manifest in the repository or as a command to run from
// an image. Both spellings are read because both are natural to write and neither is
// wrong:
//
//	pre:
//	  - k8s/migrate-job.yaml
//	post:
//	  - image: IMAGE
//	    command: ["/app", "smoke"]
type DeployStep struct {
	// Manifest is a path to a Job manifest in the repository. Empty when the step is
	// given as a command instead.
	Manifest string `yaml:"manifest"`
	// Command and Image run a command from an image: for a post-step that is just a
	// check, and writing a whole Job manifest for it would be ceremony.
	Image   string   `yaml:"image"`
	Command []string `yaml:"command"`
}

// UnmarshalYAML accepts a bare path as shorthand for a manifest, the way `changes:` does.
func (s *DeployStep) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		s.Manifest = node.Value
		return nil
	}

	type plain DeployStep
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*s = DeployStep(decoded)
	return nil
}

// DeployExpect is what has to be there already.
type DeployExpect struct {
	Secrets    []string `yaml:"secrets"`
	ConfigMaps []string `yaml:"config_maps"`
}

// Empty is whether this says nothing at all.
func (d DeploySpec) Empty() bool {
	return !d.Present
}

// parseDeploy reads the `deploy` key.
//
// A missing key and an empty one are the same thing on purpose: a project that only
// builds and never deploys should not have to write a block saying so, and a block
// that says nothing is not a deploy any more than no block is.
func parseDeploy(node *yaml.Node) (DeploySpec, error) {
	spec := DeploySpec{Present: true}

	if node.Tag == "!!null" {
		return spec, nil
	}
	if node.Kind != yaml.MappingNode {
		return spec, fmt.Errorf(
			"deploy must be a block of settings, one per line, and not a list")
	}

	if err := node.Decode(&spec); err != nil {
		return spec, fmt.Errorf("deploy: %w", err)
	}

	// Refused rather than ignored. A misspelt `namespace` that is silently dropped is
	// a deploy that goes to the wrong place, which is the one mistake in this file
	// that cannot be undone by looking at the result.
	for index := 0; index+1 < len(node.Content); index += 2 {
		key := node.Content[index].Value
		if key == "image" {
			// Named here rather than caught by the unknown-key refusal below, because
			// it is the one key somebody will reach for, and "you cannot choose the
			// image, it deploys what this run built" is an answer they can act on.
			return spec, fmt.Errorf(
				"deploy.image is written by dogit, not by the file: a deploy applies " +
					"what this run actually built")
		}

		switch key {
		case "target", "cluster", "namespace", "manifests", "pre", "post",
			"expect", "rollout", "timeout":
		default:
			return spec, fmt.Errorf(
				"deploy has no field called %q; it knows target, cluster, namespace, "+
					"manifests, pre, post, expect, rollout and timeout", key)
		}
	}

	if strings.TrimSpace(spec.Target) == "" {
		return spec, fmt.Errorf("deploy does not say where to deploy: name a target")
	}
	if len(spec.Manifests) == 0 {
		return spec, fmt.Errorf("deploy has no manifests, so there is nothing to apply")
	}
	for _, path := range spec.Manifests {
		if strings.HasPrefix(strings.TrimSpace(path), "/") || strings.Contains(path, "..") {
			return spec, fmt.Errorf(
				"deploy.manifests may only name files inside the repository; %q tries to leave it",
				path)
		}
	}
	for _, step := range append(append([]DeployStep{}, spec.Pre...), spec.Post...) {
		if step.Manifest == "" && len(step.Command) == 0 {
			return spec, fmt.Errorf("a deploy step has neither a manifest nor a command, so it does nothing")
		}
	}

	return spec, nil
}
