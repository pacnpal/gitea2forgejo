//go:build windows

package main

import (
	"os"
	"os/exec"
)

// syscallExec on Windows runs the new binary as a child process, forwards
// its stdio, and exits with the child's exit code. Unix's true process
// replacement isn't available.
func syscallExec(path string, args, env []string) error {
	c := exec.Command(path, args[1:]...)
	c.Env = env
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
