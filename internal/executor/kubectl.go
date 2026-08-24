package executor

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/approval"
)

// defaultStreamTimeout 是流式命令（logs -f / get -w）的默认采样时长。
// 借鉴 kubectl-ai streaming.go：到时后捕获部分输出并标记 StreamTimeout，不阻塞 Agent Loop。
const defaultStreamTimeout = 7 * time.Second

// RunKubectl 执行 kubectl 命令（argv 直调，无 shell）：
// deny_ops → 解析 → 动词白名单 → exec.Command(kubectl, argv...) → 输出截断。
// 流式命令（logs -f / get -w）套固定超时，超时后捕获部分输出，不视为错误。
func (r *Runner) RunKubectl(ctx context.Context, command string) (Result, error) {
	if approval.IsDenied(r.denyOps, command) {
		return Result{Command: command}, fmt.Errorf("command denied by deny_ops policy")
	}
	argv, err := ParseCommand(command)
	if err != nil {
		return Result{Command: command}, err
	}
	if r.bin == "kubectl" {
		if _, err := exec.LookPath("kubectl"); err != nil {
			return Result{Command: command}, fmt.Errorf("kubectl not found in PATH: %w", err)
		}
	}
	// 流式命令（logs -f / get -w）：套固定超时，捕获部分输出，超时不报错。
	// 交互式流（attach/exec -it）已在 ParseCommand 拒绝，不会进入此分支。
	runCtx := ctx
	var cancel context.CancelFunc
	streaming := IsStreaming(argv)
	if streaming {
		timeout := r.streamTimeout
		if timeout <= 0 {
			timeout = defaultStreamTimeout
		}
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(runCtx, r.bin, argv[1:]...)
	stdout := newCappedBuffer(r.maxBytes)
	stderr := newCappedBuffer(r.maxBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	runErr := cmd.Run()
	// 流式超时：捕获已收到的部分输出，标记 StreamTimeout，不视为错误。
	// 仅当父 ctx 未取消、且我们的超时截止才触发，避免吞掉真实取消。
	if streaming && ctx.Err() == nil && runCtx.Err() == context.DeadlineExceeded {
		return Result{
			Command:       command,
			Stdout:        stdout.String(),
			Stderr:        stderr.String(),
			StreamTimeout: true,
			Truncated:     stdout.truncated || stderr.truncated,
		}, nil
	}
	res := Result{
		Command:   command,
		Stdout:    stdout.String(),
		Stderr:    stderr.String(),
		Truncated: stdout.truncated || stderr.truncated,
	}
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			res.ExitCode = exitErr.ExitCode()
			return res, nil // 非零退出码交给 LLM 分析，不视为调用失败
		}
		return res, runErr
	}
	return res, nil
}

// cappedBuffer 超过上限后丢弃多余字节并标记截断。
type cappedBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func newCappedBuffer(max int) *cappedBuffer {
	if max <= 0 {
		max = 64 * 1024
	}
	return &cappedBuffer{max: max}
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	room := c.max - c.buf.Len()
	if room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
		} else {
			c.buf.Write(p)
		}
	}
	if len(p) > room {
		c.truncated = true
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string {
	return c.buf.String()
}
