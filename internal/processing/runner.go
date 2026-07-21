package processing

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"sync/atomic"
	"syscall"
	"time"
)

var (
	ErrCommandOutputLimit = errors.New("processing command output limit exceeded")
	ErrCommandFailed      = errors.New("processing command failed")
)

type Runner interface {
	Run(context.Context, string, []string, int64, int64) ([]byte, []byte, int, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, executable string, args []string, stdoutLimit, stderrLimit int64) ([]byte, []byte, int, error) {
	if executable == "" || stdoutLimit < 1 || stderrLimit < 1 {
		return nil, nil, -1, ErrCommandFailed
	}
	commandCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, executable, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		return nil
	}
	var exceeded atomic.Bool
	stdout := &boundedBuffer{limit: stdoutLimit, exceeded: &exceeded, cancel: cancel}
	stderr := &boundedBuffer{limit: stderrLimit, exceeded: &exceeded, cancel: cancel}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	if exceeded.Load() {
		return stdout.Bytes(), stderr.Bytes(), exitCode(err), ErrCommandOutputLimit
	}
	if ctx.Err() != nil {
		return stdout.Bytes(), stderr.Bytes(), exitCode(err), ctx.Err()
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return stdout.Bytes(), stderr.Bytes(), exitErr.ExitCode(), nil
	}
	if err != nil {
		return stdout.Bytes(), stderr.Bytes(), -1, ErrCommandFailed
	}
	return stdout.Bytes(), stderr.Bytes(), 0, nil
}

func exitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

type boundedBuffer struct {
	buffer   bytes.Buffer
	limit    int64
	exceeded *atomic.Bool
	cancel   context.CancelFunc
}

func (w *boundedBuffer) Write(p []byte) (int, error) {
	remaining := w.limit - int64(w.buffer.Len())
	if remaining <= 0 {
		w.exceeded.Store(true)
		w.cancel()
		return len(p), nil
	}
	write := p
	if int64(len(write)) > remaining {
		write = write[:remaining]
		w.exceeded.Store(true)
		w.cancel()
	}
	_, _ = w.buffer.Write(write)
	return len(p), nil
}
func (w *boundedBuffer) Bytes() []byte { return append([]byte(nil), w.buffer.Bytes()...) }
