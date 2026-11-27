package schedule

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "regenerate scheduler golden files")

func TestRender(t *testing.T) {
	for _, tc := range []struct{ name, expr string }{{"daily", "daily 02:05"}, {"weekly", "weekly mon,thu,sun 23:45"}, {"every", "every 30m"}} {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := Parse(tc.expr)
			if err != nil {
				t.Fatal(err)
			}
			nice := int64(10)
			u := Unit{Name: "demo", Cwd: "/srv/project %n", Executable: "/home/local bin/jobctl", Config: `/srv/project %n/a&b "$x".toml`, StateDir: "/srv/project/state", Spec: spec, Nice: &nice, IOIdle: true}
			for _, render := range []func(Unit) ([]File, error){Systemd, Launchd} {
				files, err := render(u)
				if err != nil {
					t.Fatal(err)
				}
				for _, f := range files {
					path := filepath.Join("testdata", tc.name+filepath.Ext(f.Name))
					if *update {
						if err = os.WriteFile(path, []byte(f.Content), 0644); err != nil {
							t.Fatal(err)
						}
					}
					want, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					if string(want) != f.Content {
						t.Errorf("%s differs:\n%s", path, f.Content)
					}
				}
			}
		})
	}
}

func TestNext(t *testing.T) {
	loc := time.FixedZone("local", 3600)
	now := time.Date(2026, 11, 27, 16, 3, 10, 0, loc) // Friday
	for _, tc := range []struct {
		expr, want string
		last       time.Time
	}{
		{"daily 16:02", "2026-11-28 16:02:00", time.Time{}},
		{"weekly mon,thu 00:01", "2026-11-30 00:01:00", time.Time{}},
		{"every 30m", "2026-11-27 16:33:10", time.Time{}},
		{"every 10m", "2026-11-27 16:06:10", now.Add(-37 * time.Minute)},
	} {
		spec, err := Parse(tc.expr)
		if err != nil {
			t.Fatal(err)
		}
		if next := spec.Next(now, tc.last); next.Location() != loc || next.Format("2006-01-02 15:04:05") != tc.want {
			t.Errorf("%s: got %s want %s", tc.expr, next, tc.want)
		}
	}
	for _, expr := range []string{"every 0m", "every 2ms", "every 1.5h", "daily 2:00", "daily 00:60", "weekly fri,fri 01:00", "weekly x 02:00", "daily\n02:00", "0 2 * * *"} {
		if _, err := Parse(expr); err == nil {
			t.Errorf("accepted %q", expr)
		}
	}
}
