package k8s

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// Counts is what the wait is decided on, so what Counts says about the rollout is the thing
// that has to be right — and `rolloutOf` being correct on its own proves nothing about it,
// because `wait` never calls `rolloutOf`. It calls Counts.
//
// This is the link that was missing, and it is the link that failed in production: the rule
// was already written and already correct, three dozen lines away from the only caller, and
// nobody had put the two together. So it is tested here rather than in the rule's own test,
// where it would pass without anything ever having connected it.
func TestCountsCarriesTheClustersOwnJudgementOfTheRollout(t *testing.T) {
	for _, one := range []struct {
		name    string
		deploy  *appsv1.Deployment
		settled bool
	}{
		{
			// Mid-rollout: the controller has seen the spec, one new pod exists of
			// three, and the three old ones are still ready and still serving. Every
			// readiness number says three of three.
			name: "pods still on the old revision",
			deploy: deploymentFixture(2, 3, appsv1.DeploymentStatus{
				ObservedGeneration: 2, Replicas: 3,
				UpdatedReplicas: 1, ReadyReplicas: 3, AvailableReplicas: 3,
			}),
			settled: false,
		},
		{
			name: "new revision created but not serving",
			deploy: deploymentFixture(2, 3, appsv1.DeploymentStatus{
				ObservedGeneration: 2, Replicas: 3,
				UpdatedReplicas: 3, ReadyReplicas: 0, AvailableReplicas: 3,
			}),
			settled: false,
		},
		{
			name: "rollout finished",
			deploy: deploymentFixture(2, 3, appsv1.DeploymentStatus{
				ObservedGeneration: 2, Replicas: 3,
				UpdatedReplicas: 3, ReadyReplicas: 3, AvailableReplicas: 4,
			}),
			settled: true,
		},
		{
			// Scaled to nothing. This is the case the earlier fix was about, and the
			// judgement has to reach it only through the same door as everything else.
			name: "scaled to nothing",
			deploy: deploymentFixture(2, 0, appsv1.DeploymentStatus{
				ObservedGeneration: 2,
			}),
			settled: true,
		},
		{
			// The cluster has not seen the Deployment at all.
			name: "not seen yet",
			deploy: deploymentFixture(2, 3, appsv1.DeploymentStatus{
				ObservedGeneration: 1, Replicas: 3,
				UpdatedReplicas: 3, ReadyReplicas: 3, AvailableReplicas: 3,
			}),
			settled: false,
		},
	} {
		t.Run(one.name, func(t *testing.T) {
			client := &clusterClient{typed: fake.NewSimpleClientset(one.deploy)}

			// A tag, because that is the shape the answer has to be right in: the pods
			// being replaced and the pods replacing them carry the same name, so nothing
			// about the pods themselves can tell the two apart.
			got, err := client.Counts(context.Background(), "versions", "app",
				"registry.example.com/team/app:v2")
			if err != nil {
				t.Fatalf("read the counts: %v", err)
			}
			if got.Settled != one.settled {
				t.Errorf("Settled is %v, want %v — desired %d, ready %d, old up %d",
					got.Settled, one.settled, got.Desired, got.Ready, got.OldUp)
			}
		})
	}
}

func deploymentFixture(generation int64, replicas int32, status appsv1.DeploymentStatus) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "app",
			Namespace:  "versions",
			Generation: generation,
		},
		Spec:   appsv1.DeploymentSpec{Replicas: &replicas},
		Status: status,
	}
}
