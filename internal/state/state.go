package state

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/ivolejon/jobctl/internal/config"
)

const LogName = "output.log"
const ProcessName = "process.json"
const runLayout = "20060102T150405Z"

var runIDPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{6}$`)

type Receipt struct {
	Version         int        `json:"version"`
	Job             string     `json:"job"`
	RunID           string     `json:"runId"`
	Trigger         string     `json:"trigger"`
	Status          string     `json:"status"`
	Command         string     `json:"command"`
	Cwd             string     `json:"cwd"`
	Schedule        string     `json:"schedule"`
	StartedAt       time.Time  `json:"startedAt"`
	EndedAt         *time.Time `json:"endedAt"`
	DurationSeconds float64    `json:"durationSeconds"`
	ExitCode        *int       `json:"exitCode"`
	Signal          *string    `json:"signal"`
	WaitedSeconds   float64    `json:"waitedSeconds"`
	Log             string     `json:"log"`
}

func Resolve(path string) (string, error) {
	if path == "" {
		path = os.Getenv("JOBCTL_STATE_DIR")
	}
	if path == "" {
		base := os.Getenv("XDG_STATE_HOME")
		if base == "" {
			base = "~/.local/state"
		}
		path = filepath.Join(base, "jobctl")
	}
	return config.Resolve(path, "")
}

func Runs(root, job string) string { return filepath.Join(root, "runs", job) }

func CreateRun(root, job string, started time.Time) (dir, id string, err error) {
	base := Runs(root, job)
	if err = os.MkdirAll(base, 0700); err != nil {
		return
	}
	for i := 0; i < 10; i++ {
		id, err = randomID()
		if err != nil {
			return
		}
		id = started.UTC().Format(runLayout) + "-" + id
		dir = filepath.Join(base, id)
		err = os.Mkdir(dir, 0700)
		if !errors.Is(err, os.ErrExist) {
			return
		}
	}
	return
}

func randomID() (string, error) {
	var b [3]byte
	_, err := rand.Read(b[:])
	return hex.EncodeToString(b[:]), err
}

func WriteReceipt(dir string, r *Receipt) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(filepath.Join(dir, "meta.json"), append(data, '\n'), 0600)
}

// Process identifies the running jobctl process so `jobctl stop` can signal it
// after checking that the pid still belongs to the recorded process.
type Process struct {
	Version   int       `json:"version"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"startedAt"`
	PSStart   string    `json:"psStart"`
}

func WriteProcess(dir string, pid int, started time.Time, psStart string) error {
	data, err := json.MarshalIndent(Process{Version: 1, PID: pid, StartedAt: started.UTC(), PSStart: psStart}, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(filepath.Join(dir, ProcessName), append(data, '\n'), 0600)
}

func ReadProcess(dir string) (*Process, error) {
	data, err := os.ReadFile(filepath.Join(dir, ProcessName))
	if err != nil {
		return nil, err
	}
	var p Process
	if err = json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	if p.Version != 1 || p.PID <= 0 || p.StartedAt.IsZero() {
		return nil, errors.New("invalid process record")
	}
	return &p, nil
}

// AtomicWrite replaces the file only after its contents have been synced.
func AtomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".jobctl-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func readReceipt(dir string) (*Receipt, error) {
	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return nil, err
	}
	var r Receipt
	if err = json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	if r.Version != 1 || r.RunID != filepath.Base(dir) || r.Job != filepath.Base(filepath.Dir(dir)) || r.StartedAt.IsZero() {
		return nil, errors.New("invalid run receipt")
	}
	return &r, nil
}

func candidates(base string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(base)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return entries, err
}

// Check the link first, then reconcile with the directories. A concurrent run
// can finish out of order or leave a stale link if publishing it failed.
func Latest(root, job string) (*Receipt, error) {
	base := Runs(root, job)
	target, _ := os.Readlink(filepath.Join(base, "latest"))
	entries, err := candidates(base)
	if err != nil {
		return nil, err
	}
	var latest *Receipt
	consider := func(e os.DirEntry) {
		if !e.IsDir() || !runIDPattern.MatchString(e.Name()) {
			return
		}
		if latest != nil && e.Name()[:len(runLayout)] < latest.StartedAt.UTC().Format(runLayout) {
			return
		}
		r, err := readReceipt(filepath.Join(base, e.Name()))
		if err != nil {
			return
		}
		if latest == nil || r.StartedAt.After(latest.StartedAt) || r.StartedAt.Equal(latest.StartedAt) && r.RunID > latest.RunID {
			latest = r
		}
	}
	for _, e := range entries {
		if e.Name() == target {
			consider(e)
			break
		}
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Name() != target {
			consider(entries[i])
		}
	}
	return latest, nil
}

func LatestRunning(root, job string) (*Receipt, string, error) {
	base := Runs(root, job)
	entries, err := candidates(base)
	if err != nil {
		return nil, "", err
	}
	var latest *Receipt
	var latestDir string
	for _, e := range entries {
		if !e.IsDir() || !runIDPattern.MatchString(e.Name()) {
			continue
		}
		dir := filepath.Join(base, e.Name())
		r, err := readReceipt(dir)
		if err != nil || r.Status != "running" {
			continue
		}
		if latest == nil || r.StartedAt.After(latest.StartedAt) {
			latest, latestDir = r, dir
		}
	}
	return latest, latestDir, nil
}

func PublishLatest(root, job string) {
	r, err := Latest(root, job)
	if err != nil || r == nil {
		return
	}
	base := Runs(root, job)
	suffix, err := randomID()
	if err != nil {
		return
	}
	temp := filepath.Join(base, ".latest-"+suffix)
	if os.Symlink(r.RunID, temp) == nil {
		defer os.Remove(temp)
		_ = os.Rename(temp, filepath.Join(base, "latest"))
	}
}

func Prune(root, job, current string, days int64, now time.Time) {
	// Cap to a far earlier date to avoid arithmetic overflow on large TOML integers.
	if days > 4_000_000 {
		return
	}
	cutoff := now.AddDate(0, 0, -int(days))
	base := Runs(root, job)
	entries, _ := candidates(base)
	for _, e := range entries {
		if !e.IsDir() || e.Name() == current || !runIDPattern.MatchString(e.Name()) {
			continue
		}
		dir := filepath.Join(base, e.Name())
		r, err := readReceipt(dir)
		if err == nil && r.EndedAt != nil && r.Status != "running" && r.EndedAt.Before(cutoff) {
			_ = os.RemoveAll(dir)
		}
	}
}
