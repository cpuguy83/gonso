package gonso

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"

	"golang.org/x/sys/unix"
)

// alias to make some things easier to read
type nsFlag = int

// Set represents a set of Linux namespaces.
// It can be used to perform operations in the context of those namespaces.
//
// See `Current` and `Unshare` for creating a new set.
type Set struct {
	// fd type (e.g. CLONE_NEWNS) => fd
	fds   map[nsFlag]int
	flags int
}

// Close closes all the file descriptors associated with the set.
//
// If this is the last reference to the file descriptors, the namespaces will be destroyed.
func (s Set) Close() error {
	for _, fd := range s.fds {
		sys_close(fd)
	}
	return nil
}

// set sets the current thread to the namespaces in the set.
// Errors are ignored if the current and target namespace are the same.
//
// If skipUser is true, then the user namespace is not set.
// This is useful when `Unshare` is called with an existing userns in the set.
// We can't setns to the userns here because of how user namespaces work, but in some cases we can fork and set the namespace in the child.
// In those cases `set` is just used to set all the other namespaces first.
func (s Set) set(skipUser bool) error {
	if s.flags&unix.CLONE_NEWNS != 0 {
		if err := unshare(unix.CLONE_FS); err != nil {
			return fmt.Errorf("error performing implicit unshare on CLONE_FS: %w", err)
		}
	}
	for kind, fd := range s.fds {
		if kind == unix.CLONE_NEWUSER && skipUser {
			continue
		}
		name := nsFlagsReverse[kind]
		if err := setns(fd, kind); err != nil {
			fdCur, _ := os.Readlink(filepath.Join("/proc/thread-self/ns", name))
			fdNew, _ := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
			if fdCur == fdNew && fdCur != "" {
				// Ignore this error if the namespace is already set to the same value
				continue
			}
			return fmt.Errorf("setns %s: %w", name, err)
		}
	}
	return nil
}

// FdSet is a map of namespace flags to file descriptors.
// It is used by a Set to store raw file descriptors.
type FdSet map[int]*os.File

// Get returns the fd for the given flag
// Only one fd is returned.
// Only one flag should be provided.
// If the flag is not in the set, nil is returned.
func (f FdSet) Get(flag int) *os.File {
	return f[flag]
}

// Close closes all the fds in the set.
func (f FdSet) Close() {
	for _, fd := range f {
		fd.Close()
	}
}

// Fds returns an FdSet, which is a dup of all the fds in the set.
// The caller is responsible for closing the returned FdSet.
// Additionally the caller is responsible for closing the original set.
//
// On error, any new fd that was created during this function call is closed.
func (s Set) Fds(flags int) (_ FdSet, retErr error) {
	if flags == 0 {
		flags = s.flags
	}
	rawSet := make(FdSet, len(s.fds))

	defer func() {
		if retErr != nil {
			rawSet.Close()
		}
	}()
	for flag, fd := range s.fds {
		if flags&flag == 0 {
			continue
		}

		nfd, err := dup(fd)
		if err != nil {
			return FdSet{}, fmt.Errorf("error duping fd for %s: %w", nsFlagsReverse[flag], err)
		}
		rawSet[flag] = os.NewFile(uintptr(nfd), nsFlagsReverse[flag])
	}
	return rawSet, nil
}

// ID gets the id of the namespace for the given flag.
// Only one flag should ever be provided.
func (s Set) ID(flag int) (string, error) {
	fd, ok := s.fds[flag]
	if !ok {
		return "", fmt.Errorf("flag not in set for %s", nsFlagsReverse[flag])
	}
	return os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))

}

// Dup creates a duplicate of the current set by duplicating the namespace file descriptors in the set and returning a new set.
// Specifying `flags` will only duplicate the namespaces specified in `flags`.
// If flags is 0, all namespaces in the set will be duplicated.
//
// The caller is responsible for closing both the current and the new Set.
func (s Set) Dup(flags int) (newS Set, retErr error) {
	defer func() {
		if retErr != nil {
			newS.Close()
		}
	}()

	newS.fds = make(map[nsFlag]int, len(s.fds))

	if flags == 0 {
		flags = s.flags
	}
	newS.flags = flags

	for flag, fd := range s.fds {
		if flags&flag == 0 {
			continue
		}
		newFD, err := dup(fd)
		if err != nil {
			return Set{}, err
		}
		newS.fds[flag] = newFD
	}
	return newS, nil
}

const nonReversibleFlags = unix.CLONE_NEWUSER | unix.CLONE_NEWIPC | unix.CLONE_FS | unix.CLONE_NEWNS

// Do does the same as DoRaw(f, false)
func (s Set) Do(f func()) error {
	return s.DoRaw(func() bool {
		f()
		return false
	}, false)
}

// DoRaw performs the given function in the context of the set of namespaces.
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
//
// If the stored namespaces includes a user namespace, then Do is expected to fail.
func (s Set) DoRaw(f func() bool, restore bool) error {
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

			if err := s.set(false); err != nil {
				return fmt.Errorf("error setting namespaces: %w", err)
			}

			if !f() {
				return nil
			}
			if !restore {
				return nil
			}

			if err := cur.set(false); err != nil {
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

func merge(orig Set, newS *Set) (retErr error) {
	if orig.flags == newS.flags {
		return nil
	}

	tmp := make(map[int]int, len(orig.fds)+len(newS.fds))
	defer func() {
		if retErr != nil {
			for _, fd := range tmp {
				sys_close(fd)
			}
		}
	}()

	for kind, fd := range orig.fds {
		if newS.flags&kind != 0 {
			continue
		}

		nfd, err := dup(fd)
		if err != nil {
			return err
		}
		tmp[kind] = nfd
	}

	for kind, fd := range tmp {
		newS.fds[kind] = fd
		newS.flags |= kind
	}

	return nil
}

// Unshare creates a new set with the namespaces specified in `flags` unshared (i.e. new namespaces are created).
//
// This does not change the current set of namespaces, it only creates a new set of namespaces that
// can be used later with the returned `Set`, e.g. `newSet.Do(func() { ... })`.
//
// If CLONE_NEWUSER is specified, the Set will be unable to be used with `Do`.
// This is because the user namespace can only be created (which is done using `clone(2)`) and not joined from a multi-threaded process.
// The forked process is used to create the user namespace and any other namespaces specified in `flags`.
// You can use `Do` by calling `Dup` on the set and dropping CLONE_NEWUSER from the flags.
func (s Set) Unshare(flags int) (Set, error) {
	type result struct {
		s   Set
		err error
	}

	restore := restorable(flags)

	ch := make(chan result)
	go func() {
		newS, err := func() (_ Set, retErr error) {
			if flags&unix.CLONE_NEWUSER != 0 || s.flags&unix.CLONE_NEWUSER != 0 {
				// If we are creating a new user namespace, we need to fork a new process
				// If the Set already contains a user namespace and we are not creating a new one, then we also need to join the user namespace before creating the new namespaces.
				// This ensures the new namespaces are bouond to the user namespace.
				usernsFd := -1
				if s.flags&unix.CLONE_NEWUSER != 0 && flags&unix.CLONE_NEWUSER == 0 {
					usernsFd = s.fds[unix.CLONE_NEWUSER]
				}
				if err := s.set(true); err != nil {
					return Set{}, err
				}
				newS, err := doClone(flags, usernsFd)
				if err != nil {
					return Set{}, err
				}
				if err := merge(s, &newS); err != nil {
					newS.Close()
					return Set{}, err
				}
				return newS, nil
			}

			runtime.LockOSThread()
			defer func() {
				// Only unlock this thread if there are no errors.
				// Additionally should not unlock threads that have had non-reversiable changes made to them.
				if retErr == nil && restore {
					runtime.UnlockOSThread()
				}
			}()

			if err := unshare(flags); err != nil {
				return Set{}, fmt.Errorf("error unsharing namespaces: %w", err)
			}

			newS, err := curNamespaces(flags)
			if err != nil {
				return Set{}, fmt.Errorf("error getting namespaces: %w", err)
			}

			// Try to restore this thread so it can be re-used be go.
			if restore {
				if err := s.set(false); err != nil {
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

	for kind, fd := range s.fds {
		name := nsFlagsReverse[kind]

		f, err := os.Create(filepath.Join(target, name))
		if err != nil {
			return fmt.Errorf("error creating target file for %s: %w", name, err)
		}
		f.Close()

		if err := mount(fmt.Sprintf("/proc/self/fd/%d", fd), f.Name(), false); err != nil {
			return fmt.Errorf("error mounting %s: %w", name, err)
		}
	}

	return err
}

// FromDir creates a set of namespaces from the specified directory.
// As an example, you could use the `Set.Mount` function and then use this to create a new set from those mounts.
// Or you can even point directly at /proc/<pid>/ns.
func FromDir(dir string, flags int) (_ Set, retErr error) {
	s := Set{flags: flags, fds: make(map[nsFlag]int)}
	defer func() {
		if retErr != nil {
			s.Close()
		}
	}()

	for kind, name := range nsFlagsReverse {
		if flags&kind == 0 {
			continue
		}

		p := filepath.Join(dir, name)
		f, err := open(p)
		if err != nil {
			return Set{}, fmt.Errorf("error opening %s: %w", name, err)
		}

		s.fds[kind] = f
	}

	return s, nil
}

// FromPid returns a `Set` for the given pid and namespace flags.
func FromPid(pid int, flags int) (Set, error) {
	return FromDir(fmt.Sprintf("/proc/%d/ns", pid), flags)
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
	nsFlags = map[string]nsFlag{
		"cgroup": unix.CLONE_NEWCGROUP,
		"ipc":    unix.CLONE_NEWIPC,
		"mnt":    unix.CLONE_NEWNS,
		"net":    unix.CLONE_NEWNET,
		"pid":    unix.CLONE_NEWPID,
		"time":   unix.CLONE_NEWTIME,
		"user":   unix.CLONE_NEWUSER,
		"uts":    unix.CLONE_NEWUTS,
	}

	nsFlagsReverse = map[nsFlag]string{
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

	s.fds = make(map[nsFlag]int, len(nsFlags))
	s.flags = flags

	for name, flag := range nsFlags {
		if flags&flag == 0 {
			continue
		}
		fd, err := open(filepath.Join("/proc/thread-self/ns", name))
		if err != nil {
			return Set{}, fmt.Errorf("error opening namespace file: %w", err)
		}
		s.fds[flag] = fd
	}

	return s, nil
}
