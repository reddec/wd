//go:build !windows

package internal

import (
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"
)

func SetCreds(cmd *exec.Cmd, file string) error {
	var stats syscall.Stat_t
	err := syscall.Stat(file, &stats)
	if err != nil {
		return err
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Credential = &syscall.Credential{
		Uid: stats.Uid,
		Gid: stats.Gid,
	}

	if u, err := user.LookupId(strconv.FormatUint(uint64(stats.Uid), 10)); err == nil {
		// might not work in MacOSx with CGO_ENABLED=0 - see https://github.com/golang/go/issues/24383
		cmd.Env = append(cmd.Env, "USER="+u.Username, "HOME="+u.HomeDir)
	}
	return nil
}

func ChownAsFile(path string, file string) error {
	var stats syscall.Stat_t
	err := syscall.Stat(file, &stats)
	if err != nil {
		return err
	}
	return os.Chown(path, int(stats.Uid), int(stats.Gid))
}
