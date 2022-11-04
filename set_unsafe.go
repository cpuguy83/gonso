package gonso

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

func doClone(flags int) (Set, error) {
	type result struct {
		s   Set
		err error
	}

	ch := make(chan result, 1)
	go func() {
		runtime.LockOSThread()

		var pipe [2]int
		if err := make_pipe(pipe[:]); err != nil {
			ch <- result{err: fmt.Errorf("error creating pipe: %w", err)}
			return
		}

		buf := make([]byte, 1)
		_p0 := unsafe.Pointer(&buf[0])

		pid, errno := sys_clone(flags)
		if errno != 0 {
			ch <- result{err: fmt.Errorf("error calling clone: %w", errno)}
			sys_close(pipe[1])
			sys_close(pipe[0])
			return
		}
		if pid == 0 {
			// child process
			_, _, errno := unix.RawSyscall(unix.SYS_READ, uintptr(pipe[0]), uintptr(_p0), uintptr(len(buf)))
			syscall.RawSyscall(unix.SYS_EXIT, uintptr(errno), 0, 0)
			return
		}

		defer func() {
			sys_close(pipe[0])
			sys_close(pipe[1])
			kill(int(pid))
			waitid(int(pid))
		}()

		set, err := FromDir(fmt.Sprintf("/proc/%d/ns", pid), flags)
		ch <- result{s: set, err: err}
	}()

	r := <-ch
	return r.s, r.err
}
