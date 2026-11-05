package service

import (
	"strings"
	"sync"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// 体检阶段名与官方总体架构图（docs/assets/images 下的「aegisops Kubernetes 体检诊断总体架构图.png」）
// 的九个方框一一对应。代码、报告「诊断信息」区块、Web 进度条三处用同一套名字，
// 这样报告里写的阶段和用户屏幕上看到的阶段可以互相对账。
const (
	// stageCollect..stageReport 对应架构图的九个分析方框；connect/write/archive 是图外的
	// 接入与产物步骤，进度条要把它们放在正确位置，所以一并进表。
	stageConnect       = "connect"        // 连接集群（分析开始前）
	stageCollect       = "collect"        // 资源采集
	stageRules         = "rules"          // 规则引擎
	stageEvidence      = "evidence"       // 按需证据采集
	stageRelations     = "relations"      // 资源关系分析
	stageCorrelate     = "correlate"      // 关联分析
	stageIncident      = "incident"       // 故障聚合
	stageDeterministic = "deterministic"  // 确定性诊断
	stageLLM           = "llm"            // LLM 增强（可选）
	stageValidate      = "validate"       // 结果校验
	stageReport        = "report"         // 报告生成
	stageWrite         = "write"          // 本地落盘（可选）
	stageArchive       = archiveScanPhase // 归档到对象存储（可选）
)

// serverVersionLabel 统一「连接集群」这一步的版本展示口径：client-go 的 GitVersion 本身
// 带 v 前缀，再拼一次就会在进度条上显示成 vv1.28.13。
func serverVersionLabel(version string) string {
	if version == "" || strings.HasPrefix(version, "v") {
		return version
	}
	return "v" + version
}

// stageSpec 是阶段的展示标题与固定顺序（报告按这个顺序渲染）。
var stageSpec = []struct{ name, title string }{
	{stageConnect, "连接集群"},
	{stageCollect, "资源采集"},
	{stageRules, "规则引擎"},
	{stageEvidence, "按需证据采集"},
	{stageRelations, "资源关系分析"},
	{stageCorrelate, "关联分析"},
	{stageIncident, "故障聚合"},
	{stageDeterministic, "确定性诊断"},
	{stageLLM, "LLM 增强诊断"},
	{stageValidate, "结果校验"},
	{stageReport, "报告生成"},
	{stageWrite, "写入报告文件"},
	{stageArchive, "归档报告到对象存储"},
}

// stageTitle 取阶段的展示名；未知阶段返回空串，由调用方决定怎么兜底。
func stageTitle(name string) string {
	for _, spec := range stageSpec {
		if spec.name == name {
			return spec.title
		}
	}
	return ""
}

// stageRecorder 记录每个阶段的真实执行情况，同时转发进度事件。
//
// 终端/Web 看到的和报告里存的是同一份事实。这里刻意不记百分比：阶段耗时测得准，
// 「还剩多少」测不准，宁可不给也不编。
type stageRecorder struct {
	prog model.ScanProgress

	mu     sync.Mutex
	order  []string
	byName map[string]*model.StageRecord
	titles map[string]string
}

func newStageRecorder(prog model.ScanProgress) *stageRecorder {
	return &stageRecorder{
		prog:   prog,
		byName: map[string]*model.StageRecord{},
		titles: map[string]string{},
	}
}

// start 标记阶段开始并转发进度事件；返回值交给 done 计算耗时。
func (r *stageRecorder) start(name, title string) time.Time {
	now := time.Now()
	r.mu.Lock()
	if _, ok := r.byName[name]; !ok {
		r.order = append(r.order, name)
		r.byName[name] = &model.StageRecord{Name: name, Title: title, Status: "running"}
	}
	rec := r.byName[name]
	rec.Title = title
	rec.Status = "running"
	rec.Detail = ""
	rec.Progress = ""
	r.titles[name] = title
	r.mu.Unlock()

	if r.prog != nil {
		r.prog.PhaseStarted(name, title)
	}
	r.publish()
	return now
}

// done 结束阶段；ok=false 时状态记为 failed，不伪装成 done。
func (r *stageRecorder) done(name, detail string, ok bool, started time.Time) {
	elapsed := time.Since(started)
	r.mu.Lock()
	rec, exists := r.byName[name]
	if !exists {
		r.order = append(r.order, name)
		rec = &model.StageRecord{Name: name}
		r.byName[name] = rec
	}
	rec.Status = "done"
	if !ok {
		rec.Status = "failed"
	}
	rec.Detail = detail
	rec.Elapsed = elapsed.Round(time.Millisecond).String()
	rec.Millis = elapsed.Milliseconds()
	r.mu.Unlock()

	if r.prog != nil {
		r.prog.PhaseDone(name, detail, ok, elapsed)
	}
	r.publish()
}

// sub 转发子步骤进度（LLM 逐 Incident 诊断走这里）。
func (r *stageRecorder) sub(name string, index, total int, text string) {
	r.mu.Lock()
	if rec, ok := r.byName[name]; ok {
		rec.Progress = text
	}
	r.mu.Unlock()
	if r.prog != nil {
		r.prog.PhaseSub(name, index, total, text)
	}
	r.publish()
}

// skip 记录「本次不执行」的阶段并给出原因；已经真实跑过的阶段不会被覆盖。
func (r *stageRecorder) skip(name, reason string) {
	r.mu.Lock()
	title := r.titles[name]
	if rec, ok := r.byName[name]; ok {
		if rec.Status == "running" || (rec.Status != "" && rec.Status != "skipped") {
			r.mu.Unlock()
			return
		}
		rec.Status = "skipped"
		rec.Detail = reason
		r.mu.Unlock()
		return
	}
	if title == "" {
		title = stageTitle(name)
	}
	if title == "" {
		title = name
	}
	r.order = append(r.order, name)
	r.byName[name] = &model.StageRecord{Name: name, Title: title, Status: "skipped", Detail: reason}
	r.mu.Unlock()

	if r.prog != nil {
		r.prog.PhaseStarted(name, title)
		r.prog.PhaseDone(name, reason, true, 0)
	}
	r.publish()
}

// publish 把阶段账快照推给可选的 model.StageSink。
//
// 必须在未持锁时调用：回调是外部代码，抱着自己的锁调它等于把死锁的钥匙交给别人。
func (r *stageRecorder) publish() {
	sink, ok := r.prog.(model.StageSink)
	if !ok || r.prog == nil {
		return
	}
	sink.StagesUpdated(r.records())
}

// records 按架构图顺序输出阶段账；本次没走到的阶段补 skipped。
func (r *stageRecorder) records() []model.StageRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]model.StageRecord, 0, len(r.order)+len(stageSpec))
	seen := map[string]bool{}
	for _, spec := range stageSpec {
		if rec, ok := r.byName[spec.name]; ok {
			if rec.Title == "" {
				rec.Title = spec.title
			}
			out = append(out, *rec)
			seen[spec.name] = true
		}
	}
	for _, name := range r.order {
		if seen[name] {
			continue
		}
		out = append(out, *r.byName[name])
	}
	return out
}
