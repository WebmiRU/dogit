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
//	 module: kubernetes               # who does the deploying
//	 target: production               # a row of that module's own settings: the place,
//	                                 # with the cluster and the namespace it carries
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

	// Name is what this deployment is called in the repository's own terms.
	//
	// Not decoration. A file that deploys three places produces three records, and
	// without a name each of them is only distinguishable by the cluster and namespace
	// they happen to use — which is not what anybody reading a failed deployment is
	// looking for.
	Name string `yaml:"name"`

	// Rules decide when this one runs, in the same language jobs use. A deployment
	// with no rules runs whenever there is a deploy at all, which is the behaviour of
	// every file written before there was more than one.
	Rules []Rule `yaml:"rules"`

	// TagOnly says this place may only be reached by a tag, never by a branch.
	//
	// The default for anywhere that matters, and enforced here rather than by
	// convention: a production cluster that follows every push to the default branch
	// is a deployment nobody chose, and the fact that it was written down correctly
	// should not be the only thing standing between a mistake and production.
	TagOnly bool `yaml:"tag_only"`

	// Module names the deploy module to do it, such as "kubernetes". Written rather
	// than assumed: the same word will mean Nomad or an archive over SSH later, and a
	// file that does not say which cannot be read by somebody else's module.
	//
	// Called a module and not a target, because the target of a deployment is where it
	// goes: `target: staging` is the place, and a file where one word meant both is a
	// file nobody can read twice without getting it wrong.
	Module string `yaml:"module"`

	// Target is which of the module's places to deploy to — a row of the module's own
	// settings, by the name that row is written down under.
	//
	// A place and not a cluster, because one cluster holds as many places as there are
	// namespaces in it, and a configuration that named only the cluster could not say
	// which of them it meant. It was also the wrong way round: where a cluster is, and
	// which cluster it is, belongs to the row that carries the kubeconfig — the place
	// knows, and the file only has to name it.
	//
	// Names need not be unique: every place with this name is deployed to, so two of
	// them in two namespaces is a way of saying "both of these".
	Target string `yaml:"target"`

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

// DeploysFor is the places that run for this ref, in the order they were written.
//
// The rules are the same language jobs use, and a place with no rules runs always: that
// is what every file written before there was more than one place already does.
//
// TagOnly is enforced here rather than left to whoever writes the file. A place marked
// that way does not run for a branch at all, whatever its rules say — the mark exists
// precisely for the case where somebody edits a rule and means it.
func DeploysFor(entries []DeploySpec, ref Ref) []DeploySpec {
	chosen := []DeploySpec{}
	for _, spec := range entries {
		if spec.TagOnly && !ref.IsTag {
			continue
		}
		if len(spec.Rules) > 0 {
			matched := false
			for _, rule := range spec.Rules {
				if ruleRuns(rule, ref, false) {
					matched = rule.When != "never"
					break
				}
			}
			if !matched {
				continue
			}
		}
		chosen = append(chosen, spec)
	}
	return chosen
}

// Empty is whether this says nothing at all.
func (d DeploySpec) Empty() bool {
	return !d.Present
}

// parseDeploys reads the `deploys` key: a list of places, each with a name and rules.
func parseDeploys(node *yaml.Node) ([]DeploySpec, error) {
	if node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("expected a list of places to deploy to")
	}

	entries := make([]DeploySpec, 0, len(node.Content))
	for _, item := range node.Content {
		if item.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("each place is a block with at least a target")
		}
		spec, err := parseDeploy(item)
		if err != nil {
			return nil, err
		}
		entries = append(entries, spec)
	}
	return entries, nil
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

	// Refused rather than ignored. A misspelt `target` that is silently dropped is
	// a deploy that goes nowhere, which is the one mistake in this file that cannot be
	// undone by looking at the result.
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
		case "name", "module", "target", "manifests", "pre", "post",
			"expect", "rollout", "timeout", "rules", "tag_only":
		default:
			return spec, fmt.Errorf(
				"deploy has no field called %q; it knows name, module, target, "+
					"manifests, pre, post, expect, rollout, timeout, rules "+
					"and tag_only", key)
		}
	}

	if strings.TrimSpace(spec.Module) == "" {
		return spec, fmt.Errorf("deploy does not say who deploys it: name a module")
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
