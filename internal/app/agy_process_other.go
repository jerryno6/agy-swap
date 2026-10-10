//go:build !windows

package app

// runningAgyProcesses is only implemented on Windows, where a running agy
// process can write a refreshed token back over the switched credential.
func runningAgyProcesses() int { return 0 }
