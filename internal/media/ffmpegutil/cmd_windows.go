//go:build windows

package ffmpegutil

import (
	"context"
	"os/exec"
	"syscall"
)

func HiddenCmd(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}
