//go:build darwin

package main

import "syscall"

func processAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func groupAlive(pgid int) bool { return syscall.Kill(-pgid, 0) == nil }
