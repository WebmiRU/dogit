package api

import "testing"

// One push is one operation, however many jobs it takes.
//
// The image is built by a job of its own and put somewhere by another, and a page watching a
// place is watching deployments. Filed under the reporting job's own id, a build's progress is a
// second operation as far as that page is concerned: a card of its own beside the deployment's,
// for one push. It was seen on a stand as four blocks where there was one deployment — two live
// and the same two again in the record below them — and nothing about it looked wrong one card at
// a time.
//
// The four are the tell rather than the complaint. One block per operation was the rule, and a
// reader who saw four knew something was off without being able to say what.
func TestOnePushIsOneOperation(t *testing.T) {
	const build, deploy = int64(170), int64(171)
	place := aPlaceNamed{place: "jabjab.ru", namespace: "versions-dev", job: deploy}

	if got := whichOperation(build, false, place); got != deploy {
		t.Errorf("a build was filed under job %d, want the deployment it was built for, %d",
			got, deploy)
	}
}

// And the deployment is always its own operation, whatever else the run holds.
//
// The module speaks for it and nobody else can: a line it says about itself is about itself, and
// re-filing it under another job would be the same mistake with the id changed.
func TestADeploymentIsAlwaysItsOwnOperation(t *testing.T) {
	const deploy = int64(171)
	place := aPlaceNamed{place: "jabjab.ru", namespace: "versions-dev", job: deploy}

	if got := whichOperation(deploy, true, place); got != deploy {
		t.Errorf("the deployment was filed under job %d, want itself", got)
	}
}

// With nowhere else to file it, a build is filed under its own job.
//
// A run that is not on its way anywhere has no deployment to belong to, and a build nobody asked
// to deploy is a thing of its own. Filing it under a deployment it is not part of would invent
// one.
func TestABuildWithNowhereToGoIsItself(t *testing.T) {
	const build = int64(170)

	if got := whichOperation(build, false, aPlaceNamed{place: "jabjab.ru"}); got != build {
		t.Errorf("a build for a place with no deploy job was filed under %d, want its own job %d",
			got, build)
	}
	if got := whichOperation(build, false, aPlaceNamed{}); got != build {
		t.Errorf("a build naming no place at all was filed under %d, want its own job %d", got, build)
	}
}
