package executor

import (
	"bytes"
	"context"
	"fmt"
	"os"
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

// exitShellDisabled 借用 shell 的 126 语义（命令存在但不可执行）：shell 通道被策略
// 关闭时不 fork 进程，所以这不是真实子进程退出码，只是给上层与审计的稳定标记。
const exitShellDisabled = 126

// shellDisabledHint 是关闭态的可指导文案（D-33/ADR-044 v1.1）。首行会被工具层摘要
// 取走（tools.extractKubectlSummary），所以第一句必须自带结论与替代动作。
const shellDisabledHint = "shell 通道已由平台配置关闭（platform.exec.shell_enabled=false）：该实例不提供 bash，" +
	"含管道 |、串联 &&/;、重定向 >、命令替换 $() 或 heredoc 的命令一律不执行。\n" +
	"请改用单条不含 shell 语法的 kubectl 命令（例如 kubectl get pods -A -o json），" +
	"过滤、统计交给结构化只读工具（scan_cluster / get_pod / get_deployment 等）。\n" +
	"确实需要 shell 排障能力时，请让管理员改用默认交付镜像（内置 bash 与 kubectl）" +
	"或把 platform.exec.shell_enabled 置为 true 后重启服务。"

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
	// shell 策略闸门（P6.3 / D-33）：必须在构造 exec.Cmd 之前拦下。
	// 无 shell 镜像变体里 bash 不存在，放行只会让模型收到 "bash: executable file
	// not found" 这种它修不了的环境错误并反复重试；这里给可指导文案，且不起进程。
	// 注意：开启态下 shell 命令能否执行由审批层决定（shell 永不自动执行、
	// 不被 --skip-permissions 免除），本闸门只决定"这个实例有没有解释器"。
	if pc.IsShell && !r.shellEnabled {
		return Result{Command: command, ExitCode: exitShellDisabled, Stderr: shellDisabledHint}, nil
	}
	argv := pc.Argv
	if r.bin == "kubectl" {
		if _, err := exec.LookPath("kubectl"); err != nil {
			// 保留 "kubectl not found" 关键字：tools 层据此识别"环境缺二进制"并回
			// SYSTEM_UNAVAILABLE，避免模型把它当命令写错而反复重试。
			return Result{Command: command}, fmt.Errorf(
				"kubectl not found: PATH 中没有 kubectl（%v）；默认交付镜像内置 kubectl，"+
					"请检查镜像是否为无 shell 变体，或用 kubernetes.kubectl_path 指定二进制路径。"+
					"只读链路（client-go 结构化工具与 scan）不受影响", err)
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
		// deny_ops、Secret 拦截与 shell 通道开关在上方已校验；输出仍统一脱敏。
		cmd = exec.CommandContext(runCtx, shellBinary, "-c", command)
	} else {
		// argv 模式：直调二进制，无 shell。
		cmd = exec.CommandContext(runCtx, r.bin, argv[1:]...)
	}
	stdout := newCappedBuffer(r.maxBytes)
	stderr := newCappedBuffer(r.maxBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = os.Environ()
	if r.kubeconfig != "" {
		cmd.Env = append(cmd.Env, "KUBECONFIG="+r.kubeconfig)
	}
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
