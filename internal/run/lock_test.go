//go:build linux || darwin

package run

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestAcquiresSortedAndReleasesOnTimeout(t *testing.T) {
	root := t.TempDir()
	stop := make(chan os.Signal)
	release, _, _, err := acquire(root, []string{"z"}, false, 0, stop)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	result := make(chan error, 1)
	go func() {
		release, _, _, err := acquire(root, []string{"z", "a", "a"}, true, time.Second, stop)
		if err == nil {
			release()
		}
		result <- err
	}()
	// The contender must take a while still blocked on z, even though z was
	// provided first. A duplicate a must not cause a self-deadlock.
	aPath := filepath.Join(root, "locks", "a.lock")
	end := time.Now().Add(750 * time.Millisecond)
	observed := false
	for time.Now().Before(end) {
		a, err := os.OpenFile(aPath, os.O_RDWR, 0)
		if err == nil {
			err = syscall.Flock(int(a.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
			_ = a.Close()
			if errors.Is(err, syscall.EWOULDBLOCK) {
				observed = true
				break
			}
			if err != nil {
				t.Fatal(err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := <-result; !errors.Is(err, errBusy) || !observed {
		t.Fatalf("acquisition was not sorted: observed=%v err=%v", observed, err)
	}
	free, _, _, err := acquire(root, []string{"a", "a"}, false, 0, stop)
	if err != nil {
		t.Fatalf("partially acquired lock was leaked or not deduplicated: %v", err)
	}
	free()
}
