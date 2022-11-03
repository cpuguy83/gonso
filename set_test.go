package gonso

import (
	"fmt"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/sys/unix"
)

func TestMount(t *testing.T) {
	s, err := Unshare(NS_NET | NS_IPC)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	dir := t.TempDir()
	if err := s.Mount(dir); err != nil {
		t.Fatal(err)
	}

	// self-mount to ease cleanup
	if err := unix.Mount(dir, dir, "none", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		t.Fatal(err)
	}

	defer func() {
		if err := unix.Unmount(dir, unix.MNT_DETACH); err != nil {
			t.Logf("error unmounting set at %s: %v", dir, err)
		}
	}()

	// Now that these are mounted, we should be able to close the Set and still be able to use the namespoaces
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	ch := make(chan error, 1)

	go func() {
		runtime.LockOSThread()
		defer close(ch)

		fd, err := unix.Open(filepath.Join(dir, "ipc"), unix.O_RDONLY, 0)
		if err != nil {
			ch <- fmt.Errorf("error opening mnt fd: %w", err)
			return
		}
		defer unix.Close(fd)

		err = unix.Setns(fd, unix.CLONE_NEWIPC)
		if err != nil {
			ch <- fmt.Errorf("setns mnt: %w", err)
		}

		fd, err = unix.Open(filepath.Join(dir, "net"), unix.O_RDONLY, 0)
		if err != nil {
			ch <- fmt.Errorf("error opening mnt fd: %w", err)
			return
		}
		defer unix.Close(fd)

		err = unix.Setns(fd, unix.CLONE_NEWNET)
		if err != nil {
			ch <- fmt.Errorf("setns net: %w", err)
			return
		}
	}()

	for err := range ch {
		if err != nil {
			t.Error(err)
		}
	}
}
