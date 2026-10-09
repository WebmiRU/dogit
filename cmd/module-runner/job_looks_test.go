package main

import "testing"

// What a job's container is given to see its checkout, and where it works.
//
// The daemon is the host's, so a path in `-v` means a path on the host. A runner that keeps
// its workspace in a volume — which is every runner that is itself a container — has to hand
// the job that volume by name, or the job runs in a checkout that is not there.
func TestWhatAJobIsGivenToSeeItsCheckout(t *testing.T) {
	volumes, dir := whereTheJobLooks(config{workspace: "/data/work"},
		"/data/work/test-versions/job-7")
	if len(volumes) != 1 || volumes[0].Source != "/data/work/test-versions/job-7" ||
		volumes[0].Target != "/build" || dir != "/build" {
		t.Errorf("a runner whose workspace is a host path: %+v in %q", volumes, dir)
	}

	// The volume is mounted where this runner has it, which is the whole of the fix: the
	// checkout the script works in has to be at the same path in both containers, and a
	// volume mounted a level higher puts it a level shorter.
	volumes, dir = whereTheJobLooks(
		config{workspace: "/data/work", workspaceVolume: "dogit-runner-work",
			workspaceMount: "/data"},
		"/data/work/test-versions/job-7")
	if len(volumes) != 1 || volumes[0].Source != "dogit-runner-work" ||
		volumes[0].Target != "/data" || dir != "/data/work/test-versions/job-7" {
		t.Errorf("a runner whose workspace is a volume: %+v in %q", volumes, dir)
	}

	// Half the answer is no answer: a volume without the path it is mounted at here, or a
	// path without the volume, both mean the daemon would be given something else than
	// what this runner is looking at — and the path is the half that is easy to leave out.
	for _, c := range []config{
		{workspace: "/data/work", workspaceVolume: "dogit-runner-work"},
		{workspace: "/data/work", workspaceMount: "/data"},
	} {
		volumes, dir = whereTheJobLooks(c, "/data/work/test-versions/job-7")
		if len(volumes) != 1 || volumes[0].Source != "/data/work/test-versions/job-7" ||
			volumes[0].Target != "/build" || dir != "/build" {
			t.Errorf("%+v: %+v in %q, want the host path it was given", c, volumes, dir)
		}
	}
	if volumes[0].ReadOnly {
		t.Error("the checkout is mounted read-only, so a build cannot write into it")
	}
}
