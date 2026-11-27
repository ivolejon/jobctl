package cli

import (
	"fmt"
	"strings"
	"time"
)

type options struct {
	command, config, stateDir, trigger     string
	names                                  []string
	json, wait, dryRun, print, force, path bool
	waitTimeout                            time.Duration
}

func parse(args []string) (options, error) {
	o := options{trigger: "manual", waitTimeout: 30 * time.Minute}
	global := map[string]*string{"config": &o.config, "state-dir": &o.stateDir}
	switches := map[string]map[string]*bool{
		"list": {"json": &o.json}, "status": {"json": &o.json}, "run": {"json": &o.json, "wait": &o.wait, "dry-run": &o.dryRun},
		"logs": {"path": &o.path}, "install": {"print": &o.print, "force": &o.force},
	}
	var literal bool
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !literal && arg == "--" {
			literal = true
			continue
		}
		if literal || !strings.HasPrefix(arg, "-") {
			if o.command == "" {
				switch arg {
				case "list", "validate", "run", "stop", "status", "logs", "install", "uninstall", "version":
					o.command = arg
				default:
					return o, fmt.Errorf("unknown command %q", arg)
				}
			} else {
				o.names = append(o.names, arg)
			}
			continue
		}
		if !strings.HasPrefix(arg, "--") {
			return o, fmt.Errorf("unknown option %q", arg)
		}
		flag, val, equals := strings.Cut(arg[2:], "=")
		if b := switches[o.command][flag]; b != nil {
			if equals {
				return o, fmt.Errorf("--%s takes no value", flag)
			}
			*b = true
			continue
		}
		dest := global[flag]
		if o.command == "run" && flag == "trigger" {
			dest = &o.trigger
		}
		duration := o.command == "run" && flag == "wait-timeout"
		if dest == nil && !duration {
			return o, fmt.Errorf("unknown flag --%s", flag)
		}
		if !equals && i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
			i++
			val = args[i]
		}
		if val == "" {
			return o, fmt.Errorf("--%s requires a value", flag)
		}
		if duration {
			d, err := time.ParseDuration(val)
			if err != nil || d <= 0 {
				return o, fmt.Errorf("--wait-timeout requires a positive duration")
			}
			o.waitTimeout = d
		} else {
			*dest = val
		}
	}
	n := len(o.names)
	switch o.command {
	case "":
		return o, fmt.Errorf("a command is required")
	case "run", "logs", "stop":
		if n != 1 {
			return o, fmt.Errorf("%s requires one job name", o.command)
		}
	case "status":
		if n > 1 {
			return o, fmt.Errorf("status takes at most one job name")
		}
	case "list", "validate", "version":
		if n > 0 {
			return o, fmt.Errorf("%s takes no positional arguments", o.command)
		}
	}
	if o.trigger != "manual" && o.trigger != "schedule" {
		return o, fmt.Errorf("--trigger must be manual or schedule")
	}
	return o, nil
}
