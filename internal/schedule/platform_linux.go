//go:build linux

package schedule

const platform = "systemd"
const platformDir = ".config/systemd/user"
const marker = SystemdMarker
const legacyMarker = ""

func render(u Unit) ([]File, error) { return Systemd(u) }
func platformFiles(name string) []File {
	return []File{{Name: "jobctl-" + name + ".service", GeneratedName: name + ".service"}, {Name: "jobctl-" + name + ".timer", GeneratedName: name + ".timer"}}
}
func reload() error               { return systemCommand("systemctl", "--user", "daemon-reload") }
func prepare(*installation) error { return nil }
func activate(_ string, all []installation) error {
	if err := reload(); err != nil {
		return err
	}
	for _, item := range all {
		timer := "jobctl-" + item.name + ".timer"
		if err := systemCommand("systemctl", "--user", "enable", "--now", timer); err != nil {
			return err
		}
		if item.installed && item.changed {
			if err := systemCommand("systemctl", "--user", "restart", timer); err != nil {
				return err
			}
		}
	}
	return nil
}
func deactivate(name string) error {
	return systemCommand("systemctl", "--user", "disable", "--now", "jobctl-"+name+".timer")
}
