//go:build darwin || linux

package main

import (
	"os"
	"syscall"
)

func restartUpdatedExecutable(path string) error {
	return syscall.Exec(path, append([]string{path}, os.Args[1:]...), os.Environ())
}
