package gonso

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"testing"

	"golang.org/x/sys/unix"
)

func TestMain(m *testing.M) {
	if !checkCapSysAdmin() {
		fmt.Fprintln(os.Stderr, "CAP_SYS_ADMIN is required to run, re-execing with sudo")
		// run with sudo
		cmd := exec.Command("sudo", os.Args...)
		cmd.Env = os.Environ()
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			os.Exit(cmd.ProcessState.ExitCode())
		}
		return
	}

	os.Exit(m.Run())
}

func checkCapSysAdmin() bool {
	ch := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		ch <- unix.Unshare(unix.CLONE_NEWIPC)
	}()

	if err := <-ch; err != nil {
		// we could check EPERM here but it really doesn't matter.
		return false
	}

	return true
}
