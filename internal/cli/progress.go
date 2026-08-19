package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// spinnerFrames 是终端常用的 Unicode 转轮序列（跨平台终端基本都支持）。
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// isTerminal 检测 f 是否为交互式终端（字符设备）。零依赖实现，替代
// golang.org/x/term.IsTerminal；当 stdout 被管道/重定向时返回 false，
// 防止 spinner 的 \r 覆盖退化为逐行刷屏。
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// terminalProgress 把 ScanProgress 事件渲染成 "⟳ title ➜ detail" 单行实时进度。
// 不依赖第三方 TUI 库，只用 \r 覆盖当前行；CI/非 TTY（isTerm=false）会退化为
// 每行一条开始/结束日志，避免输出大量 \r 字符污染构建日志。
type terminalProgress struct {
	out         io.Writer
	isTerm      bool
	currentLine string // 当前已打印的行文本长度（用于 \r 覆盖后清尾）
	mu          sync.Mutex

	phasesMu sync.Mutex
	phases   map[string]*phaseState // 按阶段名保存运行中状态
	stage    int                    // 阶段序号，用于显示 "1. xxx / 2. yyy"

	// 每阶段独立 spinner ticker：避免所有阶段共用一个 goroutine 泄漏。
	tickerMu sync.Mutex
	tickers  map[string]*time.Ticker
	stops    map[string]chan struct{}
}

type phaseState struct {
	index  int
	title  string
	detail string
	spin   int
	start  time.Time
}

// newTerminalProgress 创建默认 stdout 终端进度渲染器。
// isTerm=true 时启用 spinner + 单行覆盖；false 时走纯日志输出。
func newTerminalProgress(out io.Writer, isTerm bool) *terminalProgress {
	return &terminalProgress{
		out:     out,
		isTerm:  isTerm,
		phases:  map[string]*phaseState{},
		tickers: map[string]*time.Ticker{},
		stops:   map[string]chan struct{}{},
	}
}

// isTerminalStub 目前只按调用方传入 isTerm 判断；二期可引入
// golang.org/x/term 做真正的 fd 检测，这里保持零依赖。
func (p *terminalProgress) PhaseStarted(name, title string) {
	p.phasesMu.Lock()
	p.stage++
	st := &phaseState{index: p.stage, title: title, start: time.Now()}
	p.phases[name] = st
	p.phasesMu.Unlock()

	p.render(name, st, "start")

	if p.isTerm {
		// 启动该阶段的 spinner 刷新 goroutine，80ms 一帧。
		stop := make(chan struct{})
		t := time.NewTicker(80 * time.Millisecond)
		p.tickerMu.Lock()
		p.tickers[name] = t
		p.stops[name] = stop
		p.tickerMu.Unlock()
		go func() {
			for {
				select {
				case <-t.C:
					p.phasesMu.Lock()
					cur, ok := p.phases[name]
					if ok {
						cur.spin = (cur.spin + 1) % len(spinnerFrames)
					}
					p.phasesMu.Unlock()
					if ok {
						p.render(name, cur, "tick")
					}
				case <-stop:
					return
				}
			}
		}()
	}
}

func (p *terminalProgress) PhaseSub(name string, index, total int, detail string) {
	p.phasesMu.Lock()
	st, ok := p.phases[name]
	if !ok {
		p.phasesMu.Unlock()
		return
	}
	if index > 0 && total > 0 {
		st.detail = fmt.Sprintf("[%d/%d] %s", index, total, detail)
	} else {
		st.detail = detail
	}
	p.phasesMu.Unlock()
	p.render(name, st, "sub")
}

func (p *terminalProgress) PhaseDone(name, detail string, ok bool, elapsed time.Duration) {
	// 先停 spinner goroutine
	p.tickerMu.Lock()
	if t, ok := p.tickers[name]; ok {
		t.Stop()
		delete(p.tickers, name)
	}
	if s, ok := p.stops[name]; ok {
		close(s)
		delete(p.stops, name)
	}
	p.tickerMu.Unlock()

	p.phasesMu.Lock()
	st, ok := p.phases[name]
	if !ok {
		p.phasesMu.Unlock()
		return
	}
	delete(p.phases, name)
	p.phasesMu.Unlock()

	line := p.formatDone(st, detail, ok, elapsed)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.isTerm && p.currentLine != "" {
		// 覆盖掉前一行 spinner，再换行
		fmt.Fprint(p.out, "\r"+padRight(line, len(p.currentLine))+"\n")
		p.currentLine = ""
		return
	}
	fmt.Fprintln(p.out, line)
}

// render 单行覆盖模式；非 TTY 下只打印 phase 开始/结束，不打印刷新帧。
func (p *terminalProgress) render(name string, st *phaseState, why string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.isTerm {
		if why == "start" {
			fmt.Fprintf(p.out, "%d. %s …\n", st.index, st.title)
		}
		// "sub" / "tick" 在非 TTY 下不打印，避免刷屏
		return
	}
	frame := ""
	if len(spinnerFrames) > 0 {
		frame = spinnerFrames[st.spin%len(spinnerFrames)]
	}
	line := fmt.Sprintf("%s %d. %s", frame, st.index, st.title)
	if st.detail != "" {
		line += "  ➜ " + st.detail
	}
	if p.currentLine != "" {
		line = padRight(line, len(p.currentLine)) // 清尾
	}
	fmt.Fprint(p.out, "\r"+line)
	p.currentLine = line
}

func (p *terminalProgress) formatDone(st *phaseState, detail string, ok bool, elapsed time.Duration) string {
	icon := "✓"
	tag := "完成"
	if !ok {
		icon = "✗"
		tag = "失败"
	}
	suffix := ""
	elapsed = elapsed.Round(10 * time.Millisecond)
	if detail != "" {
		suffix = fmt.Sprintf("（%s，耗时 %s）", detail, elapsed)
	} else {
		suffix = fmt.Sprintf("（耗时 %s）", elapsed)
	}
	return fmt.Sprintf("%s %d. %s %s%s", icon, st.index, st.title, tag, suffix)
}

func padRight(s string, n int) string {
	if n <= len(s) {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

// 编译期保证 terminalProgress 实现了 ScanProgress。
var _ model.ScanProgress = (*terminalProgress)(nil)
