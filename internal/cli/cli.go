package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ivolejon/jobctl/internal/config"
	"github.com/ivolejon/jobctl/internal/run"
	"github.com/ivolejon/jobctl/internal/schedule"
	"github.com/ivolejon/jobctl/internal/state"
)

const usage = "Usage: jobctl [--config PATH] [--state-dir PATH] {list|validate|run|stop|status|logs|install|uninstall|version}"
const usageError, configError = 64, 78

type Build struct{ Version, Commit string }
type app struct {
	out, diag io.Writer
	opts      options
	cfg       *config.Config
	root      string
}

func Main(args []string, out, diag io.Writer, build Build) int {
	a := app{out: out, diag: diag}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(diag, usage)
		return 0
	}
	var err error
	if a.opts, err = parse(args); err != nil {
		fmt.Fprintln(diag, usage)
		return a.fail(usageError, err)
	}
	if a.opts.command == "version" {
		return a.result(version(out, build))
	}
	// Stopping a job reads only the state directory. It does not need the
	// config, so a job that was removed from jobs.toml can still be stopped.
	if a.opts.command == "stop" {
		name := a.opts.names[0]
		if !config.Name.MatchString(name) {
			return a.fail(usageError, fmt.Errorf("invalid job name %q", name))
		}
		root, err := state.Resolve(a.opts.stateDir)
		if err != nil {
			return a.fail(run.Internal, err)
		}
		message, err := run.Stop(root, name, 30*time.Second)
		if err != nil {
			return a.fail(1, err)
		}
		fmt.Fprintln(a.diag, message)
		return 0
	}
	// Uninstalling a named job does not read the config: a job that was removed
	// from jobs.toml still has units that must be cleaned up. Without names,
	// uninstall uses the jobs in the current config.
	if a.opts.command == "uninstall" && len(a.opts.names) > 0 {
		for _, name := range a.opts.names {
			if !config.Name.MatchString(name) {
				return a.fail(usageError, fmt.Errorf("invalid job name %q", name))
			}
		}
		root, err := state.Resolve(a.opts.stateDir)
		if err != nil {
			return a.fail(run.Internal, err)
		}
		return a.result(schedule.Uninstall(root, normalizeNames(a.opts.names)))
	}
	path, err := config.Find(a.opts.config)
	if err != nil {
		return a.fail(configError, err)
	}
	if a.opts.command == "validate" {
		if err = config.Validate(path); err != nil {
			return a.fail(configError, err)
		}
		fmt.Fprintf(diag, "Configuration valid: %s\n", path)
		return 0
	}
	if a.cfg, err = config.Load(path); err != nil {
		return a.fail(configError, err)
	}
	if a.root, err = state.Resolve(a.opts.stateDir); err != nil {
		return a.fail(run.Internal, err)
	}
	for _, name := range a.opts.names {
		if !config.Name.MatchString(name) || a.cfg.Jobs[name] == nil {
			return a.fail(usageError, fmt.Errorf("unknown job %q", name))
		}
	}
	switch a.opts.command {
	case "run":
		return a.run()
	case "list", "status":
		return a.result(a.report(a.opts.command == "list"))
	case "logs":
		return a.result(a.logs())
	case "install":
		return a.install()
	case "uninstall":
		return a.result(schedule.Uninstall(a.root, a.names()))
	}
	return a.fail(run.Internal, fmt.Errorf("unhandled command"))
}

func (a app) fail(code int, err error) int {
	if err != nil {
		fmt.Fprintln(a.diag, err)
	}
	return code
}
func (a app) result(err error) int {
	if err != nil {
		return a.fail(run.Internal, err)
	}
	return 0
}
func (a app) names() []string {
	if len(a.opts.names) == 0 {
		return config.Keys(a.cfg.Jobs)
	}
	return normalizeNames(a.opts.names)
}

func normalizeNames(names []string) []string {
	out := slices.Clone(names)
	slices.Sort(out)
	return slices.Compact(out)
}
func (a app) run() int {
	o := a.opts
	j := a.cfg.Jobs[o.names[0]]
	if !j.Enabled && o.trigger == "schedule" {
		return a.fail(configError, fmt.Errorf("job %q is disabled for scheduled runs", j.Name))
	}
	env, err := a.cfg.Environment(j)
	if err != nil {
		return a.fail(configError, err)
	}
	if o.dryRun {
		info := run.PreviewJob(j, env)
		if o.json {
			return a.result(json.NewEncoder(a.out).Encode(info))
		}
		_, err = fmt.Fprintf(a.diag, "command: %q\ncwd: %q\nenv names: %v\ntimeout: %s\nlocks: %v\n", info.Args, info.Cwd, info.EnvNames, info.Timeout, info.Locks)
		return a.result(err)
	}
	r, code, err := run.Execute(a.root, j, env, run.Options{Wait: o.wait, WaitTimeout: o.waitTimeout, Trigger: o.trigger})
	if r != nil {
		if o.json {
			if e := json.NewEncoder(a.out).Encode(r); e != nil {
				return a.fail(run.Internal, errors.Join(err, e))
			}
		} else {
			fmt.Fprintf(a.diag, "%s %s %s\n", r.RunID, r.Status, a.logPath(r))
		}
	}
	return a.fail(code, err)
}
func (a app) logPath(r *state.Receipt) string {
	return filepath.Join(state.Runs(a.root, r.Job), r.RunID, state.LogName)
}
func (a app) logs() error {
	name := a.opts.names[0]
	r, err := state.Latest(a.root, name)
	if err != nil {
		return err
	}
	if r == nil {
		return fmt.Errorf("no runs for %s", name)
	}
	path := a.logPath(r)
	if a.opts.path {
		_, err = fmt.Fprintln(a.out, path)
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(a.out, f)
	return err
}

type summary struct {
	Name            string     `json:"name"`
	Schedule        string     `json:"schedule"`
	Locks           []string   `json:"locks"`
	Enabled         bool       `json:"enabled"`
	Status          string     `json:"status"`
	StartedAt       *time.Time `json:"startedAt"`
	DurationSeconds *float64   `json:"durationSeconds"`
	NextAt          *time.Time `json:"nextAt"`
}

func (a app) report(list bool) error {
	rows := make([]summary, 0, len(a.cfg.Jobs))
	now := time.Now()
	for _, name := range a.names() {
		j := a.cfg.Jobs[name]
		row := summary{Name: name, Schedule: j.Schedule, Locks: append([]string{}, j.Locks...), Enabled: j.Enabled, Status: "never"}
		r, err := state.Latest(a.root, name)
		if err != nil {
			return err
		}
		var last time.Time
		if r != nil {
			row.Status = r.Status
			row.StartedAt = &r.StartedAt
			last = r.StartedAt
			if r.EndedAt != nil {
				row.DurationSeconds = &r.DurationSeconds
			}
		}
		if j.Enabled && j.Schedule != "" {
			if t := j.Spec.Next(now, last); !t.IsZero() {
				row.NextAt = &t
			}
		}
		rows = append(rows, row)
	}
	if a.opts.json {
		return json.NewEncoder(a.out).Encode(rows)
	}
	var output strings.Builder
	table := tabwriter.NewWriter(&output, 0, 4, 2, ' ', 0)
	if list {
		fmt.Fprintln(table, "NAME\tSCHEDULE\tLOCKS\tSTATUS\tENABLED")
		for _, r := range rows {
			fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%t\n", r.Name, orDash(r.Schedule), orDash(strings.Join(r.Locks, ",")), r.Status, r.Enabled)
		}
	} else {
		fmt.Fprintln(table, "NAME\tSTATUS\tDURATION\tSTARTED\tNEXT (EST.)")
		for _, r := range rows {
			d := "-"
			if r.DurationSeconds != nil {
				d = fmt.Sprintf("%.3fs", *r.DurationSeconds)
			}
			fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", r.Name, r.Status, d, formatTime(r.StartedAt), formatTime(r.NextAt))
		}
	}
	if err := table.Flush(); err != nil {
		return err
	}
	_, err := io.WriteString(a.out, output.String())
	return err
}
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
func formatTime(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Local().Format(time.RFC3339)
}

func (a app) install() int {
	executable, err := os.Executable()
	if err != nil {
		return a.fail(run.Internal, err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return a.fail(run.Internal, err)
	}
	var units []schedule.Unit
	var errs []error
	for _, name := range a.names() {
		j := a.cfg.Jobs[name]
		if !j.Enabled {
			if len(a.opts.names) > 0 {
				errs = append(errs, fmt.Errorf("job %q is disabled", name))
			}
			continue
		}
		if j.Schedule == "" {
			errs = append(errs, fmt.Errorf("job %q requires schedule for install", name))
			continue
		}
		units = append(units, schedule.Unit{Name: name, Cwd: j.Cwd, Executable: executable, Config: a.cfg.Path, StateDir: a.root, Spec: j.Spec, Nice: j.Nice, IOIdle: j.IOIdle})
	}
	if len(errs) > 0 {
		return a.fail(configError, errors.Join(errs...))
	}
	var print io.Writer
	if a.opts.print {
		print = a.out
	}
	return a.result(schedule.Install(a.root, units, print, a.opts.force, state.AtomicWrite))
}

func version(out io.Writer, b Build) error {
	if info, ok := debug.ReadBuildInfo(); ok {
		if b.Version == "" && info.Main.Version != "" && info.Main.Version != "(devel)" {
			b.Version = info.Main.Version
		}
		if b.Commit == "" {
			for _, s := range info.Settings {
				if s.Key == "vcs.revision" {
					b.Commit = s.Value
					break
				}
			}
		}
	}
	if b.Version == "" {
		b.Version = "dev"
	}
	if b.Commit == "" {
		b.Commit = "unknown"
	}
	_, err := fmt.Fprintf(out, "jobctl %s (commit %s) %s %s/%s\n", b.Version, b.Commit, runtime.Version(), runtime.GOOS, runtime.GOARCH)
	return err
}
