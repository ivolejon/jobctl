package schedule

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type WriteFile func(string, []byte, os.FileMode) error

type installation struct {
	name               string
	files              []File
	installed, changed bool
	keepActive         bool
}

func Install(root string, units []Unit, print io.Writer, force bool, write WriteFile) error {
	var all []installation
	for _, u := range units {
		files, err := render(u)
		if err != nil {
			return fmt.Errorf("job %s: %w", u.Name, err)
		}
		all = append(all, installation{name: u.Name, files: files})
	}
	if print != nil {
		for _, item := range all {
			for _, file := range item.files {
				if _, err := fmt.Fprint(print, file.Content, "\n"); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if len(all) == 0 {
		return nil
	}
	dir, generated, err := directories(root)
	if err != nil {
		return err
	}
	for i := range all {
		for _, file := range all[i].files {
			existing, err := check(filepath.Join(dir, file.Name), force)
			if err != nil {
				return err
			}
			all[i].installed = all[i].installed || existing != nil
			all[i].changed = all[i].changed || !bytes.Equal(existing, []byte(file.Content))
			if _, err = check(filepath.Join(generated, file.GeneratedName), force); err != nil {
				return err
			}
		}
	}
	for _, d := range []string{dir, generated} {
		if err = os.MkdirAll(d, 0700); err != nil {
			return err
		}
	}
	for i := range all {
		item := &all[i]
		if err := prepare(item); err != nil {
			return err
		}
		for _, file := range item.files {
			for _, path := range []string{filepath.Join(generated, file.GeneratedName), filepath.Join(dir, file.Name)} {
				old, err := check(path, force)
				if err != nil {
					return err
				}
				if !bytes.Equal(old, []byte(file.Content)) {
					if err = write(path, []byte(file.Content), 0600); err != nil {
						return err
					}
				}
			}
		}
	}
	return activate(dir, all)
}

func Uninstall(root string, names []string) error {
	if len(names) == 0 {
		return nil
	}
	dir, generated, err := directories(root)
	if err != nil {
		return err
	}
	var all []installation
	for _, name := range names {
		files := platformFiles(name)
		item := installation{name: name, files: files}
		for _, f := range files {
			data, err := check(filepath.Join(dir, f.Name), false)
			if err != nil {
				return err
			}
			item.installed = item.installed || data != nil
			if _, err = check(filepath.Join(generated, f.GeneratedName), false); err != nil {
				return err
			}
		}
		all = append(all, item)
	}
	removed := false
	for _, item := range all {
		if item.installed {
			if err = deactivate(item.name); err != nil {
				return err
			}
			removed = true
		}
		for _, f := range item.files {
			for _, path := range []string{filepath.Join(dir, f.Name), filepath.Join(generated, f.GeneratedName)} {
				data, err := check(path, false)
				if err != nil {
					return err
				}
				if data != nil {
					if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
						return err
					}
				}
			}
		}
	}
	if removed {
		return reload()
	}
	return nil
}

func directories(root string) (string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	return filepath.Join(home, platformDir), filepath.Join(root, "generated", platform), nil
}

func check(path string, force bool) ([]byte, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		if force && info.Mode()&os.ModeSymlink != 0 {
			return []byte{}, nil
		}
		return nil, fmt.Errorf("refusing to replace or remove non-regular file %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !force && !bytes.HasPrefix(data, []byte(marker)) &&
		(legacyMarker == "" || !bytes.HasPrefix(data, []byte(legacyMarker))) {
		return nil, fmt.Errorf("refusing to replace or remove unmarked file %s", path)
	}
	return data, nil
}

func systemCommand(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err == nil {
		return nil
	}
	if text := strings.TrimSpace(string(out)); text != "" {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, text)
	}
	return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
}
