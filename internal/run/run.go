//go:build linux || darwin

package run

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ivolejon/jobctl/internal/config"
	"github.com/ivolejon/jobctl/internal/state"
)

const (
	Timeout     = 124
	StartFailed = 125
	Busy        = 126
	Internal    = 70
)

type Options struct {
	Wait        bool
	WaitTimeout time.Duration
	Trigger     string
}
type Preview struct {
	Args     []string `json:"args"`
	Cwd      string   `json:"cwd"`
	EnvNames []string `json:"envNames"`
	Timeout  string   `json:"timeout"`
	Locks    []string `json:"locks"`
}

func PreviewJob(j *config.Job, env map[string]string) Preview {
	names := map[string]struct{}{}
	for _, entry := range os.Environ() {
		if k, _, ok := strings.Cut(entry, "="); ok {
			names[k] = struct{}{}
		}
	}
	for k := range env {
		names[k] = struct{}{}
	}
	return Preview{[]string{"/bin/sh", "-c", j.Command}, j.Cwd, config.Keys(names), j.Timeout.String(), append([]string{}, lockOrder(j.Locks)...)}
}

func Execute(root string, j *config.Job, env map[string]string, opts Options) (*state.Receipt, int, error) {
	stop := make(chan os.Signal, 2)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(stop)
	release, waited, caught, err := acquire(root, j.Locks, opts.Wait, opts.WaitTimeout, stop)
	if caught != nil {
		return nil, signalCode(caught), fmt.Errorf("interrupted by %s", signalName(caught.(syscall.Signal)))
	}
	if errors.Is(err, errBusy) {
		return nil, Busy, err
	}
	if err != nil {
		return nil, Internal, err
	}
	defer release()
	select {
	case caught = <-stop:
		return nil, signalCode(caught), fmt.Errorf("interrupted by %s", signalName(caught.(syscall.Signal)))
	default:
	}
	started := time.Now()
	dir, id, err := state.CreateRun(root, j.Name, started)
	if err != nil {
		return nil, Internal, err
	}
	// Write the process record before the running receipt. A reader that sees
	// the running status must be able to stop the run it names.
	psStart, _ := processStart(os.Getpid())
	if err = state.WriteProcess(dir, os.Getpid(), started, psStart); err != nil {
		_ = os.RemoveAll(dir)
		return nil, Internal, err
	}
	r := &state.Receipt{Version: 1, Job: j.Name, RunID: id, Trigger: opts.Trigger, Status: "running", Command: j.Command, Cwd: j.Cwd, Schedule: j.Schedule, StartedAt: started.UTC(), WaitedSeconds: waited.Seconds(), Log: state.LogName}
	if err = state.WriteReceipt(dir, r); err != nil {
		return nil, Internal, err
	}
	state.PublishLatest(root, j.Name)
	code, runErr := executeProcess(j, env, r, dir, stop)
	ended := time.Now()
	utc := ended.UTC()
	r.EndedAt = &utc
	r.DurationSeconds = ended.Sub(started).Seconds()
	if err = state.WriteReceipt(dir, r); err != nil {
		return r, Internal, errors.Join(runErr, err)
	}
	state.Prune(root, j.Name, id, j.RetentionDays, ended.UTC())
	state.PublishLatest(root, j.Name)
	return r, code, runErr
}

func executeProcess(j *config.Job, env map[string]string, r *state.Receipt, dir string, stop <-chan os.Signal) (int, error) {
	log, err := os.OpenFile(filepath.Join(dir, state.LogName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		r.Status = "start_failed"
		return Internal, err
	}
	defer log.Close()
	cmd := exec.Command("/bin/sh", "-c", j.Command)
	cmd.Dir = j.Cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = cmd.Environ()
	for _, k := range config.Keys(env) {
		cmd.Env = append(cmd.Env, k+"="+env[k])
	}
	cmd.Stdout, cmd.Stderr = log, log
	select {
	case sig := <-stop:
		r.Status = "interrupted"
		return signalCode(sig), nil
	default:
	}
	if err = cmd.Start(); err != nil {
		r.Status = "start_failed"
		return StartFailed, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(j.Timeout)
	defer timer.Stop()
	var waitErr error
	var code int
	select {
	case waitErr = <-done:
		// A signal may arrive as the group exits naturally.
		select {
		case sig := <-stop:
			r.Status = "interrupted"
			code = signalCode(sig)
			shutdown(cmd.Process.Pid, sig.(syscall.Signal), j.KillGrace, nil)
		default:
		}
	case <-timer.C:
		r.Status = "timed_out"
		code = Timeout
		waitErr = shutdown(cmd.Process.Pid, syscall.SIGTERM, j.KillGrace, done)
	case sig := <-stop:
		r.Status = "interrupted"
		code = signalCode(sig)
		waitErr = shutdown(cmd.Process.Pid, sig.(syscall.Signal), j.KillGrace, done)
	}
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok {
		if ws.Signaled() {
			sig := signalName(ws.Signal())
			r.Signal = &sig
		} else if ws.Exited() {
			n := ws.ExitStatus()
			r.ExitCode = &n
		}
	}
	if code != 0 {
		return code, nil
	}
	if waitErr == nil {
		r.Status = "passed"
		return 0, nil
	}
	r.Status = "failed"
	if r.ExitCode != nil {
		return *r.ExitCode, nil
	}
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal()), nil
	}
	return Internal, waitErr
}

// A shell can die before its children. Keep monitoring the group until it
// disappears or grace expires, even after Wait has reaped the shell.
func shutdown(pid int, sig syscall.Signal, grace time.Duration, done <-chan error) (err error) {
	_ = syscall.Kill(-pid, sig)
	timer := time.NewTimer(grace)
	defer timer.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if done == nil && errors.Is(syscall.Kill(-pid, 0), syscall.ESRCH) {
			return
		}
		select {
		case err = <-done:
			done = nil
		case <-ticker.C:
		case <-timer.C:
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			if done != nil {
				err = <-done
			}
			return
		}
	}
}

func signalCode(sig os.Signal) int { return 128 + int(sig.(syscall.Signal)) }
func signalName(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGKILL:
		return "SIGKILL"
	case syscall.SIGHUP:
		return "SIGHUP"
	case syscall.SIGQUIT:
		return "SIGQUIT"
	case syscall.SIGABRT:
		return "SIGABRT"
	case syscall.SIGSEGV:
		return "SIGSEGV"
	case syscall.SIGPIPE:
		return "SIGPIPE"
	default:
		return fmt.Sprintf("SIG%d", sig)
	}
}
