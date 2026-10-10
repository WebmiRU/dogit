package api

import (
	"encoding/json"
	"testing"
)

func identityRow(t *testing.T, name, namespace, contextName, server string) map[string]json.RawMessage {
	t.Helper()
	document := "apiVersion: v1\nkind: Config\ncurrent-context: " + contextName + "\n" +
		"clusters:\n- name: selected\n  cluster:\n    server: " + server + "\n" +
		"contexts:\n- name: " + contextName + "\n  context:\n    cluster: selected\n"
	values := map[string]any{
		"name": name, "default_namespace": namespace, "kubeconfig": document, "context": "",
	}
	row := map[string]json.RawMessage{}
	for key, value := range values {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal %s: %v", key, err)
		}
		row[key] = raw
	}
	return row
}

func TestPhysicalDeployIdentityJoinsAliasesForTheSameKubernetesNamespace(t *testing.T) {
	rows := []map[string]json.RawMessage{
		identityRow(t, "production", "web", "context-a", "https://KUBE.example.test:6443/"),
		identityRow(t, "prod-alias", "web", "context-b", "https://kube.example.test:6443"),
		identityRow(t, "prod-other-namespace", "jobs", "context-c", "https://kube.example.test:6443"),
	}
	first, err := physicalDeployIdentity(rows, "production", "web")
	if err != nil {
		t.Fatalf("resolve first destination: %v", err)
	}
	alias, err := physicalDeployIdentity(rows, "prod-alias", "web")
	if err != nil {
		t.Fatalf("resolve alias destination: %v", err)
	}
	otherNamespace, err := physicalDeployIdentity(rows, "prod-other-namespace", "jobs")
	if err != nil {
		t.Fatalf("resolve another namespace: %v", err)
	}
	if first != alias {
		t.Errorf("aliases to the same API server and namespace got different locks: %q != %q", first, alias)
	}
	if first == otherNamespace {
		t.Errorf("different namespaces on one API server got the same lock: %q", first)
	}
}

func TestPhysicalDeployIdentityRefusesMissingOrUnparseableKubeconfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  map[string]json.RawMessage
	}{
		{name: "missing"},
		{name: "bad YAML", row: map[string]json.RawMessage{
			"name": json.RawMessage(`"prod"`), "kubeconfig": json.RawMessage(`"not: [valid"`),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := physicalDeployIdentity([]map[string]json.RawMessage{tc.row}, "prod", "web")
			if err == nil {
				t.Fatal("a destination without a resolvable API server received a lock identity")
			}
		})
	}
}
