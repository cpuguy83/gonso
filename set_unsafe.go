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
		if err := unix.Pipe2(pipe[:], unix.O_CLOEXEC); err != nil {
			ch <- result{err: fmt.Errorf("error creating pipe: %w", err)}
			return
		}

		buf := make([]byte, 1)
		_p0 := unsafe.Pointer(&buf[0])

		pid, _, errno := unix.RawSyscall6(unix.SYS_CLONE, uintptr(syscall.SIGCHLD)|unix.CLONE_CLEAR_SIGHAND|uintptr(flags), 0, 0, 0, 0, 0)
		if errno != 0 {
			ch <- result{err: fmt.Errorf("error calling clone: %w", errno)}
			unix.Close(pipe[1])
			unix.Close(pipe[0])
			return
		}
		if pid == 0 {
			// child process
			_, _, errno := unix.RawSyscall(unix.SYS_READ, uintptr(pipe[0]), uintptr(_p0), uintptr(len(buf)))
			syscall.RawSyscall(unix.SYS_EXIT, uintptr(errno), 0, 0)
			return
		}

		defer func() {
			unix.Close(pipe[0])
			unix.Close(pipe[1])
			unix.Kill(int(pid), unix.SIGKILL)
			unix.Waitid(unix.P_PID, int(pid), nil, unix.WEXITED, nil)
		}()

		set, err := FromDir(fmt.Sprintf("/proc/%d/ns", pid), flags)
		ch <- result{s: set, err: err}
	}()

	r := <-ch
	return r.s, r.err
}
