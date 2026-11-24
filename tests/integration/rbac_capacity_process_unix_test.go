// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build linux || darwin

package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

func verifyRBACCapacityRunningExecutable(ctx context.Context, pid string, file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	current, err := os.Lstat(file.Name())
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(info, current) {
		return errors.New("RBAC native executable pathname changed")
	}
	if runtime.GOOS == "linux" {
		running, err := os.Stat("/proc/" + pid + "/exe")
		if err != nil || !os.SameFile(info, running) {
			return errors.New("RBAC native pathname does not identify the running executable")
		}
		return nil
	}

	output, err := exec.CommandContext(ctx, "lsof", "-nP", "-a", "-p", pid, "-d", "txt", "-F", "fDi").Output()
	if err != nil {
		return fmt.Errorf("RBAC native executable mappings unavailable: %w", err)
	}
	// Match the mapped text vnode, not lsof's pathname, which can be replaced.
	stat := info.Sys().(*syscall.Stat_t)
	var text, haveDevice, haveInode bool
	var device, inode uint64
	for _, field := range strings.Split(string(output), "\n") {
		if field == "" {
			continue
		}
		switch field[0] {
		case 'f':
			text, haveDevice, haveInode = field == "ftxt", false, false
		case 'D':
			device, err = strconv.ParseUint(field[1:], 0, 64)
			haveDevice = err == nil
		case 'i':
			inode, err = strconv.ParseUint(field[1:], 10, 64)
			haveInode = err == nil
		}
		if text && haveDevice && haveInode && device == uint64(uint32(stat.Dev)) && inode == stat.Ino {
			return nil
		}
	}
	return errors.New("RBAC native pathname does not identify the running executable")
}
