package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ivolejon/jobctl/internal/state"
)

var binary string

func TestMain(m *testing.M) {
	os.Exit(func() int {
		dir, err := os.MkdirTemp("", "jobctl-test-")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer os.RemoveAll(dir)
		binary = filepath.Join(dir, "jobctl")
		build := exec.Command("go", "build", "-o", binary, ".")
		build.Env = append(os.Environ(), "CGO_ENABLED=0")
		build.Stdout, build.Stderr = os.Stdout, os.Stderr
		if err = build.Run(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return m.Run()
	}())
}

type sandbox struct{ dir, home, root, cfg, stub string }

func setup(t *testing.T, body string) sandbox {
	t.Helper()
	d := t.TempDir()
	s := sandbox{d, filepath.Join(d, "home"), filepath.Join(d, "state % with spaces"), filepath.Join(d, "job config.toml"), filepath.Join(d, "bin")}
	for _, dir := range []string{s.home, s.stub} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	write(t, s.cfg, body, 0600)
	return s
}
func write(t *testing.T, path, text string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
}
func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func (s sandbox) command(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = s.dir
	cmd.Env = append(os.Environ(), "HOME="+s.home, "JOBCTL_STATE_DIR="+s.root, "JOBCTL_CONFIG="+s.cfg, "XDG_CONFIG_HOME=", "XDG_STATE_HOME=", "STUB_LOG="+filepath.Join(s.dir, "calls"), "STUB_LOADED="+filepath.Join(s.dir, "loaded"), "PATH="+s.stub+":/usr/bin:/bin")
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 3 * time.Second
	return cmd
}
func exit(t *testing.T, err error, want int, out, diag string) {
	t.Helper()
	code := 0
	if err != nil {
		var e *exec.ExitError
		if !errors.As(err, &e) {
			t.Fatal(err)
		}
		code = e.ExitCode()
	}
	if code != want {
		t.Fatalf("code=%d want %d\nstdout: %s\nstderr: %s", code, want, out, diag)
	}
}
func (s sandbox) call(t *testing.T, code int, args ...string) (string, string) {
	t.Helper()
	var out, diag bytes.Buffer
	cmd := s.command(t, args...)
	cmd.Stdout, cmd.Stderr = &out, &diag
	err := cmd.Run()
	exit(t, err, code, out.String(), diag.String())
	return out.String(), diag.String()
}
func (s sandbox) receipt(t *testing.T, out string) state.Receipt {
	t.Helper()
	var r state.Receipt
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	saved := read(t, filepath.Join(s.root, "runs", r.Job, r.RunID, "meta.json"))
	var onDisk state.Receipt
	if err := json.Unmarshal([]byte(saved), &onDisk); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(onDisk)
	want, _ := json.Marshal(r)
	if !bytes.Equal(b, want) {
		t.Fatalf("receipt mismatch:\n%s\n%s", b, want)
	}
	return r
}
func eventually(t *testing.T, predicate func() bool, what string) {
	t.Helper()
	end := time.Now().Add(5 * time.Second)
	for time.Now().Before(end) {
		if predicate() {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("timed out waiting for " + what)
}
func start(t *testing.T, cmd *exec.Cmd) (done chan error, out, diag *bytes.Buffer) {
	t.Helper()
	out, diag = new(bytes.Buffer), new(bytes.Buffer)
	cmd.Stdout, cmd.Stderr = out, diag
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done = make(chan error, 1)
	finished := make(chan struct{})
	go func() { done <- cmd.Wait(); close(finished) }()
	t.Cleanup(func() { _ = cmd.Process.Signal(syscall.SIGTERM); <-finished })
	return
}

func TestRunAndStatus(t *testing.T) {
	s := setup(t, `[jobs.demo]
command = "printf 'out\\n'; printf 'err\\n' >&2; exit 17"
schedule = 'daily 02:00'
`)
	out, diag := s.call(t, 17, "run", "--trigger", "schedule", "demo", "--json")
	if diag != "" {
		t.Fatalf("unexpected stderr: %s", diag)
	}
	r := s.receipt(t, out)
	if r.Version != 1 || r.Job != "demo" || r.Cwd != s.dir || r.Status != "failed" || r.Trigger != "schedule" || r.Schedule != "daily 02:00" || r.ExitCode == nil || *r.ExitCode != 17 || r.Signal != nil || r.WaitedSeconds != 0 || r.EndedAt == nil || r.EndedAt.Before(r.StartedAt) || r.DurationSeconds <= 0 || r.Log != "output.log" {
		t.Fatalf("bad receipt: %s", out)
	}
	data, _ := s.call(t, 0, "logs", "demo")
	if data != "out\nerr\n" {
		t.Fatalf("bad log: %q", data)
	}
	path, _ := s.call(t, 0, "logs", "--path", "demo")
	logPath := strings.TrimSpace(path)
	for name, mode := range map[string]os.FileMode{logPath: 0600, filepath.Dir(logPath): 0700} {
		info, err := os.Stat(name)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("wrong mode: %s %v %v", name, info, err)
		}
	}
	if err := os.Remove(filepath.Join(filepath.Dir(filepath.Dir(logPath)), "latest")); err != nil {
		t.Fatal(err)
	}
	status, _ := s.call(t, 0, "status", "--json", "demo")
	if !strings.Contains(status, `"status":"failed"`) || strings.Contains(status, `"nextAt":null`) {
		t.Fatalf("bad status: %s", status)
	}
	list, _ := s.call(t, 0, "list", "--json")
	if !strings.Contains(list, `"locks":["demo"]`) {
		t.Fatalf("bad list: %s", list)
	}
}

func TestExplicitPathsAndJobExitCodes(t *testing.T) {
	s := setup(t, "[jobs.env-job]\ncommand = 'exit 1'\n")
	cfg := filepath.Join(s.dir, "explicit.toml")
	root := filepath.Join(s.dir, "explicit-state")
	write(t, cfg, `[jobs.job124]
command = 'exit 124'
[jobs.job126]
command = 'exit 126'
[jobs.signal]
command = 'kill -TERM $$'
`, 0600)
	for _, tc := range []struct {
		name string
		code int
	}{{"job124", 124}, {"job126", 126}, {"signal", 143}} {
		out, _ := s.call(t, tc.code, "--config", cfg, "run", "--state-dir", root, tc.name, "--json")
		var r state.Receipt
		if err := json.Unmarshal([]byte(out), &r); err != nil || r.Status != "failed" || r.Trigger != "manual" {
			t.Fatalf("unexpected receipt: %s (%v)", out, err)
		}
		if tc.name == "signal" {
			if r.ExitCode != nil || r.Signal == nil || *r.Signal != "SIGTERM" {
				t.Fatalf("wrong signal fields: %s", out)
			}
		} else if r.ExitCode == nil || *r.ExitCode != tc.code || r.Signal != nil {
			t.Fatalf("wrong exit fields: %s", out)
		}
	}
	if _, err := os.Stat(s.root); !os.IsNotExist(err) {
		t.Fatalf("environment state used despite override: %v", err)
	}
	s.call(t, 64, "run", "--wait-timeout", "0s", "env-job")
	s.call(t, 78, "validate", "--config", filepath.Join(s.dir, "not-found.toml"))
}

func TestDashPrefixedJob(t *testing.T) {
	s := setup(t, "[jobs.--json]\ncommand = 'true'\nschedule = 'every 20s'\n")
	out, _ := s.call(t, 0, "run", "--json", "--", "--json")
	if r := s.receipt(t, out); r.Job != "--json" || r.Status != "passed" {
		t.Fatalf("unexpected receipt: %s", out)
	}
	out, _ = s.call(t, 0, "install", "--print", "--", "--json")
	var want string
	if runtime.GOOS == "darwin" {
		want = "<string>--</string>\n    <string>--json</string>"
	} else {
		want = "--trigger schedule -- --json"
	}
	if !strings.Contains(out, want) {
		t.Fatalf("missing argument separator: %s", out)
	}
}

func TestDryRunAndEnv(t *testing.T) {
	s := setup(t, `[jobs.demo]
command = 'test "$INHERITED" = before && test "$OVERRIDE" = toml && test -n "$LITERAL" && touch ran'
env_file = 'env'
env = {OVERRIDE = 'toml'}
`)
	t.Setenv("INHERITED", "before")
	t.Setenv("OVERRIDE", "parent")
	write(t, filepath.Join(s.dir, "env"), "OVERRIDE=file\nLITERAL=$(touch should-not-exist)\n", 0600)
	out, diag := s.call(t, 0, "run", "demo", "--dry-run", "--json")
	if !strings.Contains(out, `"LITERAL"`) || !strings.Contains(out, `"INHERITED"`) || strings.Contains(out+diag, "should-not-exist") {
		t.Fatalf("bad dry-run: %s %s", out, diag)
	}
	for _, p := range []string{s.root, filepath.Join(s.dir, "ran")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("created %s", p)
		}
	}
	out, diag = s.call(t, 0, "run", "demo")
	if out != "" || !strings.Contains(diag, " passed ") {
		t.Fatalf("bad CLI output: %s %s", out, diag)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "ran")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "should-not-exist")); !os.IsNotExist(err) {
		t.Fatal("env_file was executed")
	}
}

func TestLocks(t *testing.T) {
	s := setup(t, `[jobs.holder]
command = 'touch ready; while [ ! -f release ]; do sleep 0.02; done'
lock = ['z', 'a']
timeout = '10s'
kill_grace = '100ms'
[jobs.other]
command = 'true'
lock = ['a', 'z']
`)
	hold, holdOut, holdDiag := start(t, s.command(t, "run", "holder"))
	eventually(t, func() bool { _, err := os.Stat(filepath.Join(s.dir, "ready")); return err == nil }, "lock holder")
	s.call(t, 126, "run", "other")
	s.call(t, 126, "run", "other", "--wait", "--wait-timeout=100ms")
	waiter, waitOut, waitDiag := start(t, s.command(t, "run", "--wait", "other", "--wait-timeout", "5s", "--json"))
	select {
	case err := <-waiter:
		t.Fatalf("waiter returned before release: %v: %s", err, waitDiag.String())
	case <-time.After(250 * time.Millisecond):
	}
	write(t, filepath.Join(s.dir, "release"), "", 0600)
	exit(t, <-hold, 0, holdOut.String(), holdDiag.String())
	exit(t, <-waiter, 0, waitOut.String(), waitDiag.String())
	r := s.receipt(t, waitOut.String())
	if r.Status != "passed" || r.WaitedSeconds <= 0 {
		t.Fatalf("didn't wait: %s", waitOut)
	}
	for _, n := range []string{"a", "z"} {
		if _, err := os.Stat(filepath.Join(s.root, "locks", n+".lock")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTimeoutAndSignal(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		t.Run(strconv.FormatBool(interrupted), func(t *testing.T) {
			limit, wantCode, wantStatus := "1s", 124, "timed_out"
			if interrupted {
				limit, wantCode, wantStatus = "15s", 143, "interrupted"
			}
			// A child ignores TERM; the shell must receive it, and KILL must target the group.
			s := setup(t, fmt.Sprintf(`[jobs.demo]
command = '''
trap 'printf term > received; exit 42' TERM
/bin/sh -c 'trap "" TERM; touch child-ready; while :; do sleep 60; done' &
printf '%%s %%s\n' "$$" "$!" > pids
wait
'''
timeout = '%s'
kill_grace = '120ms'
`, limit))
			cmd := s.command(t, "run", "demo", "--json")
			done, out, diag := start(t, cmd)
			var pgid, child int
			eventually(t, func() bool {
				data, _ := os.ReadFile(filepath.Join(s.dir, "pids"))
				n, _ := fmt.Sscan(string(data), &pgid, &child)
				_, err := os.Stat(filepath.Join(s.dir, "child-ready"))
				return n == 2 && err == nil
			}, "child process group")
			if interrupted {
				if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
			}
			exit(t, <-done, wantCode, out.String(), diag.String())
			r := s.receipt(t, out.String())
			if r.Status != wantStatus || r.ExitCode == nil || *r.ExitCode != 42 || r.Signal != nil {
				t.Fatalf("wrong receipt: %s", out)
			}
			if data := read(t, filepath.Join(s.dir, "received")); data != "term" {
				t.Fatalf("not forwarded: %s", data)
			}
			eventually(t, func() bool {
				return errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH) && errors.Is(syscall.Kill(child, 0), syscall.ESRCH)
			}, "killed process group (including descendant)")
		})
	}
}

func TestRetention(t *testing.T) {
	s := setup(t, "[jobs.demo]\ncommand = 'true'\nretention_days = 2\n")
	now := time.Now().UTC()
	dirs := map[string]bool{}
	for _, tc := range []struct {
		when   time.Time
		status string
		keep   bool
	}{{now.AddDate(0, 0, -3), "passed", false}, {now.AddDate(0, 0, -1), "passed", true}, {now.AddDate(0, 0, -5), "running", true}} {
		dir, id, err := state.CreateRun(s.root, "demo", tc.when)
		if err != nil {
			t.Fatal(err)
		}
		r := state.Receipt{Version: 1, Job: "demo", RunID: id, StartedAt: tc.when, Status: tc.status}
		if tc.status != "running" {
			r.EndedAt = &tc.when
		}
		if err = state.WriteReceipt(dir, &r); err != nil {
			t.Fatal(err)
		}
		dirs[dir] = tc.keep
	}
	out, _ := s.call(t, 0, "run", "demo", "--json")
	r := s.receipt(t, out)
	dirs[filepath.Join(s.root, "runs", "demo", r.RunID)] = true
	for p, keep := range dirs {
		_, err := os.Stat(p)
		if keep && err != nil || !keep && !os.IsNotExist(err) {
			t.Errorf("keep=%v %s: %v", keep, p, err)
		}
	}
}

func TestInstall(t *testing.T) {
	s := setup(t, "[jobs.demo]\ncommand = 'true'\nschedule = 'weekly mon,thu 02:05'\n")
	var bin, installed, generated, fragment, bootstrap, uninstall string
	switch runtime.GOOS {
	case "darwin":
		bin = "launchctl"
		installed = "Library/LaunchAgents/com.jobctl.demo.plist"
		generated = "launchd/demo.plist"
		fragment = "<key>Weekday</key><integer>4</integer>"
		prefix := "gui/" + strconv.Itoa(os.Getuid())
		bootstrap = "bootstrap " + prefix + " " + filepath.Join(s.home, installed)
		uninstall = "bootout " + prefix + "/com.jobctl.demo"
	case "linux":
		bin = "systemctl"
		installed = ".config/systemd/user/jobctl-demo.timer"
		generated = "systemd/demo.timer"
		fragment = "OnCalendar=Thu *-*-* 02:05:00"
		bootstrap = "--user enable --now jobctl-demo.timer"
		uninstall = "--user disable --now jobctl-demo.timer"
	default:
		t.Skip("unsupported scheduler")
	}
	write(t, filepath.Join(s.stub, bin), `#!/bin/sh
printf '%s\n' "$*" >> "$STUB_LOG"
if [ "$STUB_FAIL" = yes ]; then echo 'stub activation error' >&2; exit 2; fi
case "$1" in
  print) test -f "$STUB_LOADED" ;;
  bootstrap) touch "$STUB_LOADED" ;;
  bootout) rm -f "$STUB_LOADED" ;;
  *) exit 0 ;;
esac
`, 0700)
	printed, _ := s.call(t, 0, "install", "demo", "--print")
	if !strings.Contains(printed, fragment) || !strings.Contains(printed, s.cfg) || !strings.Contains(printed, binary) || !strings.Contains(printed, "--state-dir") || !strings.Contains(printed, "Generated by jobctl") {
		t.Fatalf("bad rendering:\n%s", printed)
	}
	if _, err := os.Stat(s.root); !os.IsNotExist(err) {
		t.Fatalf("--print touched state: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "calls")); !os.IsNotExist(err) {
		t.Fatalf("--print called scheduler: %v", err)
	}
	if files, _ := os.ReadDir(s.home); len(files) != 0 {
		t.Fatal("--print touched HOME")
	}
	installed = filepath.Join(s.home, installed)
	generated = filepath.Join(s.root, "generated", generated)
	if err := os.MkdirAll(filepath.Dir(installed), 0700); err != nil {
		t.Fatal(err)
	}
	write(t, installed, "not owned", 0600)
	s.call(t, 70, "install", "demo")
	if read(t, installed) != "not owned" {
		t.Fatal("overwrote unmarked file")
	}
	_, diag := s.call(t, 70, "uninstall", "demo")
	if !strings.Contains(diag, "unmarked") {
		t.Fatalf("bad diagnostic: %s", diag)
	}
	t.Setenv("STUB_FAIL", "yes")
	_, diag = s.call(t, 70, "install", "--force", "demo")
	if !strings.Contains(diag, "stub activation error") {
		t.Fatalf("bad diagnostic: %s", diag)
	}
	t.Setenv("STUB_FAIL", "no")
	s.call(t, 0, "install", "demo")
	data := read(t, installed)
	if data != read(t, generated) || !strings.Contains(printed, data) {
		t.Fatal("installed/copied data mismatch")
	}
	s.call(t, 0, "install", "demo")
	if data != read(t, installed) {
		t.Fatal("reinstall changed bytes")
	}
	s.call(t, 0, "uninstall")
	s.call(t, 0, "uninstall", "demo")
	for _, p := range []string{installed, generated} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("uninstall left %s", p)
		}
	}
	calls := read(t, filepath.Join(s.dir, "calls"))
	if !strings.Contains(calls, bootstrap+"\n") || !strings.Contains(calls, uninstall+"\n") {
		t.Fatalf("unexpected scheduler calls:\n%s", calls)
	}
}
