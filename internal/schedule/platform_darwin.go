//go:build darwin

package schedule

import (
	"os"
	"path/filepath"
	"strconv"
)

const platform = "launchd"
const platformDir = "Library/LaunchAgents"
const marker = LaunchdMarker

func render(u Unit) ([]File, error) { return Launchd(u) }
func platformFiles(name string) []File {
	return []File{{Name: "com.jobctl." + name + ".plist", GeneratedName: name + ".plist"}}
}
func domain() string             { return "gui/" + strconv.Itoa(os.Getuid()) }
func service(name string) string { return domain() + "/com.jobctl." + name }
func reload() error              { return nil }
func prepare(item *installation) error {
	if !item.installed || systemCommand("launchctl", "print", service(item.name)) != nil {
		return nil
	}
	if !item.changed {
		item.keepActive = true
		return nil
	}
	// If bootout fails, leave the installed file unchanged so a retry still
	// detects that the loaded job must be replaced.
	return deactivate(item.name)
}

func activate(dir string, all []installation) error {
	for _, item := range all {
		if item.keepActive {
			continue
		}
		if err := systemCommand("launchctl", "bootstrap", domain(), filepath.Join(dir, item.files[0].Name)); err != nil {
			return err
		}
	}
	return nil
}
func deactivate(name string) error { return systemCommand("launchctl", "bootout", service(name)) }
