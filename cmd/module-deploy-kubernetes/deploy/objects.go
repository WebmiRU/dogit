package deploy

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/ewolf/dogit/cmd/module-deploy-kubernetes/k8s"
)

// Reading a Job and a Deployment back out of what the cluster answered.
//
// Both are read as the generic shape the cluster returns rather than through a typed
// struct, because a Job's outcome lives in a status condition whose reason is the
// message a person actually wants — and a typed field would give a boolean where the
// explanation is what matters.

// jobFailed says a Job failed, and why.
func jobFailed(live *unstructured.Unstructured) (bool, string) {
	for _, condition := range jobConditions(live) {
		if status, _ := condition["status"].(string); status != "True" {
			continue
		}
		if conditionType, _ := condition["type"].(string); conditionType != "Failed" {
			continue
		}
		reason := strings.TrimSpace(stringOf(condition, "reason"))
		message := strings.TrimSpace(stringOf(condition, "message"))
		return true, joinReason(reason, message)
	}
	return false, ""
}

// jobSucceeded says a Job has finished successfully.
func jobSucceeded(live *unstructured.Unstructured) (bool, string) {
	for _, condition := range jobConditions(live) {
		if status, _ := condition["status"].(string); status != "True" {
			continue
		}
		if conditionType, _ := condition["type"].(string); conditionType != "Complete" {
			continue
		}
		return true, strings.TrimSpace(stringOf(condition, "message"))
	}
	return false, ""
}

func jobConditions(live *unstructured.Unstructured) []map[string]any {
	conditions, found, err := unstructured.NestedSlice(live.Object, "status", "conditions")
	if err != nil || !found {
		return nil
	}

	out := make([]map[string]any, 0, len(conditions))
	for _, one := range conditions {
		if condition, ok := one.(map[string]any); ok {
			out = append(out, condition)
		}
	}
	return out
}

// joinReason puts the two halves of a condition into one sentence, and does not
// produce a colon followed by nothing.
func joinReason(reason, message string) string {
	switch {
	case reason == "" && message == "":
		return "the cluster did not say why"
	case message == "":
		return reason
	case reason == "":
		return message
	default:
		return fmt.Sprintf("%s: %s", reason, message)
	}
}

func stringOf(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return value
}

// workloadOf is which Deployment a deployment record is about.
//
// Recorded when the deployment was made, from the objects it applied, rather than
// configured anywhere else: a second place to name the workload is a second place to
// get it wrong, and a rollback that looked at the wrong name would undo somebody
// else's deployment.
func workloadOf(record *Deployment) string {
	if record == nil {
		return ""
	}
	return record.Workload
}

// WorkloadsOf is the deployment's objects that are workloads, by name.
func WorkloadsOf(objects []k8s.Object) []string {
	out := []string{}
	for _, object := range objects {
		if object.Kind == "Deployment" || object.Kind == "StatefulSet" {
			out = append(out, object.Name)
		}
	}
	return out
}
