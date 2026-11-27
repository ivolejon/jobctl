package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "jobs.toml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestErrorsWithLocations(t *testing.T) {
	path := fixture(t, `unknown = 1
[jobs."broken"]
"unexpected" = true
schedule = "daily 25:00"
timeout = 'soon'
[jobs.other.env]
VALUE = true
`)
	err := Validate(path)
	if err == nil {
		t.Fatal("invalid TOML accepted")
	}
	want := map[int]string{1: "unknown field", 2: "command: required", 3: "unexpected: unknown field", 4: "schedule:", 5: "timeout:", 6: "command: required", 7: "env:"}
	for line, fragment := range want {
		found := false
		for _, s := range strings.Split(err.Error(), "\n") {
			if strings.HasPrefix(s, fmt.Sprintf("%s:%d:", path, line)) && strings.Contains(s, fragment) {
				found = true
			}
		}
		if !found {
			t.Errorf("missing line %d (%s) in:\n%s", line, fragment, err)
		}
	}
}

func TestInvalidInlineAndSyntax(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{`jobs = { demo = { command = "ok", TIMEOUT = "1s" } }`, "TIMEOUT: unknown field"},
		{"[Jobs.demo]\ncommand = 'ok'", `unknown field "Jobs"`},
		{"[jobs.demo]\ncommand = ???", "invalid TOML"},
	} {
		path := fixture(t, tc.input)
		if err := Validate(path); err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), path+":") {
			t.Fatalf("got %v; want %q", err, tc.want)
		}
	}
}

func TestDefaultsAndEnvFile(t *testing.T) {
	path := fixture(t, "[jobs.demo]\ncommand = 'exit 0'\nenv_file = '~/environment'\nenv = {OVERRIDE = 'from-toml'}\n")
	dir := filepath.Dir(path)
	t.Setenv("HOME", dir)
	envPath := filepath.Join(dir, "environment")
	if err := os.WriteFile(envPath, []byte("# comment\n\nOVERRIDE=from-file\nLITERAL=$(touch unwanted) # text\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	j := cfg.Jobs["demo"]
	if j.Cwd != dir || j.Timeout != time.Hour || j.KillGrace != 30*time.Second || j.RetentionDays != 30 || !j.Enabled || len(j.Locks) != 1 || j.Locks[0] != "demo" {
		t.Fatalf("bad defaults: %+v", j)
	}
	env, err := cfg.Environment(j)
	if err != nil {
		t.Fatal(err)
	}
	if env["OVERRIDE"] != "from-toml" || env["LITERAL"] != "$(touch unwanted) # text" {
		t.Fatalf("bad env parsing: %v", env)
	}
	if err = os.WriteFile(envPath, []byte("private-value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = Validate(path); err == nil || !strings.Contains(err.Error(), path+":3:") || !strings.Contains(err.Error(), envPath+":1:") || strings.Contains(err.Error(), "private-value") {
		t.Fatalf("bad env-file error: %v", err)
	}
}
