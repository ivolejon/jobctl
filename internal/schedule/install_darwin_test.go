//go:build darwin

package schedule

import (
	"os"
	"path/filepath"
	"testing"
)

// A plist installed before the XML declaration was added still carries the old
// marker. Reinstalling or uninstalling it must not require --force.
func TestCheckAcceptsTheLegacyLaunchdMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "com.jobctl.demo.plist")
	content := LegacyLaunchdMarker + "<plist version=\"1.0\"></plist>\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := check(path, false)
	if err != nil || data == nil {
		t.Fatalf("legacy marker rejected: %v", err)
	}
}
