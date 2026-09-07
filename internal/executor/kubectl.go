package executor

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/approval"
	"github.com/k8s-ai/k8s-ai/internal/security"
)

// defaultStreamTimeout 是流式命令（logs -f / get -w）的默认采样时长。
// 借鉴 kubectl-ai streaming.go：到时后捕获部分输出并标记 StreamTimeout，不阻塞 Agent Loop。
const defaultStreamTimeout = 7 * time.Second

// redactor 脱敏 kubectl 自由文本输出（FR-018；Redactor 编译正则只读，线程安全）。
var redactor = security.NewRedactor()

// RunKubectl 执行 kubectl 命令：
// deny_ops → 安全红线（secret/--raw）→ 解析（argv 或 shell）→ 动词白名单 →
// exec.Command → 输出截断 + 脱敏。
// isShell=true（命令含管道/heredoc/重定向等 shell 元字符）时走 bash -c；
// 否则走 argv 直调二进制（无 shell）。
// 流式命令（logs -f / get -w）套固定超时，超时后捕获部分输出，不视为错误。
func (r *Runner) RunKubectl(ctx context.Context, command string) (Result, error) {
	if approval.IsDenied(r.denyOps, command) {
		return Result{Command: command}, fmt.Errorf("command denied by deny_ops policy")
	}
	// LLM 数据安全红线：Secret 数据永不进入 LLM 上下文（AGENTS.md）。
	if approval.IsForbiddenCommand(command) {
		return Result{Command: command}, fmt.Errorf("command forbidden: 禁止通过 Agent 读取 Secret 资源或使用 --raw（敏感数据不进入 LLM 上下文）")
	}
	pc, err := ParseCommandDetail(command)
	if err != nil {
		return Result{Command: command}, err
	}
	argv := pc.Argv
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

	var cmd *exec.Cmd
	if pc.IsShell {
		// shell 模式：bash -c <完整命令>，支持 pipe/heredoc/redirect/&&/|| 等。
		// 安全保证：进入此分支的写操作已由 Agent Loop 的 executeTool 走过人工审批；
		// deny_ops 和 Secret 拦截在上方已校验；输出仍统一脱敏。
		cmd = exec.CommandContext(runCtx, "bash", "-c", command)
	} else {
		// argv 模式：直调二进制，无 shell。
		cmd = exec.CommandContext(runCtx, r.bin, argv[1:]...)
	}
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
			Stdout:        redactor.Redact(stdout.String()),
			Stderr:        redactor.Redact(stderr.String()),
			StreamTimeout: true,
			Truncated:     stdout.truncated || stderr.truncated,
		}, nil
	}
	res := Result{
		Command:   command,
		Stdout:    redactor.Redact(stdout.String()),
		Stderr:    redactor.Redact(stderr.String()),
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

// HasShellSyntax 已移至 approval 包以避免循环依赖。
// executor 通过 approval.HasShellSyntax 间接调用（仅在审批分类层使用）。

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
