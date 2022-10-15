package gonso

import (
	"fmt"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestUnshare(t *testing.T) {
	t.Run("restore=false", testUnshare(t, false))
	t.Run("restore=true", testUnshare(t, true))
	t.Run("parallel", func(t *testing.T) {
		if testing.Short() {
			t.Skip("skipping test in short mode")
		}
		for i := 0; i < 1000; i++ {
			t.Run("restore=false", asParallel(t, testUnshare(t, false)))
			t.Run("restore=true", asParallel(t, testUnshare(t, true)))
		}
	})
}

func testUnshare(t *testing.T, restore bool) func(t *testing.T) {
	return func(t *testing.T) {
		dir := t.TempDir()
		if err := unix.Mount("tmpfs", dir, "tmpfs", 0, ""); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := unix.Unmount(dir, unix.MNT_DETACH); err != nil {
				t.Log(err)
			}
		}()

		getNs := func(ns string) (string, error) {
			t.Helper()

			p := "/proc/thread-self/ns/" + ns
			l, err := os.Readlink(p)
			if err != nil {
				return "", fmt.Errorf("readlink %s: %w", p, err)
			}
			return l, nil
		}

		nsName := "net"
		ns := nsFlags[nsName]

		s, err := Current(ns)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()

		newS, err := s.Unshare(ns)
		if err != nil {
			t.Fatal(err)
		}
		defer newS.Close()

		var p1, p2 string
		var pErr error
		err = s.Do(func() bool {
			p1, pErr = getNs(nsName)
			return pErr == nil
		}, restore)
		if pErr != nil {
			t.Fatal(pErr)
		}
		if err != nil {
			t.Fatal(err)
		}

		err = newS.Do(func() bool {
			p2, pErr = getNs(nsName)
			return pErr == nil
		}, restore)
		if pErr != nil {
			t.Fatal(pErr)
		}
		if err != nil {
			t.Fatal(err)
		}

		if p1 == p2 {
			t.Fatal("expected new namespace")
		}
	}
}

func asParallel(t *testing.T, testFunc func(*testing.T)) func(t *testing.T) {
	return func(t *testing.T) {
		t.Parallel()
		testFunc(t)
	}
}

func BenchmarkDo(b *testing.B) {
	for _, restore := range []bool{true, false} {
		b.Run(fmt.Sprintf("restore=%v", restore), func(b *testing.B) {
			b.Run("one namespace", benchmarkNamespace(b, unix.CLONE_NEWNET, restore))
			b.Run("two namespaces", benchmarkNamespace(b, unix.CLONE_NEWNET|unix.CLONE_NEWIPC, restore))
			b.Run("three namespaces", benchmarkNamespace(b, unix.CLONE_NEWNET|unix.CLONE_NEWIPC|unix.CLONE_NEWUTS, restore))
			b.Run("four namespaces", benchmarkNamespace(b, unix.CLONE_NEWNET|unix.CLONE_NEWIPC|unix.CLONE_NEWUTS|unix.CLONE_NEWPID, restore))
			b.Run("five namespaces", benchmarkNamespace(b, unix.CLONE_NEWNET|unix.CLONE_NEWIPC|unix.CLONE_NEWUTS|unix.CLONE_NEWPID|unix.CLONE_NEWCGROUP, restore))
		})
	}
}

func benchmarkNamespace(b *testing.B, flags int, restore bool) func(b *testing.B) {
	return func(b *testing.B) {
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
}
