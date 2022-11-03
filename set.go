package gonso

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"golang.org/x/sys/unix"
)

// Set represents a set of Linux namespaces.
// It can be used to perform operations in the context of those namespaces.
//
// See `Current` and `Unshare` for creating a new set.
type Set struct {
	fds   map[int]*os.File
	flags int
}

// Close closes all the file descriptors associated with the set.
//
// If this is the last reference to the file descriptors, the namespaces will be destroyed.
func (s Set) Close() error {
	for _, fd := range s.fds {
		fd.Close()
	}
	return nil
}

// set sets the current thread to the namespaces in the set.
// Errors are ignored if the current and target namespace are the same.
func (s Set) set() error {
	if s.flags&unix.CLONE_NEWNS != 0 {
		if err := unix.Unshare(unix.CLONE_FS); err != nil {
			return fmt.Errorf("error performing implicit unshare on CLONE_FS: %w", err)
		}
	}
	for _, fd := range s.fds {
		if err := unix.Setns(int(fd.Fd()), nsFlags[filepath.Base(fd.Name())]); err != nil {
			fdCur, _ := os.Readlink(filepath.Join("/proc/thread-self/ns", filepath.Base(fd.Name())))
			fdNew, _ := os.Readlink(fd.Name())
			if fdCur == fdNew && fdCur != "" {
				// Ignore this error if the namespace is already set to the same value
				continue
			}
			return fmt.Errorf("setns %s: %w", filepath.Base(fd.Name()), err)
		}
	}
	return nil
}

// Dup creates a duplicate of the current set by duplicating all the associated
// namespace file descriptors.
//
// The caller is responsible for closing both the current and the new Set.
func (s Set) Dup() (newS Set, retErr error) {
	defer func() {
		if retErr != nil {
			newS.Close()
		}
	}()

	newS.fds = make(map[int]*os.File, len(s.fds))

	for flag, fd := range s.fds {
		newFD, err := unix.Dup(int(fd.Fd()))
		if err != nil {
			return Set{}, err
		}
		newS.fds[flag] = os.NewFile(uintptr(newFD), fd.Name())
	}
	return newS, nil
}

const nonReversibleFlags = unix.CLONE_NEWUSER | unix.CLONE_NEWIPC | unix.CLONE_FS | unix.CLONE_NEWNS

// Do performs the given function in the context of the set of namespaces.
// This does not affect the state of the current thread or goroutine.
//
// The bool on the return function should be used to indicate if the thread
// should be restored to the old state. In some cases even true is returned the
// thread may still not be restored and will subsequently be thrown away.
// When in doubt, return false.  You can also just outright skip restoration by
// passing `false` to `Do`. In some cases, particularly when more than a couple
// of namespaces are set, this will perform better.
//
// Keep in mind it is *always* safer to not restore the thread, which causes go to
// throw away the thread and create a new one.
//
// The passed in function should not create any new goroutinues or those goroutines will not be in the correct namespace.
// If you need to create a goroutine and want it to be in the correct namespace, call `set.Do` again from that goroutine.
//
// If the stored namespaces includes a mount namespace, then CLONE_FS will also be implicitly unshared
// since it is impossible to setns to a mount namespace without also unsharing CLONE_FS.
func (s Set) Do(f func() bool, restore bool) error {
	chErr := make(chan error, 1)
	var cur Set

	// Some flags are not reversible so don't even bother trying to restore the thread.
	if restore {
		restore = restorable(s.flags)
	}

	if restore {
		var err error
		cur, err = Current(s.flags)
		if err != nil {
			restore = true
		}
		defer cur.Close()
	}

	go func() {
		chErr <- func() (retErr error) {
			runtime.LockOSThread()

			if err := s.set(); err != nil {
				return fmt.Errorf("error setting namespaces: %w", err)
			}

			if !f() {
				return nil
			}
			if !restore {
				return nil
			}

			if err := cur.set(); err != nil {
				return fmt.Errorf("error restoring namespaces: %w", err)
			}

			// Only unlock this thread if there are no errors If there are
			// errors the thread state will not be suitable for running
			// other goroutines again, in which case the thread should
			// just exit exit as soon as this goroutine is done.
			runtime.UnlockOSThread()

			return nil
		}()
	}()

	return <-chErr
}

// Unshare creates a new set with the namespaces specified in `flags` unshared (i.e. new namespaces are created).
//
// This does not change the current set of namespaces, it only creates a new set of namespaces that
// can be used later with the returned `Set`, e.g. `newSet.Do(func() { ... })`.
//
// CLONE_NEWUSER is not supported by this function due to limitations imposed by
// the kernel.  While it is possible to call unshare(CLONE_NEWUSER), it is not
// possible to setns(2) to a user namespace from a multi-threaded program.
func (s Set) Unshare(flags int) (Set, error) {
	type result struct {
		s   Set
		err error
	}

	if flags&unix.CLONE_NEWUSER != 0 {
		return Set{}, fmt.Errorf("setns(2) does not support joining a user namespace from a multithreaded process: %w", unix.EINVAL)
	}

	restore := restorable(flags)

	ch := make(chan result)
	go func() {
		newS, err := func() (_ Set, retErr error) {
			runtime.LockOSThread()
			defer func() {
				// Only unlock this thread if there are no errors.
				// Additionally should not unlock threads that have had non-reversiable changes made to them.
				if retErr == nil && restore {
					runtime.UnlockOSThread()
				}
			}()

			if err := unix.Unshare(flags); err != nil {
				return Set{}, fmt.Errorf("error unsharing namespaces: %w", err)
			}

			newS, err := curNamespaces(flags)
			if err != nil {
				return Set{}, fmt.Errorf("error getting namespaces: %w", err)
			}

			// Try to restore this thread so it can be re-used be go.
			if restore {
				if err := s.set(); err != nil {
					return Set{}, err
				}
			}

			return newS, nil
		}()
		ch <- result{s: newS, err: err}
	}()

	r := <-ch
	return r.s, r.err
}

// Unshare returns a new `Set` with the namespaces specified in `flags` unshared (i.e. new namespaces are created).
// The returned set only contains the namespaces specified in `flags`.
// This is the same as calling `Current(flags).Unshare(flags)`.
func Unshare(flags int) (Set, error) {
	s, err := Current(flags)
	if err != nil {
		return Set{}, err
	}
	return s.Unshare(flags)
}

// Mount the set's namespaces to the specified target directory with each
// namespace being mounted to a file named after the namespace type as seen in
// procfs.
//
// The target directory must already exist.
// It is up to the caller to clean up mounts.
//
// If the set contains a mount namespace it is the caller's responsibility to
// make sure that the mounts performed here are propagated to caller's
// desired mount namespace.
//
// Mounting a mount namespace is also tricky see the mount(2) documentation for details.
// In particular, mounting a mount namespace magic link may cause EINVAL if the parent uses MS_SHARED.
func (s Set) Mount(target string) error {
	var err error

	err2 := s.Do(func() bool {
		for id := range s.fds {
			name := nsFlagsReverse[id]
			p := filepath.Join("/proc/thread-self/ns", name)

			var f *os.File
			f, err = os.Create(filepath.Join(target, name))
			if err != nil {
				err = fmt.Errorf("error creating file for ns %s: %w", name, err)
				return false
			}
			f.Close()
			if err = unix.Mount(p, filepath.Join(target, name), "none", unix.MS_BIND, ""); err != nil {
				err = fmt.Errorf("error mounting ns %s: %w", name, err)
				return false
			}
		}
		return false
	}, false)
	if err2 != nil {
		return err2
	}
	return err
}

// FromPid returns a `Set` for the given pid and namespace flags.
//
// This requires `pidfd_open(2)` support which was first added in kernel 5.13.
func FromPid(pid int, flags int) (Set, error) {
	type result struct {
		s   Set
		err error
	}

	if flags&unix.CLONE_NEWUSER != 0 {
		return Set{}, fmt.Errorf("setns(2) does not support joining a user namespace from a multithreaded process: %w", unix.EINVAL)
	}

	ch := make(chan result)
	go func() {
		newS, err := func() (_ Set, retErr error) {
			runtime.LockOSThread()

			curr, err := curNamespaces(flags)
			if err != nil {
				return Set{}, err
			}

			pidFD, err := unix.PidfdOpen(pid, 0)
			if err != nil {
				return Set{}, err
			}
			defer unix.Close(pidFD)

			if err := unix.Setns(pidFD, flags); err != nil {
				return Set{}, fmt.Errorf("error joining namespaces: %w", err)
			}

			newS, err := curNamespaces(flags)
			if err != nil {
				return Set{}, fmt.Errorf("error getting namespaces: %w", err)
			}

			if err := curr.set(); err == nil {
				runtime.UnlockOSThread()
			}

			return newS, nil
		}()
		ch <- result{s: newS, err: err}
	}()

	r := <-ch
	return r.s, r.err
}

func restorable(flags int) bool {
	return flags&nonReversibleFlags == 0
}

// These are the flags that can be passed to `Unshare` and `Current`.
// They are the same as the flags for `unshare(2)` and `clone(2)`.
//
// Pretty much these values are here to (hopefully) make the code easier to
// understand since `CLONE_NEW*` is werid when being used to filter existing
// namespaces (as with `Current`) rather than creating a new one.
const (
	NS_CGROUP = unix.CLONE_NEWCGROUP
	NS_IPC    = unix.CLONE_NEWIPC
	NS_MNT    = unix.CLONE_NEWNS
	NS_NET    = unix.CLONE_NEWNET
	NS_PID    = unix.CLONE_NEWPID
	NS_TIME   = unix.CLONE_NEWTIME
	NS_USER   = unix.CLONE_NEWUSER
	NS_UTS    = unix.CLONE_NEWUTS
)

var (
	nsFlags = map[string]int{
		"cgroup": unix.CLONE_NEWCGROUP,
		"ipc":    unix.CLONE_NEWIPC,
		"mnt":    unix.CLONE_NEWNS,
		"net":    unix.CLONE_NEWNET,
		"pid":    unix.CLONE_NEWPID,
		"time":   unix.CLONE_NEWTIME,
		"user":   unix.CLONE_NEWUSER,
		"uts":    unix.CLONE_NEWUTS,
	}

	nsFlagsReverse = map[int]string{
		unix.CLONE_NEWCGROUP: "cgroup",
		unix.CLONE_NEWIPC:    "ipc",
		unix.CLONE_NEWNS:     "mnt",
		unix.CLONE_NEWNET:    "net",
		unix.CLONE_NEWPID:    "pid",
		unix.CLONE_NEWTIME:   "time",
		unix.CLONE_NEWUSER:   "user",
		unix.CLONE_NEWUTS:    "uts",
	}
)

// Current returns the set of namespaces for the current thread.
//
// If `flags` is 0, all namespaces are returned.
func Current(flags int) (Set, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if flags == 0 {
		// NS_USER is intentionally not included here since it is not supported by setns(2) from a multithreaded program.
		flags = NS_CGROUP | NS_IPC | NS_MNT | NS_NET | NS_PID | NS_TIME | NS_UTS
	}

	return curNamespaces(flags)
}

func curNamespaces(flags int) (s Set, retErr error) {
	defer func() {
		if retErr != nil {
			s.Close()
		}
	}()

	s.fds = make(map[int]*os.File, len(nsFlags))
	s.flags = flags
	for name, flag := range nsFlags {
		if flags&flag == 0 {
			continue
		}

		fd, err := os.Open(filepath.Join("/proc/thread-self/ns", name))
		if err != nil {
			return Set{}, fmt.Errorf("error opening namespace file: %w", err)
		}
		s.fds[flag] = fd
	}

	return s, nil
}
