package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/ewolf/dogit/internal/models"
	"go.yaml.in/yaml/v3"
)

// logicalDeployCandidateKey groups retries and settings failures for the same repository target.
// It is not a cluster lock; it prevents a newer, unresolvable configuration from leaving an
// older candidate eligible merely because the core could not determine the physical target.
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
			Server                   string `yaml:"server"`
			TLSServerName             string `yaml:"tls-server-name"`
			CertificateAuthority      string `yaml:"certificate-authority"`
			CertificateAuthorityData  string `yaml:"certificate-authority-data"`
		} `yaml:"cluster"`
	} `yaml:"clusters"`
}

// physicalDeployIdentity resolves the destination the same way a kubeconfig does: selected
// context -> cluster entry -> API server. The target name is intentionally absent from the
// resulting key, so two aliases pointing at the same server and namespace share one lock.
// Authentication material is never included in the key; only a digest of the CA data is.
func physicalDeployIdentity(rows []map[string]json.RawMessage, target, namespace string) (string, error) {
	if strings.TrimSpace(namespace) == "" {
		return "", fmt.Errorf("the namespace for target %q is empty; refusing to deploy without a lock", target)
	}

	rawKubeconfig, hasKubeconfig := placeField(rows, target, "kubeconfig")
	var kubeconfig string
	if hasKubeconfig {
		if err := json.Unmarshal(rawKubeconfig, &kubeconfig); err != nil {
			return "", fmt.Errorf("read kubeconfig for target %q: %w", target, err)
		}
	}
	rawInCluster, hasInCluster := placeField(rows, target, "in_cluster")
	var inCluster bool
	if hasInCluster {
		if err := json.Unmarshal(rawInCluster, &inCluster); err != nil {
			return "", fmt.Errorf("read in-cluster setting for target %q: %w", target, err)
		}
	}
	if strings.TrimSpace(kubeconfig) == "" {
		if inCluster {
			// The API server URL is not available to the core in this mode. Use one
			// conservative shared identity per namespace rather than guessing from a
			// project or module name and allowing aliases to overlap.
			return "k8s:in-cluster:namespace:" + strings.TrimSpace(namespace), nil
		}
		return "", fmt.Errorf("target %q has neither a kubeconfig nor in_cluster enabled; refusing to deploy without a lock", target)
	}

	var config kubeconfigIdentity
	if err := yaml.Unmarshal([]byte(kubeconfig), &config); err != nil {
		return "", fmt.Errorf("parse kubeconfig for target %q: %w", target, err)
	}
	contextName := config.CurrentContext
	if rawContext, ok := placeField(rows, target, "context"); ok {
		var chosen string
		if err := json.Unmarshal(rawContext, &chosen); err != nil {
			return "", fmt.Errorf("read kubeconfig context for target %q: %w", target, err)
		}
		if strings.TrimSpace(chosen) != "" {
			contextName = strings.TrimSpace(chosen)
		}
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
	var server, tlsServerName, caPath, caData string
	for _, candidate := range config.Clusters {
		if candidate.Name == clusterName {
			server = strings.TrimSpace(candidate.Cluster.Server)
			tlsServerName = strings.TrimSpace(candidate.Cluster.TLSServerName)
			caPath = strings.TrimSpace(candidate.Cluster.CertificateAuthority)
			caData = strings.TrimSpace(candidate.Cluster.CertificateAuthorityData)
			break
		}
	}
	parsed, err := url.Parse(server)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("selected cluster for target %q has no usable API server; refusing to deploy without a lock", target)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	server = strings.TrimRight(parsed.String(), "/")
	caSum := sha256.Sum256([]byte(caData))
	material := server + "\x00" + tlsServerName + "\x00" + caPath + "\x00" +
		hex.EncodeToString(caSum[:]) + "\x00" + strings.TrimSpace(namespace)
	sum := sha256.Sum256([]byte(material))
	return "k8s:" + hex.EncodeToString(sum[:]), nil
}
