//go:build linux

package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// A killed descendant can stay as a zombie until PID 1 reaps it. A zombie is
// not running, so it counts as gone. This matters when the test binary is not
// PID 1, for example under `docker run image go test ./...`.
func processAlive(pid int) bool {
	text, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	state, _, ok := splitStat(string(text))
	if !ok {
		return true
	}
	return state != 'Z'
}

func groupAlive(pgid int) bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return syscall.Kill(-pgid, 0) == nil
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		text, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			continue
		}
		state, group, ok := splitStat(string(text))
		if !ok || state == 'Z' {
			continue
		}
		if group == pgid {
			return true
		}
	}
	return false
}

func splitStat(text string) (state byte, pgid int, ok bool) {
	end := strings.LastIndexByte(text, ')')
	if end < 0 || end+2 >= len(text) {
		return 0, 0, false
	}
	state = text[end+2]
	fields := strings.Fields(text[end+2:])
	if len(fields) < 3 {
		return 0, 0, false
	}
	pgid, err := strconv.Atoi(fields[2])
	return state, pgid, err == nil
}
