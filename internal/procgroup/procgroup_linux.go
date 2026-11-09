package procgroup

import "syscall"

// deathSignal makes the kernel kill the child when the thread that started
// it ends, which for Go is the node's end: the helper and the program it
// execs die with the node. Their own children are reached only by the
// group kill on a time limit, so a kill -9 of the node can leave them;
// that is M3c's known minor "a script outlives a killed node", narrowed.
func deathSignal(a *syscall.SysProcAttr) { a.Pdeathsig = syscall.SIGKILL }
