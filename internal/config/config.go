package config

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ivolejon/jobctl/internal/schedule"
	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

var Name = regexp.MustCompile(`^[a-z0-9-]+$`)
var envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type Job struct {
	Name, Command, Cwd, Schedule, Description string
	Spec                                      schedule.Spec
	Timeout, KillGrace                        time.Duration
	Locks                                     []string
	Env                                       map[string]string
	EnvFile                                   string
	envFileLine                               int
	RetentionDays                             int64
	Enabled, IOIdle                           bool
	Nice                                      *int64
}

type Config struct {
	Path string
	Jobs map[string]*Job
}

func Keys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func Resolve(path, base string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/"))
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	return filepath.Abs(path)
}

func Find(path string) (string, error) {
	if path == "" {
		path = os.Getenv("JOBCTL_CONFIG")
	}
	if path == "" {
		if _, err := os.Lstat("jobs.toml"); !errors.Is(err, os.ErrNotExist) {
			path = "jobs.toml"
		} else {
			base := os.Getenv("XDG_CONFIG_HOME")
			if base == "" {
				base = "~/.config"
			}
			path = filepath.Join(base, "jobctl", "jobs.toml")
		}
	}
	return Resolve(path, "")
}

func Load(path string) (*Config, error) { return load(path, false) }

func Validate(path string) error { _, err := load(path, true); return err }

func load(path string, checkEnv bool) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s:1: cannot read configuration: %w", path, err)
	}
	// Decode open job tables so type and semantic errors can be reported together.
	// The strict decoder checks the closed root schema; validateJob closes each job table.
	var root struct {
		Jobs any `toml:"jobs"`
	}
	err = toml.NewDecoder(bytes.NewReader(raw)).DisallowUnknownFields().Decode(&root)
	var errs []error
	var unknown *toml.StrictMissingError
	var decode *toml.DecodeError
	switch {
	case errors.As(err, &unknown):
		for _, e := range unknown.Errors {
			line, _ := e.Position()
			errs = append(errs, fmt.Errorf("%s:%d: unknown field %q", path, line, strings.Join(e.Key(), ".")))
		}
	case errors.As(err, &decode):
		line, _ := decode.Position()
		return nil, fmt.Errorf("%s:%d: invalid TOML (syntax error or conflicting keys)", path, line)
	case err != nil:
		return nil, fmt.Errorf("%s:1: invalid TOML", path)
	}
	loc, roots := locations(raw)
	for _, key := range Keys(roots) {
		// The TOML decoder also accepts case-insensitive struct tags.
		if key != "jobs" && strings.EqualFold(key, "jobs") {
			errs = append(errs, fmt.Errorf("%s:%d: unknown field %q", path, roots[key], key))
		}
	}
	report := func(key []string, msg string) {
		for len(key) > 0 {
			if line := loc[pathKey(key)]; line != 0 {
				errs = append(errs, fmt.Errorf("%s:%d: %s", path, line, msg))
				return
			}
			key = key[:len(key)-1]
		}
		errs = append(errs, fmt.Errorf("%s:1: %s", path, msg))
	}
	cfg := &Config{Path: path, Jobs: map[string]*Job{}}
	if root.Jobs != nil {
		if jobs, ok := root.Jobs.(map[string]any); ok {
			for _, name := range Keys(jobs) {
				prefix := []string{"jobs", name}
				if !Name.MatchString(name) {
					report(prefix, fmt.Sprintf("invalid job name %q", name))
				}
				fields, ok := jobs[name].(map[string]any)
				if !ok {
					report(prefix, fmt.Sprintf("job %q must be a table", name))
					continue
				}
				j := validateJob(name, filepath.Dir(path), fields, func(field []string, msg string) {
					report(append(slices.Clone(prefix), field...), fmt.Sprintf("job %q: %s", name, msg))
				})
				j.envFileLine = loc[pathKey([]string{"jobs", name, "env_file"})]
				cfg.Jobs[name] = j
				if checkEnv {
					if _, err := cfg.Environment(j); err != nil {
						errs = append(errs, err)
					}
				}
			}
		} else {
			report([]string{"jobs"}, "jobs must be a table")
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return cfg, nil
}

func validateJob(name, base string, fields map[string]any, report func([]string, string)) *Job {
	j := &Job{Name: name, Cwd: base, Timeout: time.Hour, KillGrace: 30 * time.Second, RetentionDays: 30, Enabled: true, Locks: []string{name}, Env: map[string]string{}}
	bad := func(k, msg string) { report([]string{k}, k+": "+msg) }
	if _, ok := fields["command"]; !ok {
		bad("command", "required")
	}
	for _, k := range Keys(fields) {
		value := fields[k]
		str := func(set func(string)) {
			if s, ok := value.(string); ok && !strings.ContainsRune(s, 0) {
				set(s)
			} else {
				bad(k, "expected a string without NUL")
			}
		}
		boolean := func(dest *bool) {
			if b, ok := value.(bool); ok {
				*dest = b
			} else {
				bad(k, "expected a boolean")
			}
		}
		duration := func(dest *time.Duration) {
			str(func(s string) {
				d, err := time.ParseDuration(s)
				if err != nil || d <= 0 {
					bad(k, "expected a positive duration")
				} else {
					*dest = d
				}
			})
		}
		switch k {
		case "command":
			str(func(s string) {
				if strings.TrimSpace(s) == "" {
					bad(k, "must not be empty")
				}
				j.Command = s
			})
		case "description":
			str(func(s string) { j.Description = s })
		case "cwd":
			str(func(s string) {
				var err error
				j.Cwd, err = Resolve(s, base)
				if err == nil {
					var info os.FileInfo
					info, err = os.Stat(j.Cwd)
					if err == nil && info.IsDir() {
						return
					}
				}
				bad(k, "must be an existing directory")
			})
		case "schedule":
			str(func(s string) {
				spec, err := schedule.Parse(s)
				if err != nil {
					bad(k, err.Error())
				} else {
					j.Schedule, j.Spec = s, spec
				}
			})
		case "timeout":
			duration(&j.Timeout)
		case "kill_grace":
			duration(&j.KillGrace)
		case "enabled":
			boolean(&j.Enabled)
		case "io_idle":
			boolean(&j.IOIdle)
		case "nice":
			if n, ok := value.(int64); ok {
				j.Nice = &n
			} else {
				bad(k, "expected an integer")
			}
		case "retention_days":
			if n, ok := value.(int64); ok && n >= 0 {
				j.RetentionDays = n
			} else {
				bad(k, "expected a non-negative integer")
			}
		case "lock":
			j.Locks = nil
			add := func(v any) {
				if s, ok := v.(string); ok && Name.MatchString(s) {
					j.Locks = append(j.Locks, s)
				} else {
					bad(k, "lock names must match [a-z0-9-]+")
				}
			}
			switch v := value.(type) {
			case string:
				add(v)
			case []any:
				for _, e := range v {
					add(e)
				}
			default:
				bad(k, "expected a string or an array of strings")
			}
			slices.Sort(j.Locks)
			j.Locks = slices.Compact(j.Locks)
		case "env":
			if m, ok := value.(map[string]any); ok {
				for _, key := range Keys(m) {
					if s, ok := m[key].(string); ok && envKey.MatchString(key) && !strings.ContainsRune(s, 0) {
						j.Env[key] = s
					} else {
						report([]string{k, key}, "env: expected KEY = string without NUL")
					}
				}
			} else {
				bad(k, "expected a table")
			}
		case "env_file":
			str(func(s string) {
				var err error
				if s != "" {
					j.EnvFile, err = Resolve(s, base)
				}
				if s == "" || err != nil {
					bad(k, "expected a file path")
				}
			})
		default:
			bad(k, "unknown field")
		}
	}
	return j
}

func (c *Config) Environment(j *Job) (map[string]string, error) {
	env, err := j.Environment()
	if err != nil {
		return nil, fmt.Errorf("%s:%d: job %q: env_file: %w", c.Path, max(1, j.envFileLine), j.Name, err)
	}
	return env, nil
}

// Env-file values are literal, including whitespace and quotes. Never evaluate them.
func (j *Job) Environment() (map[string]string, error) {
	env := map[string]string{}
	if j.EnvFile != "" {
		info, err := os.Stat(j.EnvFile)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%s: expected a regular file", j.EnvFile)
		}
		f, err := os.Open(j.EnvFile)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for n := 1; scanner.Scan(); n++ {
			text := scanner.Text()
			trim := strings.TrimSpace(text)
			if trim == "" || strings.HasPrefix(trim, "#") {
				continue
			}
			k, v, ok := strings.Cut(text, "=")
			if !ok || !envKey.MatchString(k) || strings.ContainsRune(v, 0) {
				return nil, fmt.Errorf("%s:%d: expected KEY=VALUE", j.EnvFile, n)
			}
			env[k] = v
		}
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("%s: %w", j.EnvFile, err)
		}
	}
	for k, v := range j.Env {
		env[k] = v
	}
	return env, nil
}

func pathKey(parts []string) string { return fmt.Sprintf("%q", parts) }

// Reuse the TOML parser's key spans, including quoted/dotted keys and inline tables.
func locations(raw []byte) (map[string]int, map[string]int) {
	var p unstable.Parser
	p.Reset(raw)
	out := map[string]int{}
	roots := map[string]int{}
	var table []string
	var newlines []int
	for i, c := range raw {
		if c == '\n' {
			newlines = append(newlines, i)
		}
	}
	keys := func(n *unstable.Node) ([]string, []int) {
		var parts []string
		var lines []int
		for it := n.Key(); it.Next(); {
			k := it.Node()
			parts = append(parts, string(k.Data))
			lines = append(lines, 1+sort.SearchInts(newlines, int(k.Raw.Offset)))
		}
		return parts, lines
	}
	record := func(prefix, parts []string, lines []int) []string {
		if len(prefix) == 0 && roots[parts[0]] == 0 {
			roots[parts[0]] = lines[0]
		}
		full := slices.Clone(prefix)
		for i, k := range parts {
			full = append(full, k)
			key := pathKey(full)
			if out[key] == 0 {
				out[key] = lines[i]
			}
		}
		return full
	}
	var visit func([]string, *unstable.Node)
	visit = func(prefix []string, n *unstable.Node) {
		parts, lines := keys(n)
		full := record(prefix, parts, lines)
		if n.Value().Kind == unstable.InlineTable {
			for it := n.Value().Children(); it.Next(); {
				visit(full, it.Node())
			}
		}
	}
	for p.NextExpression() {
		n := p.Expression()
		switch n.Kind {
		case unstable.Table, unstable.ArrayTable:
			parts, lines := keys(n)
			table = record(nil, parts, lines)
		case unstable.KeyValue:
			visit(table, n)
		}
	}
	return out, roots
}
