package gonso

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
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
		err = s.DoRaw(func() bool {
			p1 = getNS(t, nsName)
			return restore
		}, restore)
		if err != nil {
			t.Fatal(err)
		}

		err = newS.DoRaw(func() bool {
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

	for kind := range cur.fds {
		if kind == ns {
			if cur.testGetID(t, kind) == unshared.testGetID(t, kind) {
				t.Fatalf("expected different ID for %s", nsFlagsReverse[kind])
			}
		} else {
			if cur.testGetID(t, kind) != unshared.testGetID(t, kind) {
				t.Fatalf("expected same ID for %s", nsFlagsReverse[kind])
			}
		}
	}

	pidS, err := FromPid(os.Getpid(), ns)
	if err != nil {
		t.Fatal(err)
	}

	var unsharedP, pidP string

	err = unshared.DoRaw(func() bool {
		unsharedP = getNS(t, name)
		return false
	}, false)
	if err != nil {
		t.Fatal(err)
	}

	err = pidS.DoRaw(func() bool {
		pidP = getNS(t, name)
		return false
	}, false)
	if err != nil {
		t.Fatal(err)
	}

	if unsharedP == pidP {
		t.Error("expected different namespaces")
	}

	err = cur.DoRaw(func() bool {
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

const cmdReadMappings = "readmappings"

func readMappings() {
	err := func() error {
		f1, err := os.Open("/proc/self/uid_map")
		if err != nil {
			return err
		}
		defer f1.Close()

		f2, err := os.Open("/proc/self/gid_map")
		if err != nil {
			return err
		}
		defer f2.Close()

		if _, err := io.Copy(os.Stdout, f1); err != nil {
			return err
		}
		if _, err := io.Copy(os.Stdout, f2); err != nil {
			return err
		}
		return nil
	}()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v", err)
		os.Exit(1)
	}
}

func checkIDMaps(t *testing.T, set Set, uidMaps, gidMaps []IDMap) {
	ch, cancel, stdio, err := set.testDoRexec(t, cmdReadMappings, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stdio.Close()
	defer cancel()

	chErr := make(chan error, 1)
	go func() {
		defer close(chErr)
		code := <-ch
		if code != 0 {
			data, _ := io.ReadAll(stdio.err)
			chErr <- fmt.Errorf("unexpected exit code %d: %w: %s", code, unix.Errno(code), string(data))
		}
	}()

	maps := append(uidMaps, gidMaps...)
	scanner := bufio.NewScanner(stdio.out)
	var i int
	for i = 0; scanner.Scan(); i++ {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 3 {
			t.Errorf("wrong number of fields in id map: %v", fields)
			continue
		}

		var idMap IDMap
		idMap.ContainerID, _ = strconv.Atoi(fields[0])
		idMap.HostID, _ = strconv.Atoi(fields[1])
		idMap.Size, _ = strconv.Atoi(fields[2])

		other := maps[i]
		if idMap.ContainerID != other.ContainerID || idMap.HostID != other.HostID || idMap.Size != other.Size {
			t.Errorf("unexpected id map: %+v != %+v", idMap, other)
		}
	}

	if i != len(maps) {
		t.Errorf("expected %d maps, got %d", len(maps), i)
	}

	if err := <-chErr; err != nil {
		t.Fatal(err)
	}
}

func TestUserns(t *testing.T) {
	flags := unix.CLONE_NEWUSER | unix.CLONE_NEWNET

	maps := []IDMap{
		{HostID: 0, ContainerID: 0, Size: 1},
		{HostID: 1000, ContainerID: 10000, Size: 1000},
	}
	set, err := Unshare(flags, WithIDMaps(maps, maps))
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	checkIDMaps(t, set, maps, maps)

	if _, ok := set.fds[unix.CLONE_NEWUSER]; !ok {
		t.Fatal("set should include userns")
	}

	err = set.DoRaw(func() bool { return true }, true)
	if err == nil {
		t.Fatal("exepcted error callindg `Do` with a userns")
	}

	unshared, err := set.Unshare(unix.CLONE_NEWIPC)
	if err != nil {
		t.Fatal(err)
	}

	if set.testGetID(t, unix.CLONE_NEWUSER) != unshared.testGetID(t, unix.CLONE_NEWUSER) {
		t.Fatal("expected same user id")
	}

	unshared.Close()

	tmp := t.TempDir()
	if err := mount(tmp, tmp, false); err != nil {
		t.Fatal(err)
	}
	defer func() {
		unmount(tmp)
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

	err = dup.DoRaw(func() bool { return true }, true)
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
	if err := mount(tmp, tmp, false); err != nil {
		t.Fatal(err)
	}
	defer func() {
		unmount(tmp)
	}()

	if err := set.Mount(tmp); err != nil {
		t.Fatal(err)
	}

	set.Close()

	set, err = FromDir(tmp, flags)
	if err != nil {
		t.Fatal(err)
	}

	err = set.DoRaw(func() bool {
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
			err := s.DoRaw(func() bool {
				return restore
			}, restore)
			if err != nil {
				b.Error(err)
			}
		}
	}
}
