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

func getNS(t *testing.T, ns string) string {
	t.Helper()

	p := "/proc/thread-self/ns/" + ns
	l, err := os.Readlink(p)
	if err != nil {
		t.Fatalf("readlink %s: %v", p, err)
	}
	return l
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
		err = s.Do(func() bool {
			p1 = getNS(t, nsName)
			return restore
		}, restore)
		if err != nil {
			t.Fatal(err)
		}

		err = newS.Do(func() bool {
			p2 = getNS(t, nsName)
			return restore
		}, restore)
		if err != nil {
			t.Fatal(err)
		}

		if p1 == p2 {
			t.Fatal("expected new namespace")
		}
	}
}

func TestFromPid(t *testing.T) {
	name := "net"
	ns := nsFlags[name]
	cur, err := Current(ns)
	if err != nil {
		t.Fatal(err)
	}
	defer cur.Close()

	unshared, err := cur.Unshare(ns)
	if err != nil {
		t.Fatal(err)
	}
	defer unshared.Close()

	pidS, err := FromPid(os.Getpid(), ns)
	if err != nil {
		t.Fatal(err)
	}

	var unsharedP, pidP string

	err = unshared.Do(func() bool {
		unsharedP = getNS(t, name)
		return false
	}, false)
	if err != nil {
		t.Fatal(err)
	}

	err = pidS.Do(func() bool {
		pidP = getNS(t, name)
		return false
	}, false)
	if err != nil {
		t.Fatal(err)
	}

	if unsharedP == pidP {
		t.Error("expected different namespaces")
	}

	err = cur.Do(func() bool {
		curP := getNS(t, name)
		if curP != pidP {
			t.Error("expected same namespaces")
		}
		return false
	}, false)
	if err != nil {
		t.Fatal(err)
	}
}

func TestUserns(t *testing.T) {
	flags := unix.CLONE_NEWUSER | unix.CLONE_NEWNET
	set, err := Unshare(flags)
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()

	err = set.Do(func() bool { return true }, true)
	if err == nil {
		t.Fatal("exepcted error callindg `Do` with a userns")
	}

	tmp := t.TempDir()
	if err := unix.Mount(tmp, tmp, "none", unix.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
	defer func() {
		unix.Unmount(tmp, unix.MNT_DETACH)
	}()

	if err := set.Mount(tmp); err != nil {
		t.Fatal(err)
	}

	set2, err := FromDir(tmp, flags)
	if err != nil {
		t.Fatal(err)
	}
	set2.Close()

	// Check that we can mask the user namespace and call do
	dup, err := set.Dup(unix.CLONE_NEWNET)
	if err != nil {
		t.Fatal(err)
	}
	defer dup.Close()

	err = dup.Do(func() bool { return true }, true)
	if err != nil {
		t.Fatal(err)
	}
}

func TestFromDir(t *testing.T) {
	flags := unix.CLONE_NEWNET | unix.CLONE_NEWIPC

	curr, err := Current(flags)
	if err != nil {
		t.Fatal(err)
	}
	defer curr.Close()

	set, err := curr.Unshare(flags)
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()

	tmp := t.TempDir()
	if err := unix.Mount(tmp, tmp, "none", unix.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
	defer func() {
		unix.Unmount(tmp, unix.MNT_DETACH)
	}()

	if err := set.Mount(tmp); err != nil {
		t.Fatal(err)
	}

	set.Close()

	set, err = FromDir(tmp, flags)
	if err != nil {
		t.Fatal(err)
	}

	err = set.Do(func() bool {
		return true
	}, true)
	if err != nil {
		t.Fatal(err)
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
