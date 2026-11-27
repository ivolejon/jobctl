//go:build linux || darwin

package run

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"
)

var errBusy = errors.New("lock busy or wait timeout exceeded")

func lockOrder(names []string) []string {
	order := slices.Clone(names)
	slices.Sort(order)
	return slices.Compact(order)
}

func acquire(root string, names []string, wait bool, timeout time.Duration, stop <-chan os.Signal) (release func(), waited time.Duration, caught os.Signal, err error) {
	started := time.Now()
	deadline := started.Add(timeout)
	var files []*os.File
	release = func() {
		for i := len(files) - 1; i >= 0; i-- {
			_ = syscall.Flock(int(files[i].Fd()), syscall.LOCK_UN)
			_ = files[i].Close()
		}
	}
	defer func() {
		if err != nil || caught != nil {
			release()
		}
		if wait {
			waited = time.Since(started)
		}
	}()
	if len(names) == 0 {
		return
	}
	dir := filepath.Join(root, "locks")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return
	}
	for _, name := range lockOrder(names) {
		var f *os.File
		f, err = os.OpenFile(filepath.Join(dir, name+".lock"), os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return
		}
		files = append(files, f)
		for {
			select {
			case caught = <-stop:
				return
			default:
			}
			if wait && !time.Now().Before(deadline) {
				err = errBusy
				return
			}
			err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
			if err == nil {
				break
			}
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
				return
			}
			if !wait {
				err = errBusy
				return
			}
			timer := time.NewTimer(min(25*time.Millisecond, time.Until(deadline)))
			select {
			case <-timer.C:
			case caught = <-stop:
				timer.Stop()
				err = nil
				return
			}
		}
	}
	return
}
