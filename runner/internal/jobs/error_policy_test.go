package jobs

import "testing"

func TestErrorPolicyDefaults(t *testing.T) {
	job := &Job{}
	if !breaksOnError(job) {
		t.Fatal("break_on_error must default to true")
	}
	if !checksExitStatus(job, 0) {
		t.Fatal("check_exit_status must default to true")
	}
}

func TestErrorPolicyHonorsExplicitFalse(t *testing.T) {
	stop := false
	job := &Job{
		BreakOnError:    &stop,
		CheckExitStatus: []bool{false, true},
	}
	if breaksOnError(job) {
		t.Fatal("explicit break_on_error=false was ignored")
	}
	if checksExitStatus(job, 0) {
		t.Fatal("explicit check_exit_status=false was ignored")
	}
	if !checksExitStatus(job, 1) {
		t.Fatal("explicit check_exit_status=true was ignored")
	}
	if !checksExitStatus(job, 2) {
		t.Fatal("missing command policy must default to checking the exit status")
	}
}
