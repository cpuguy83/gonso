package gonso

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestUnshare(t *testing.T) {
	dir := t.TempDir()
	if err := unix.Mount("tmpfs", dir, "tmpfs", 0, ""); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := unix.Unmount(dir, unix.MNT_DETACH); err != nil {
			t.Log(err)
		}
	}()

	getNs := func(ns string) (*os.File, error) {
		t.Helper()

		p := "/proc/self/task/" + strconv.Itoa(unix.Gettid()) + "/ns/" + ns
		l, err := os.Readlink(p)
		if err != nil {
			return nil, fmt.Errorf("readlink %s: %w", p, err)
		}

		l = strings.Replace(strings.Replace(strings.Replace(l, ":", "-", -1), "[", "", -1), "]", "", -1)

		if _, err := os.Stat(filepath.Join(dir, l)); !os.IsNotExist(err) {
			return nil, fmt.Errorf("expected file not found, got: %w", err)
		}

		f, err := os.Create(filepath.Join(dir, l))
		if err != nil {
			return nil, fmt.Errorf("error creating ns file: %w", err)
		}

		f.Close()

		if err := unix.Mount(p, f.Name(), "none", unix.MS_BIND, ""); err != nil {
			return nil, fmt.Errorf("error mounting ns file: %w", err)
		}
		return os.Open(f.Name())
	}

	s, err := Current(NS_NET)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	newS, err := s.Unshare(unix.CLONE_NEWNET)
	if err != nil {
		t.Fatal(err)
	}
	defer newS.Close()

	var p1, p2 *os.File
	var pErr error
	err = s.Do(func() bool {
		p1, pErr = getNs("net")
		return pErr == nil
	}, true)
	if pErr != nil {
		t.Fatal(pErr)
	}
	if err != nil {
		t.Fatal(err)
	}

	defer p1.Close()

	err = newS.Do(func() bool {
		p2, pErr = getNs("net")
		return pErr == nil
	}, true)
	if pErr != nil {
		t.Fatal(pErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer p2.Close()

	if p1.Name() == p2.Name() {
		t.Fatal("expected new mount namespace")
	}
}

func BenchmarkDo(b *testing.B) {
	for _, restore := range []bool{true, false} {
		b.Run(fmt.Sprintf("restore=%v", restore), func(b *testing.B) {
			b.Run("one namespace", func(b *testing.B) {
				benchmarkNamespace(b, unix.CLONE_NEWNET, restore)
			})
			b.Run("two namespaces", func(b *testing.B) {
				benchmarkNamespace(b, unix.CLONE_NEWNET|unix.CLONE_NEWIPC, restore)
			})
			b.Run("three namespaces", func(b *testing.B) {
				benchmarkNamespace(b, unix.CLONE_NEWNET|unix.CLONE_NEWIPC|unix.CLONE_NEWUTS, restore)
			})
			b.Run("four namespaces", func(b *testing.B) {
				benchmarkNamespace(b, unix.CLONE_NEWNET|unix.CLONE_NEWIPC|unix.CLONE_NEWUTS|unix.CLONE_NEWPID, restore)
			})
			b.Run("five namespaces", func(b *testing.B) {
				benchmarkNamespace(b, unix.CLONE_NEWNET|unix.CLONE_NEWIPC|unix.CLONE_NEWUTS|unix.CLONE_NEWPID|unix.CLONE_NEWCGROUP, restore)
			})
		})
	}
}

func benchmarkNamespace(b *testing.B, flags int, restore bool) {
	b.StopTimer()
	curr, err := Current(flags)
	if err != nil {
		b.Fatal(err)
	}
	defer curr.Close()

	s, err := curr.Unshare(flags)
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()

	b.StartTimer()

	for i := 0; i < b.N; i++ {
		err := s.Do(func() bool {
			return restore
		}, restore)
		if err != nil {
			b.Error(err)
		}
	}
}
