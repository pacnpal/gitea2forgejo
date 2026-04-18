//go:build !windows

package main

import "syscall"

func syscallExec(path string, args, env []string) error {
	return syscall.Exec(path, args, env)
}
