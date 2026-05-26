//go:build !windows

package ffmpegutil

import (
	"context"
	"os/exec"
)

func HiddenCmd(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}
