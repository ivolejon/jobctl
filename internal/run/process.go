//go:build linux || darwin

package run

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ivolejon/jobctl/internal/state"
)

// Stop sends SIGTERM to the runner of the job's running receipt. The runner
// forwards it to the job's process group, waits the job's kill grace and writes
// the interrupted receipt, so the caller only needs to wait for that receipt.
func Stop(root, job string, wait time.Duration) (string, error) {
	receipt, dir, err := state.LatestRunning(root, job)
	if err != nil {
		return "", err
	}
	if receipt == nil {
		return "", fmt.Errorf("job %q is not running", job)
	}
	record, err := state.ReadProcess(dir)
	if err != nil {
		return "", fmt.Errorf("the running record has no process information: %w", err)
	}
	if record.PSStart == "" {
		return "", fmt.Errorf("the running record has no process start time; refusing to signal pid %d", record.PID)
	}
	if !processAlive(record.PID) {
		return "", fmt.Errorf("job %q is marked running, but its runner is gone", job)
	}
	current, err := processStart(record.PID)
	if err != nil {
		return "", fmt.Errorf("cannot verify pid %d: %w", record.PID, err)
	}
	if current != record.PSStart {
		return "", fmt.Errorf("refusing to signal pid %d: it is no longer the recorded runner", record.PID)
	}
	if err = syscall.Kill(record.PID, syscall.SIGTERM); err != nil {
		return "", fmt.Errorf("cannot signal pid %d: %w", record.PID, err)
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		latest, err := state.Latest(root, job)
		if err != nil {
			return "", err
		}
		if latest == nil || latest.Status != "running" {
			status := "unknown"
			if latest != nil {
				status = latest.Status
			}
			return fmt.Sprintf("stopped job %q (%s)", job, status), nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", fmt.Errorf("sent SIGTERM to job %q, but it is still marked running after %s", job, wait)
}

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// processStart reads the process start time that `ps` reports. It is stored
// with the running receipt and compared again before a signal, so a reused pid
// is refused instead of stopped.
func processStart(pid int) (string, error) {
	out, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
