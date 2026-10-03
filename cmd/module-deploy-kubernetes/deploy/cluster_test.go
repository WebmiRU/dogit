package deploy

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/cmd/module-deploy-kubernetes/k8s"
)

// The whole deployment against a real cluster, because the phases are only worth
// anything if they hold against the thing they are talking to.
//
// A fake can tell you the order the code calls things in. Only a cluster can tell you
// that a Job really does not finish until its pod does, that a Deployment's new pods
// come up alongside the old ones rather than after them, or that a rollout which looks
// finished in the first second was not finished at all.
//
//	KCUBECONFIG=~/.kube/config go test ./cmd/module-deploy-kubernetes/deploy -run Cluster

func clusterSetup(t *testing.T) (k8s.Client, string) {
	t.Helper()

	kubeconfig := os.Getenv("KCUBECONFIG")
	if kubeconfig == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skip("no home directory to look for a kubeconfig in")
		}
		kubeconfig = home + "/.kube/config"
		if _, err := os.Stat(kubeconfig); err != nil {
			t.Skip("no kubeconfig; not testing against a cluster")
		}
	}

	// The file's contents, because that is what a settings page holds: the module is
	// given a kubeconfig rather than a path it may not be able to open.
	contents, err := os.ReadFile(kubeconfig)
	if err != nil {
		t.Skipf("the kubeconfig could not be read: %v", err)
	}

	client, err := k8s.Connect(context.Background(), k8s.Access{Kubeconfig: contents})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	name := "dogit-deploy-" + uuid.NewString()[:8]
	manifest := "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: " + name + "\n"
	if _, err := client.Apply(context.Background(), k8s.Object{
		APIVersion: "v1", Kind: "Namespace", Name: name, Body: []byte(manifest),
	}); err != nil {
		t.Fatalf("create the namespace: %v", err)
	}

	t.Cleanup(func() {
		_ = exec.Command("kubectl", "delete", "namespace", name,
			"--ignore-not-found", "--wait=false").Run()
	})
	return client, name
}

const busyboxJob = `apiVersion: batch/v1
kind: Job
metadata:
  name: migrate
  namespace: %s
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: run
          image: busybox:1.36
          command: ["sh", "-c", "echo migrated"]

`

const failingJob = `apiVersion: batch/v1
kind: Job
metadata:
  name: migrate
  namespace: %s
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: run
          image: busybox:1.36
          command: ["sh", "-c", "echo no; exit 1"]

`

func namespaceJob(t *testing.T, template string, space string) k8s.Object {
	t.Helper()
	return k8s.Object{
		APIVersion: "batch/v1", Kind: "Job", Namespace: space, Name: "migrate",
		Body: []byte(sprintf(template, space)),
	}
}

// A deployment that goes all the way through: the Job passes, the pods come up, and
// the history says what was deployed.
func TestClusterADeploymentGoesAllTheWayThrough(t *testing.T) {
	client, space := clusterSetup(t)
	deployer := &Deployer{
		client:  client,
		history: newHistory(),
		Now:     time.Now,
	}

	record, err := deployer.Run(context.Background(), Request{
		Project:     "home-store/www",
		Cluster:     "test",
		Namespace:   space,
		Image:       "registry.k8s.io/pause:3.9",
		Placeholder: "IMAGE",
		Manifests: []k8s.Object{{
			APIVersion: "apps/v1", Kind: "Deployment", Namespace: space, Name: "app",
			Body: []byte(deploymentFor(space)),
		}},
		Pre:            []Job{{Name: "migrate", Object: namespaceJob(t, busyboxJob, space)}},
		WaitForRollout: true,
		Rollout:        "app",
		Workload:       "app",
		Timeout:        2 * time.Minute,
	})
	if err != nil {
		t.Fatalf("deploy: %v (state %q, phase %q: %s)", err, record.State, record.Phase, record.Reason)
	}

	if record.State != StateSucceeded {
		t.Errorf("state is %q", record.State)
	}

	live, err := client.Get(context.Background(), k8s.Object{
		APIVersion: "apps/v1", Kind: "Deployment", Namespace: space, Name: "app",
	})
	if err != nil {
		t.Fatalf("read the deployment back: %v", err)
	}
	containers, _, err := nestedContainers(live)
	if err != nil {
		t.Fatalf("read the containers: %v", err)
	}
	if image := stringField(containers, 0, "image"); image != "registry.k8s.io/pause:3.9" {
		t.Errorf("the cluster runs %q", image)
	}
}

// The case the phases exist for: a migration that fails must stop the deployment
// before anything is applied. Against a cluster this is not a mock's say-so — the Job
// really does fail, and the Deployment really is not there.
func TestClusterAFailedMigrationLeavesTheClusterUntouched(t *testing.T) {
	client, space := clusterSetup(t)
	deployer := &Deployer{client: client, history: newHistory(), Now: time.Now}

	record, err := deployer.Run(context.Background(), Request{
		Project:     "home-store/www",
		Cluster:     "test",
		Namespace:   space,
		Image:       "registry.k8s.io/pause:3.9",
		Placeholder: "IMAGE",
		Manifests: []k8s.Object{{
			APIVersion: "apps/v1", Kind: "Deployment", Namespace: space, Name: "app",
			Body: []byte(deploymentFor(space)),
		}},
		Pre: []Job{{Name: "migrate", Object: namespaceJob(t, failingJob, space)}},
	})

	if err == nil {
		t.Fatalf("a failed migration was reported as a deployment; state %q", record.State)
	}
	if record.Phase != PhasePre {
		t.Errorf("recorded as failing in %q, want pre", record.Phase)
	}

	// Nothing was applied, and the cluster agrees: no Deployment exists.
	if _, err := client.Get(context.Background(), k8s.Object{
		APIVersion: "apps/v1", Kind: "Deployment", Namespace: space, Name: "app",
	}); err == nil {
		t.Error("the Deployment exists although the migration failed")
	} else {
		t.Logf("and the cluster agrees: %v", err)
	}
}

// A rollback on a real cluster, with two real deployments behind it.
func TestClusterARollbackPutsThePreviousImageBack(t *testing.T) {
	client, space := clusterSetup(t)
	deployer := &Deployer{client: client, history: newHistory(), Now: time.Now}

	for _, image := range []string{"registry.k8s.io/pause:3.9", "registry.k8s.io/pause:3.10"} {
		_, err := deployer.Run(context.Background(), Request{
			Project: "home-store/www", Cluster: "test", Namespace: space,
			Image: image, Placeholder: "IMAGE",
			Manifests: []k8s.Object{{
				APIVersion: "apps/v1", Kind: "Deployment", Namespace: space, Name: "app",
				Body: []byte(deploymentFor(space)),
			}},
			WaitForRollout: true, Rollout: "app", Workload: "app",
			Timeout: 2 * time.Minute,
		})
		if err != nil {
			t.Fatalf("deploy %s: %v", image, err)
		}
	}

	record, err := deployer.Rollback(context.Background(), "home-store/www", "test", space)
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if record.State != StateRolledBack {
		t.Errorf("the history says %q after a rollback", record.State)
	}

	revisions, err := client.Revisions(context.Background(), space, "app")
	if err != nil {
		t.Fatalf("revisions: %v", err)
	}
	for _, one := range revisions {
		if one.Current {
			t.Logf("after the rollback the cluster runs %s (revision %d)", one.Image, one.Revision)
		}
	}
}
