package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"

	"github.com/ewolf/dogit/internal/models"
	"go.yaml.in/yaml/v3"
)

// logicalDeployCandidateKey groups retries and settings failures for the same repository target.
// It is not a cluster lock; it invalidates an older candidate when a newer pipeline names the
// same target but the core temporarily cannot resolve its physical destination.
func logicalDeployCandidateKey(project *models.Project, module, target string) string {
	projectID := "unknown-project"
	if project != nil {
		projectID = project.ID.String()
	}
	return "logical:" + projectID + ":" + strings.ToLower(strings.TrimSpace(module)) +
		":" + strings.ToLower(strings.TrimSpace(target))
}

type kubeconfigIdentity struct {
	CurrentContext string `yaml:"current-context"`
	Contexts       []struct {
		Name    string `yaml:"name"`
		Context struct {
			Cluster string `yaml:"cluster"`
		} `yaml:"context"`
	} `yaml:"contexts"`
	Clusters []struct {
		Name    string `yaml:"name"`
		Cluster struct {
			Server string `yaml:"server"`
		} `yaml:"cluster"`
	} `yaml:"clusters"`
}

func rowString(row map[string]json.RawMessage, key string) (string, bool, error) {
	raw, ok := row[key]
	if !ok {
		return "", false, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", true, fmt.Errorf("setting %s is not a string: %w", key, err)
	}
	return strings.TrimSpace(value), true, nil
}

// identityForPlaceRow resolves the API server in one module row and combines it with the
// namespace that the deployment module will actually use. No credential material is part of
// the key: different kubeconfigs for one API endpoint still have to share an exclusive lock.
func identityForPlaceRow(row map[string]json.RawMessage, target, namespace string) (string, error) {
	if strings.TrimSpace(namespace) == "" {
		return "", fmt.Errorf("the namespace for target %q is empty; refusing to deploy without a lock", target)
	}

	kubeconfig, _, err := rowString(row, "kubeconfig")
	if err != nil {
		return "", fmt.Errorf("read kubeconfig for target %q: %w", target, err)
	}
	if kubeconfig == "" {
		return "", fmt.Errorf("target %q has no kubeconfig; refusing to deploy without a physical lock", target)
	}

	var config kubeconfigIdentity
	if err := yaml.Unmarshal([]byte(kubeconfig), &config); err != nil {
		return "", fmt.Errorf("parse kubeconfig for target %q: %w", target, err)
	}
	contextName := config.CurrentContext
	chosen, _, err := rowString(row, "context")
	if err != nil {
		return "", fmt.Errorf("read kubeconfig context for target %q: %w", target, err)
	}
	if chosen != "" {
		contextName = chosen
	}
	if contextName == "" {
		return "", fmt.Errorf("target %q has no selected kubeconfig context; refusing to deploy without a lock", target)
	}

	clusterName := ""
	for _, candidate := range config.Contexts {
		if candidate.Name == contextName {
			clusterName = candidate.Context.Cluster
			break
		}
	}
	if clusterName == "" {
		return "", fmt.Errorf("context %q for target %q does not name a cluster; refusing to deploy without a lock", contextName, target)
	}

	server := ""
	for _, candidate := range config.Clusters {
		if candidate.Name == clusterName {
			server = strings.TrimSpace(candidate.Cluster.Server)
			break
		}
	}
	parsed, err := url.Parse(server)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("selected cluster for target %q has no usable API server; refusing to deploy without a lock", target)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	hostname := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	// An explicitly written default port and an omitted default port are the same
	// Kubernetes API endpoint. If left different, two aliases could take different locks
	// and mutate one namespace simultaneously.
	if (parsed.Scheme == "https" && port == "443") ||
		(parsed.Scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		parsed.Host = net.JoinHostPort(hostname, port)
	} else if strings.Contains(hostname, ":") {
		parsed.Host = "[" + hostname + "]"
	} else {
		parsed.Host = hostname
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	server = strings.TrimRight(parsed.String(), "/")

	// The API server and namespace define the mutation boundary. Authentication and CA
	// formatting are intentionally excluded so credentials/CA file paths do not split one
	// physical destination into multiple independent locks.
	sum := sha256.Sum256([]byte(server + "\x00" + strings.TrimSpace(namespace)))
	return "k8s:" + hex.EncodeToString(sum[:]), nil
}

// physicalDeployIdentities resolves every namespace a named target can fan out to. The
// Kubernetes deploy module permits multiple rows with one target name and deploys to all of
// them; returning only the first would leave the other namespaces unprotected.
func physicalDeployIdentities(rows []map[string]json.RawMessage, target string) (
	identities []string, namespaces []string, err error,
) {
	for _, row := range rows {
		name, exists, err := rowString(row, "name")
		if err != nil {
			return nil, nil, fmt.Errorf("read a deployment target name: %w", err)
		}
		if !exists || name != strings.TrimSpace(target) {
			continue
		}
		namespace, _, err := rowString(row, "default_namespace")
		if err != nil {
			return nil, nil, fmt.Errorf("read namespace for target %q: %w", target, err)
		}
		if namespace == "" {
			return nil, nil, fmt.Errorf("target %q has no default_namespace; refusing to guess the namespace for its lock", target)
		}
		identity, err := identityForPlaceRow(row, target, namespace)
		if err != nil {
			return nil, nil, err
		}
		identities = append(identities, identity)
		namespaces = append(namespaces, namespace)
	}
	if len(identities) == 0 {
		return nil, nil, fmt.Errorf("target %q has no configured physical destination", target)
	}

	identities = uniqueSorted(identities)
	namespaces = uniqueSorted(namespaces)
	return identities, namespaces, nil
}

// physicalDeployIdentity resolves one rollback destination. The module picks the first active
// row with this name, so the core does the same rather than guessing from a different alias row.
// The requested namespace is explicit in a rollback record and may differ from the row default.
func physicalDeployIdentity(rows []map[string]json.RawMessage, target, namespace string) (string, error) {
	for _, row := range rows {
		name, exists, err := rowString(row, "name")
		if err != nil {
			return "", fmt.Errorf("read a deployment target name: %w", err)
		}
		if !exists || name != strings.TrimSpace(target) {
			continue
		}
		enabledRaw, enabledExists := row["enabled"]
		if enabledExists {
			var enabled bool
			if err := json.Unmarshal(enabledRaw, &enabled); err != nil {
				return "", fmt.Errorf("read enabled state for target %q: %w", target, err)
			}
			if !enabled {
				continue
			}
		}
		return identityForPlaceRow(row, target, namespace)
	}
	return "", fmt.Errorf("target %q has no active physical destination; refusing to rollback without a lock", target)
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
