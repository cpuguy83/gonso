package gonso

import (
	"golang.org/x/sys/unix"
)

func memfd() (int, error) {
	for {
		fd, err := unix.MemfdCreate("gonso", unix.MFD_CLOEXEC)
		if err == nil {
			return fd, nil
		}
		if err != unix.EINTR {
			return -1, err
		}
	}
}

func dup(fd int) (int, error) {
	nfd, err := memfd()
	if err != nil {
		return -1, err
	}
	for {
		err := unix.Dup3(fd, nfd, unix.O_CLOEXEC)
		if err == nil {
			return nfd, nil
		}
		if err != unix.EINTR {
			return -1, err
		}
	}
}

func open(p string) (int, error) {
	for {
		fd, err := unix.Open(p, unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if err == nil {
			return fd, nil
		}
		if err != unix.EINTR {
			return -1, err
		}
	}
}

func unshare(flags int) error {
	for {
		err := unix.Unshare(flags)
		if err == nil {
			return nil
		}
		if err != unix.EINTR {
			return err
		}
	}
}

func setns(fd, nstype int) error {
	for {
		err := unix.Setns(fd, nstype)
		if err == nil {
			return nil
		}
		if err != unix.EINTR {
			return err
		}
	}
}

func mount(src, target string, recurisve bool) error {
	flags := unix.MS_BIND
	if recurisve {
		flags |= unix.MS_REC
	}
	for {
		err := unix.Mount(src, target, "none", uintptr(flags), "")
		if err == nil {
			return nil
		}
		if err != unix.EINTR {
			return err
		}
	}
}

func sys_close(fd int) {
	for {
		err := unix.Close(fd)
		if err == nil || err != unix.EINTR {
			return
		}
	}
}

func make_pipe(p []int) error {
	for {
		err := unix.Pipe2(p, unix.O_CLOEXEC)
		if err == nil {
			return nil
		}
		if err != unix.EINTR {
			return err
		}
	}
}

func kill(pid int) {
	for {
		err := unix.Kill(pid, unix.SIGKILL)
		if err == nil || err != unix.EINTR {
			return
		}
	}
}

func waitid(pid int) error {
	for {
		err := unix.Waitid(unix.P_PID, int(pid), nil, unix.WEXITED, nil)
		if err == nil || err != unix.EINTR {
			return err
		}
	}
}

func unmount(p string) {
	for {
		err := unix.Unmount(p, unix.MNT_DETACH)
		if err == nil || err != unix.EINTR {
			return
		}
	}
}

func sys_clone(flags int) (int, unix.Errno) {
	for {
		pid, _, err := unix.RawSyscall6(unix.SYS_CLONE, uintptr(unix.SIGCHLD)|unix.CLONE_CLEAR_SIGHAND|uintptr(flags), 0, 0, 0, 0, 0)
		if err == 0 {
			return int(pid), 0
		}
		if err != unix.EINTR {
			return -1, err
		}
	}
}