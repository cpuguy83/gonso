package gonso

import (
	"context"
	"fmt"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func doClone(flags, usernsFd int) (Set, error) {
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

		beforeFork()
		pid, _, errno := unix.RawSyscall6(unix.SYS_CLONE, uintptr(unix.SIGCHLD)|unix.CLONE_CLEAR_SIGHAND|unix.CLONE_FILES|uintptr(flags), 0, 0, 0, 0, 0)
		if errno != 0 {
			afterFork()
			ch <- result{err: fmt.Errorf("error calling clone: %w", errno)}
			sys_close(pipe[1])
			sys_close(pipe[0])
			return
		}
		if pid == 0 {
			// child process
			if usernsFd >= 0 {
				_, _, errno := syscall.RawSyscall(unix.SYS_SETNS, uintptr(usernsFd), uintptr(unix.CLONE_NEWUSER), 0)
				if errno != 0 {
					syscall.RawSyscall(unix.SYS_EXIT_GROUP, uintptr(errno), 0, 0)
				}
			}

			// block until the parent process closes this fd
			unix.RawSyscall(unix.SYS_READ, uintptr(pipe[0]), uintptr(_p0), uintptr(len(buf)))
			syscall.RawSyscall(unix.SYS_EXIT_GROUP, uintptr(0), 0, 0)
			panic("unreachable")
		}

		afterFork()

		set, err := FromDir(fmt.Sprintf("/proc/%d/ns", pid), flags)

		sys_close(pipe[0])
		sys_close(pipe[1])

		chExit := make(chan error, 1)
		go func() {
			code, err := wait(int(pid))
			if err != nil {
				chExit <- fmt.Errorf("error waiting for child: %w", err)
				return
			}
			if code != 0 {
				chExit <- fmt.Errorf("child exited with code %d: %w", code, unix.Errno(code))
				return
			}
			chExit <- nil
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		select {
		case <-ctx.Done():
			kill(int(pid))
			err2 := <-chExit
			if err == nil {
				err = err2
			}
		case err2 := <-chExit:
			if err2 != nil {
				err = err2
			}
		}

		ch <- result{s: set, err: err}
	}()

	r := <-ch
	return r.s, r.err
}
