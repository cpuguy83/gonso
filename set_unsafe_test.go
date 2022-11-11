package gonso

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

type pipes struct {
	in  *os.File
	out *os.File
	err *os.File
}

func (p pipes) Close() {
	p.in.Close()
	p.out.Close()
	p.err.Close()
}

func (s Set) testDoRexec(t *testing.T, cmd string, argv []string, envv []string) (<-chan int, func(), pipes, error) {
	type result struct {
		pid int
		err error
	}
	ch := make(chan result, 1)

	var stdio pipes
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdio.in = stdinW
	defer stdinR.Close()

	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdoutW.Close()
	stdio.out = stdoutR

	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stderrW.Close()
	stdio.err = stderrR

	stdinFd := int(stdinR.Fd())
	stdoutFd := int(stdoutW.Fd())
	stderrFd := int(stderrW.Fd())

	go func() {
		runtime.LockOSThread()

		args := append([]string{cmd}, argv...)

		argv0p, err := syscall.BytePtrFromString("/proc/self/exe")
		if err != nil {
			ch <- result{err: fmt.Errorf("error converting argv[0] to string: %w", err)}
			return
		}
		argvp, err := syscall.SlicePtrFromStrings(append([]string{cmd}, args...))
		if err != nil {
			ch <- result{err: fmt.Errorf("error converting argv to string: %w", err)}
			return
		}
		envvp, err := syscall.SlicePtrFromStrings(envv)
		if err != nil {
			ch <- result{err: fmt.Errorf("error converting envv to string: %w", err)}
			return
		}

		type namespace struct {
			fd   int
			kind int
		}
		var fds []namespace
		for kind, fd := range s.fds {
			fds = append(fds, namespace{fd: fd, kind: kind})
		}

		beforeFork()
		pid, _, errno := unix.RawSyscall6(unix.SYS_CLONE, uintptr(unix.SIGCHLD), 0, 0, 0, 0, 0)
		if errno != 0 {
			afterFork()
			ch <- result{err: fmt.Errorf("error calling clone: %w", errno)}
			return
		}

		if pid == 0 {
			if _, _, errno := unix.RawSyscall(unix.SYS_DUP2, uintptr(stdinFd), uintptr(0), 0); errno != 0 {
				syscall.RawSyscall(unix.SYS_EXIT_GROUP, uintptr(errno), 0, 0)
			}
			if _, _, errno := unix.RawSyscall(unix.SYS_DUP2, uintptr(stdoutFd), uintptr(1), 0); errno != 0 {
				syscall.RawSyscall(unix.SYS_EXIT_GROUP, uintptr(errno), 0, 0)
			}
			if _, _, errno := unix.RawSyscall(unix.SYS_DUP2, uintptr(stderrFd), uintptr(2), 0); errno != 0 {
				syscall.RawSyscall(unix.SYS_EXIT_GROUP, uintptr(errno), 0, 0)
			}

			for _, fd := range fds {
				_, _, errno := syscall.RawSyscall(unix.SYS_SETNS, uintptr(fd.fd), uintptr(fd.kind), 0)
				if errno != 0 {
					syscall.RawSyscall(unix.SYS_EXIT_GROUP, uintptr(errno), 0, 0)
					panic("unreachable")
				}
			}
			_, _, errno = syscall.RawSyscall(unix.SYS_EXECVE, uintptr(unsafe.Pointer(argv0p)), uintptr(unsafe.Pointer(&argvp[0])), uintptr(unsafe.Pointer(&envvp[0])))
			syscall.RawSyscall(unix.SYS_EXIT_GROUP, uintptr(errno), 0, 0)
		}

		afterFork()

		ch <- result{pid: int(pid)}
	}()

	r := <-ch
	if r.err != nil {
		t.Fatal(r.err)
	}

	ret := make(chan int, 1)
	go func() {
		var ws syscall.WaitStatus
		syscall.Wait4(r.pid, &ws, 0, nil)
		ret <- ws.ExitStatus()
	}()

	cancel := func() {
		syscall.Kill(r.pid, syscall.SIGKILL)
	}

	return ret, cancel, stdio, nil
}
