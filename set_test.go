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

func TestDup(t *testing.T) {
	t.Run("some fds", func(t *testing.T) {
		s, err := Current(0)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		dup, err := s.Dup(NS_NET | NS_IPC)
		if err != nil {
			t.Fatal(err)
		}
		defer dup.Close()

		if len(dup.fds) != 2 {
			t.Errorf("expected 2 fds, got %d", len(dup.fds))
		}

		_, ok := dup.fds[NS_NET]
		if !ok {
			t.Error("expected net fd")
		}

		_, ok = dup.fds[NS_IPC]
		if !ok {
			t.Error("expected ipc fd")
		}

		if err := s.Close(); err != nil {
			t.Fatal(err)
		}

		// Make sure `Do` still works after the original set is closed
		if err := dup.Do(func() {}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("all fds", func(t *testing.T) {
		s, err := Current(0)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()

		dup2, err := s.Dup(0)
		if err != nil {
			t.Fatal(err)
		}
		defer dup2.Close()
		if len(dup2.fds) != len(s.fds) {
			t.Errorf("expected 0 fds, got %d", len(dup2.fds))
		}

		if err := s.Close(); err != nil {
			t.Fatal(err)
		}

		// Make sure `Do` still works after the original set is closed
		if err := dup2.DoRaw(func() bool {
			return false
		}, false); err != nil {
			t.Fatal(err)
		}
	})

}
