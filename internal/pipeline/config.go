package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ConfigFileName is where a project's pipeline configuration lives.
//
// Called what it is: this is dogit's own configuration, in a dogit installation,
// and borrowing another forge's name for it would be a small lie that everybody
// has to remember to un-learn.
const ConfigFileName = ".dogit-ci.yml"

// The format is dogit's own. It is small on purpose: what it does not understand
// is refused rather than ignored, so a file always does what it appears to do, and
// it will grow with the product rather than with anybody else's.

// Config is a parsed pipeline configuration.
//
// It is a deliberately small subset of what GitLab's format allows. A full
// implementation of that format is a project of its own, and pretending to
// support it here would mean silently ignoring the parts nobody implemented —
// which is the worst outcome for a file people write expecting it to run.
type Config struct {
	// Stages run in order. A job in a later stage waits for the earlier ones.
	Stages []string `yaml:"stages"`
	// Default is what a job inherits when it says nothing.
	Default JobSpec `yaml:"default"`
	// Variables are available to every job.
	Variables map[string]any `yaml:"variables"`
	// Notify is what this pipeline says about being announced. Its absence is
	// silence, and is handled by the type rather than by a zero value somewhere else.
	Notify NotifySpec `yaml:"-"`
	// Jobs are the named tasks, in the order they were written.
	Jobs map[string]JobSpec `yaml:"-"`
	// Order preserves the order the jobs were written in, which is the order a
	// person wrote them in and the order they are displayed in.
	Order []string `yaml:"-"`
}

// JobSpec is one job's definition.
type JobSpec struct {
	Stage        string         `yaml:"stage"`
	Image        string         `yaml:"image"`
	Script       []string       `yaml:"script"`
	BeforeScript []string       `yaml:"before_script"`
	Tags         []string       `yaml:"tags"`
	Needs        []string       `yaml:"needs"`
	AllowFailure bool           `yaml:"allow_failure"`
	Variables    map[string]any `yaml:"variables"`
	Rules        []Rule         `yaml:"rules"`
	Only         RuleCondition  `yaml:"only"`
	Except       RuleCondition  `yaml:"except"`
	Build        map[string]any `yaml:"build"`
	Cache        map[string]any `yaml:"cache"`

	// Everything this build does not understand is kept rather than refused: an
	// `artifacts` section or a `services` list is not this build's business, and
	// rejecting a file for containing one would make it impossible to write a
	// configuration that works on somebody else's instance.
	//
	// It is kept, not acted on, and what was kept is visible in the job's view so
	// nobody can believe an unknown section did something.
	Raw map[string]any `yaml:",inline"`
}

type Rule struct {
	If      string       `yaml:"if"`
	Changes []RuleChange `yaml:"changes"`
	When    string       `yaml:"when"`
}

// RuleChange is one entry of a `changes` rule, written either as a path or as a
// mapping with options. Both spellings are read because both are natural and
// neither is a mistake.
type RuleChange struct {
	Paths []string `yaml:"paths"`
}

// UnmarshalYAML accepts both spellings, because both are common and neither is
// wrong: `changes: [src/**]` and `changes: [{paths: [src/**]}]` mean the same.
func (c *RuleChange) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		c.Paths = []string{node.Value}
		return nil
	}

	type plain RuleChange
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*c = RuleChange(decoded)
	return nil
}

// Parse reads a configuration.
//
// The format is dogit's own and deliberately small. What it does not understand is
// reported rather than ignored: a file somebody wrote to do something, silently
// doing something else or nothing, is the failure mode that costs an afternoon.
func Parse(data []byte) (*Config, error) {
	config := &Config{
		Variables: map[string]any{},
		Jobs:      map[string]JobSpec{},
	}

	// Read twice: once as the keys this format defines, and once as the document,
	// because the document knows what order things were written in.
	if err := yaml.Unmarshal(data, config); err != nil {
		return nil, fmt.Errorf("parse pipeline configuration: %w", err)
	}

	// Parsed as a document as well as a struct, because the struct knows what each
	// key means and the document knows what order they were written in.
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("parse pipeline configuration: %w", err)
	}
	raw := topLevelKeys(&document)

	// Only the keys this build understands at the top level are reserved. Reserving
	// more would swallow jobs that happen to be called "image" or "cache", and a job
	// that silently disappears from a pipeline is worse than one that runs with a
	// default image.
	reserved := map[string]bool{
		"stages": true, "default": true, "variables": true,
		"workflow": true, "include": true,
		"before_script": true, "after_script": true,
		// Not a job. Read separately: `notify` decides whether the run speaks, and a
		// job of that name would otherwise be parsed as one.
		"notify": true,
	}

	// Read as a document rather than as a field of the struct, because `notify` is
	// three different shapes of thing — a list, one entry, or `false` — and only one
	// of them is a value the struct can hold.
	for index := 0; index+1 < len(raw); index += 2 {
		if raw[index].Value != "notify" {
			continue
		}
		notify, err := parseNotify(raw[index+1])
		if err != nil {
			return nil, fmt.Errorf("notify: %w", err)
		}
		config.Notify = notify
	}

	// Written order, not sorted order: the order somebody wrote jobs in is the order
	// they think about them in, and a page that reorders them reads as a mistake.
	for index := 0; index+1 < len(raw); index += 2 {
		key := raw[index].Value
		if reserved[key] {
			continue
		}

		var job JobSpec
		if err := raw[index+1].Decode(&job); err != nil {
			return nil, fmt.Errorf("job %q: %w", key, err)
		}
		config.Jobs[key] = job
		config.Order = append(config.Order, key)
	}

	for name, job := range config.Jobs {
		if job.Stage == "" {
			job.Stage = config.Default.Stage
		}
		if job.Stage == "" {
			job.Stage = "test"
		}
		if job.Image == "" {
			job.Image = config.Default.Image
		}
		if job.Image == "" {
			job.Image = "alpine:3.21"
		}
		config.Jobs[name] = job
	}

	if len(config.Stages) == 0 {
		config.Stages = []string{"build", "test", "deploy"}
	}
	return config, nil
}

// Load reads a project's configuration from a checkout.
func Load(checkout string) (*Config, error) {
	data, err := os.ReadFile(filepath.Join(checkout, ConfigFileName))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", ConfigFileName, err)
	}
	return Parse(data)
}

// VariablesAsStrings returns what a run was configured with, as the string map the
// pipeline column takes.
//
// A variable nobody can read as text is not one a job can use, so a number is
// written out rather than stored as JSON and decoded at some later point by
// whoever happens to be reading the job.
func (c *Config) VariablesAsStrings() map[string]string {
	variables := map[string]string{}
	for key, value := range c.Variables {
		switch typed := value.(type) {
		case string:
			variables[key] = typed
		case nil:
			variables[key] = ""
		default:
			variables[key] = fmt.Sprint(typed)
		}
	}
	return variables
}

// Exists reports whether a checkout has a pipeline configuration at all.
//
// A project without one is not broken: most projects have nothing to build, and
// the page says so rather than showing an error nobody can act on.
func Exists(checkout string) bool {
	_, err := os.Stat(filepath.Join(checkout, ConfigFileName))
	return err == nil
}

// RunsOn says whether a job should run for a branch.
//
// A job with a `rules` list runs only when one of its rules says so. That is the
// opposite of the obvious reading, and getting it wrong is how a deployment ends up
// running on every push.
//
// A rule whose condition this build cannot read matches nothing. Refusing is the
// whole point: a pipeline that guesses at what it does not understand is a pipeline
// that publishes something somebody told it not to.
func RunsOn(job JobSpec, branch string, changes bool) bool {
	if len(job.Rules) > 0 {
		for _, rule := range job.Rules {
			if !ruleRuns(rule, branch, changes) {
				continue
			}
			return rule.When != "never"
		}
		return false
	}

	// `only` narrows and `except` removes. Both absent means the job runs.
	if len(job.Only.Refs) > 0 && !matchesBranch(job.Only.Refs, branch) {
		return false
	}
	if matchesBranch(job.Except.Refs, branch) {
		return false
	}
	return true
}

// ruleRuns is one rule's condition. A condition nobody here reads matches nothing.
func ruleRuns(rule Rule, branch string, changes bool) bool {
	if rule.If == "" {
		// A rule with only `changes` is about files, not branches.
		return len(rule.Changes) > 0 && changes
	}
	if strings.HasPrefix(strings.TrimSpace(rule.If), "$") {
		return matchesBranchIf(rule.If, branch)
	}
	return false
}

// RuleCondition narrows a job by branch, written as `only` or `except`.
type RuleCondition struct {
	Refs []string `yaml:"refs"`
}

func matchesBranch(patterns []string, branch string) bool {
	for _, pattern := range patterns {
		if pattern == branch || pattern == "*" {
			return true
		}
		if strings.HasSuffix(pattern, "/*") &&
			strings.HasPrefix(branch, strings.TrimSuffix(pattern, "*")) {
			return true
		}
	}
	return false
}

// matchesBranchIf reads the one condition that is worth reading without a
// language: "only on these branches", written the way GitLab writes it.
// isKnownCondition says whether a condition is one this build reads.
//
// An expression mentioning something else — a variable, a comparison against a
// changed file — is not answered here. Treating it as "true" would let a deploy
// job run on every push, which is the one mistake a pipeline must not make.
func isKnownCondition(condition string) bool {
	trimmed := strings.TrimSpace(condition)
	if !strings.HasPrefix(trimmed, "$") {
		return false
	}
	for _, variable := range []string{"$CI_COMMIT_BRANCH", "$CI_COMMIT_REF_NAME"} {
		if strings.HasPrefix(trimmed, variable) &&
			(trimmed == variable ||
				strings.HasPrefix(trimmed[len(variable):], " ==") ||
				strings.HasPrefix(trimmed[len(variable):], " !=")) {
			return true
		}
	}
	return false
}

func matchesBranchIf(condition, branch string) bool {
	if condition == "" {
		return false
	}
	trimmed := strings.TrimSpace(condition)
	if !strings.HasPrefix(trimmed, "$") {
		return false
	}

	// $CI_COMMIT_BRANCH == "main"
	for _, operator := range []string{"==", "!="} {
		if index := strings.Index(trimmed, operator); index >= 0 {
			left := strings.TrimSpace(trimmed[:index])
			right := strings.Trim(strings.TrimSpace(trimmed[index+2:]), `"'`)
			if left != "$CI_COMMIT_BRANCH" && left != "$CI_COMMIT_REF_NAME" {
				return false
			}
			equal := right == branch || right == "*" ||
				(strings.HasSuffix(right, "/*") && strings.HasPrefix(branch, strings.TrimSuffix(right, "*")))
			if operator == "==" {
				return equal
			}
			return !equal
		}
	}
	return false
}

// writtenOrder is the order the top-level keys appear in the file.
//
// Read from the parsed document rather than from a map, which has no order of its
// own: a map iteration would sort them, and a page listing jobs alphabetically is
// a page that has lost the only thing a human put into it.
func writtenOrder(raw []*yaml.Node) []string {
	keys := make([]string, 0, len(raw)/2)
	for index := 0; index+1 < len(raw); index += 2 {
		keys = append(keys, raw[index].Value)
	}
	return keys
}

// topLevelKeys is the document's own mapping, as it was written.
//
// Not a map: a map has no order, and the order jobs were written in is the only
// ordering a person put into the file.
func topLevelKeys(document *yaml.Node) []*yaml.Node {
	root := document
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return nil
	}
	return root.Content
}
