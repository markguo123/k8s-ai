package service

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// stageProbe 同时实现 model.ScanProgress 与 model.StageSink，用来核对「事件流」与
// 「阶段账快照」两条输出是否说的是同一件事。
type stageProbe struct {
	mu        sync.Mutex
	events    []string
	snapshots [][]model.StageRecord
}

func (p *stageProbe) PhaseStarted(name, title string) {
	p.record("start:" + name)
}

func (p *stageProbe) PhaseSub(name string, index, total int, detail string) {
	p.record("sub:" + name)
}

func (p *stageProbe) PhaseDone(name, detail string, ok bool, elapsed time.Duration) {
	p.record("done:" + name)
}

func (p *stageProbe) StagesUpdated(stages []model.StageRecord) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.snapshots = append(p.snapshots, stages)
}

func (p *stageProbe) record(s string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, s)
}

func (p *stageProbe) last() []model.StageRecord {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.snapshots) == 0 {
		return nil
	}
	return p.snapshots[len(p.snapshots)-1]
}

func stageByName(t *testing.T, stages []model.StageRecord, name string) model.StageRecord {
	t.Helper()
	for _, s := range stages {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("阶段账里没有 %s：%+v", name, stages)
	return model.StageRecord{}
}

// TestStageRecorderPublishesSnapshots 锁 P9.2 的机制本身：每变一次推一份全量快照，
// 页面轮询时永远能拿到「当前这一份」，而不是要靠端侧自己拼事件。
func TestStageRecorderPublishesSnapshots(t *testing.T) {
	probe := &stageProbe{}
	rec := newStageRecorder(probe)

	started := rec.start(stageCollect, "资源采集")
	if got := stageByName(t, probe.last(), stageCollect); got.Status != "running" {
		t.Fatalf("开始后的状态 = %s，期望 running", got.Status)
	}
	rec.sub(stageCollect, 2, 5, "Pods")
	rec.done(stageCollect, "共 42 个对象", true, started)

	last := probe.last()
	done := stageByName(t, last, stageCollect)
	if done.Status != "done" || done.Detail != "共 42 个对象" {
		t.Fatalf("结束后的阶段账 = %+v", done)
	}
	if done.Millis < 0 || done.Elapsed == "" {
		t.Fatalf("耗时没记进阶段账：%+v", done)
	}
	// 快照里带上了子步骤描述，页面上能看到「正在采 Pods」。
	if stageByName(t, last, stageCollect).Progress == "" {
		t.Errorf("子步骤描述没进阶段账：%+v", last)
	}
	if len(probe.snapshots) < 3 {
		t.Errorf("快照推送次数 = %d，期望每次变化都推", len(probe.snapshots))
	}
}

// TestStageRecorderOrderAndSkipped 锁报告的形状：阶段按架构图顺序出，本次不做的补
// skipped 并写清原因，绝不让「没做」在报告里长得像「做完了」。
func TestStageRecorderOrderAndSkipped(t *testing.T) {
	rec := newStageRecorder(nil)
	collect := rec.start(stageCollect, "资源采集")
	rec.done(stageCollect, "12 个对象", true, collect)
	rules := rec.start(stageRules, "规则引擎")
	rec.done(stageRules, "命中 3 条", true, rules)
	rec.skip(stageEvidence, "本次不采集日志")
	llm := rec.start(stageLLM, "LLM 增强诊断")
	rec.done(stageLLM, "未配置模型，跳过", true, llm)
	rec.skip(stageReport, "上游失败，未生成报告")

	stages := rec.records()
	// 只出真实走过的阶段：跑到哪一步页面就显示到哪一步，一上来把十步全标成 skipped
	// 会让人以为扫描做完了。
	if len(stages) != 5 {
		t.Fatalf("阶段账条数 = %d，期望 5：%+v", len(stages), stages)
	}
	want := []string{stageCollect, stageRules, stageEvidence, stageLLM, stageReport}
	for i, name := range want {
		if stages[i].Name != name {
			t.Fatalf("第 %d 条 = %s，期望 %s（阶段必须按架构图顺序）", i, stages[i].Name, name)
		}
	}
	if got := stageByName(t, stages, stageEvidence); got.Status != "skipped" || got.Detail != "本次不采集日志" {
		t.Fatalf("跳过的证据采集阶段 = %+v", got)
	}
	if got := stageByName(t, stages, stageReport); got.Status != "skipped" {
		t.Fatalf("没跑过的阶段应补 skipped：%+v", got)
	}
	if got := stageByName(t, stages, stageCollect); got.Title != "资源采集" {
		t.Fatalf("标题丢失：%+v", got)
	}
}

// TestStageRecorderFailureStaysFailed 是「不伪装成功」这条红线：ok=false 只能变 failed。
func TestStageRecorderFailureStaysFailed(t *testing.T) {
	rec := newStageRecorder(nil)
	started := rec.start(stageRules, "规则引擎")
	rec.done(stageRules, "规则执行报错", false, started)
	rec.skip(stageRules, "已跳过")

	got := stageByName(t, rec.records(), stageRules)
	if got.Status != "failed" {
		t.Fatalf("失败阶段被改写成 %s，期望 failed", got.Status)
	}
}

// TestServerVersionLabelKeepsSinglePrefix 锁连接阶段那行版本描述：GitVersion 自带 v，
// 代码再拼一次就会显示成 vv1.28.13。
func TestServerVersionLabelKeepsSinglePrefix(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"v1.28.13", "v1.28.13"},
		{"1.30.0", "v1.30.0"},
		{"1.29.4-eks-6e7f99", "v1.29.4-eks-6e7f99"},
		{"", ""},
	} {
		if got := serverVersionLabel(tc.in); got != tc.want {
			t.Errorf("serverVersionLabel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestStageRecorderNeverInventsPercentage 锁需求 §6：进度只说「在做什么」，不编百分比。
func TestStageRecorderNeverInventsPercentage(t *testing.T) {
	rec := newStageRecorder(nil)
	started := rec.start(stageLLM, "LLM 增强诊断")
	rec.sub(stageLLM, 1, 3, "诊断 incident INC-1")
	rec.done(stageLLM, "3 份诊断", true, started)

	for _, s := range rec.records() {
		if strings.Contains(s.Progress, "%") || strings.Contains(s.Detail, "%") {
			t.Fatalf("阶段账里出现了百分比：%+v", s)
		}
	}
}

// TestStageRecorderOrdersOuterStagesAndLabelsSkips 锁真机验证抓到的两个展示缺陷：
// connect/write/archive 不在固定顺序表里时，「连接集群」会被排到全部分析阶段之后；
// 从未 start 过的跳过阶段没有标题，进度条上会直接露出 evidence / llm / write 这类英文 id。
func TestStageRecorderOrdersOuterStagesAndLabelsSkips(t *testing.T) {
	rec := newStageRecorder(nil)
	conn := rec.start(stageConnect, "连接 Kubernetes 集群并校验连通性")
	rec.done(stageConnect, "v1.28.13", true, conn)
	rec.skip(stageEvidence, "本次档位不采集容器日志")
	rec.skip(stageLLM, "未启用 LLM")
	rec.skip(stageWrite, "report-mode=none：不落盘本地报告")
	rep := rec.start(stageReport, "报告生成")
	rec.done(stageReport, "结论=attention", true, rep)
	arch := rec.start(stageArchive, "归档报告到对象存储")
	rec.done(stageArchive, "objects=1", true, arch)

	stages := rec.records()
	// 固定顺序：连接 -> 分析阶段 -> 报告生成 -> 本地落盘 -> 归档。
	want := []string{stageConnect, stageEvidence, stageLLM, stageReport, stageWrite, stageArchive}
	if len(stages) != len(want) {
		t.Fatalf("阶段账条数 = %d，期望 %d：%+v", len(stages), len(want), stages)
	}
	for i, name := range want {
		if stages[i].Name != name {
			t.Fatalf("第 %d 条 = %s，期望 %s：%+v", i, stages[i].Name, name, stages)
		}
	}
	for _, s := range stages {
		if s.Title == "" || s.Title == s.Name {
			t.Errorf("阶段 %s 没有中文展示名：%+v", s.Name, s)
		}
	}
}
