package main

import "runtime"

// schedGetaffinityInto reports the processors this process may run on.
//
// A container is usually limited to a subset of the machine's processors, and that
// subset is what a runner should report: capacity it cannot use is not capacity.
func schedGetaffinityInto(set *[]int) error {
	*set = make([]int, runtime.NumCPU())
	return nil
}
