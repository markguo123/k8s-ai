package service

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/report"
)

// maxStructuredReportBytes 是一次结构化报告读取的正文上限。页面上的报告详情页要把整份
// 报告解成 JSON 渲染，上限既防一次误传的巨型 blob 打爆内存，也给「这份报告是不是哪里
// 写坏了」一条明确的线：正常体检报告在几百 KB 量级，撞到 8 MiB 就是事故不是常态。
const maxStructuredReportBytes = 8 << 20

// ErrReportTooLarge 是正文超过 maxStructuredReportBytes。
var ErrReportTooLarge = errors.New("service: report payload too large")

// ErrReportNotStructured 是报告正文不是结构化体检报告，且同一 scan 也找不到 JSON 产物。
//
// 常见原因是历史报告只归档了 Markdown 一份（P8 之前 CLI 只落 .md）。这时诚实报错比
// 前端拿到半份数据瞎渲染要好。
var ErrReportNotStructured = errors.New("service: report content is not a structured inspection report")

// StructuredReport 是一次体检的结构化视图（需求 §7 统一报告契约）。
type StructuredReport struct {
	// Report 是用户点开的那一行（可能本身是 Markdown 行）。
	Report ReportView
	// Result 是解析后的统一报告；老报告缺的区块是零值，前端按 schemaVersion 分支。
	Result *model.ScanResult
	// SchemaVersion 归一化后的版本号：v1 老报告也报 1，不让调用方去猜 0 是什么。
	SchemaVersion int
	// Legacy 为真表示这是四期之前的 v1 结构（缺结论/采集质量/诊断账等区块）。
	Legacy bool
}

// OpenStructuredReport 按 id 读出结构化体检报告（P9）。
//
// 这里要处理一个 P8 遗留的形状：一次扫描归档成 latest-md 与 latest-json **两行** report，
// 页面列表里点开的是 Markdown 那一行。所以「点开报告看详情」必须能顺着 reports.scan_id
// 找到同一轮的 JSON 行，而不是要求前端先去猜哪一行的 kind 是 latest-json。找不到 JSON
// 产物（只归档了 Markdown 的老报告）时诚实报 ErrReportNotStructured，绝不拿 Markdown
// 正文硬凑一个结构化对象。
//
// 归属判定完全复用 OpenContent/List 的 scope，这里不新增任何授权通道： sibling 查询带的
// 是同一个 scope，越权的人连自己那行的正文都打不开。
func (p *ReportPlatform) OpenStructuredReport(ctx context.Context, id string, scope ReportScope) (StructuredReport, error) {
	view, body, err := p.readReportBody(ctx, id, scope)
	if err != nil {
		return StructuredReport{}, err
	}
	if looksLikeJSON(body) {
		res, legacy, perr := report.ParseReportJSON(body)
		if perr != nil {
			return StructuredReport{}, perr
		}
		return structuredReport(view, res, legacy), nil
	}

	if view.ScanID == "" {
		return StructuredReport{}, fmt.Errorf("%w: report %s has no scan id", ErrReportNotStructured, id)
	}
	sibling := scope
	sibling.ScanID = view.ScanID
	sibling.Kind = ReportKindLatestJSON
	items, _, err := p.List(ctx, sibling, 4, 0)
	if err != nil {
		return StructuredReport{}, err
	}
	for _, item := range items {
		if item.ID == view.ID || !item.Available {
			continue
		}
		_, jsonBody, rerr := p.readReportBody(ctx, item.ID, scope)
		if rerr != nil {
			// 同一轮的 JSON 行读不出来（内容被清、后端抖动）不该把整件事说成「没有结构化报告」：
			// 原样上抛，让 server 映射成 410/503，运维才知道该去找存储而不是去找渲染。
			return StructuredReport{}, rerr
		}
		if !looksLikeJSON(jsonBody) {
			continue
		}
		res, legacy, perr := report.ParseReportJSON(jsonBody)
		if perr != nil {
			return StructuredReport{}, perr
		}
		return structuredReport(view, res, legacy), nil
	}
	return StructuredReport{}, fmt.Errorf("%w: scan %s has no JSON artifact", ErrReportNotStructured, view.ScanID)
}

func structuredReport(view ReportView, res *model.ScanResult, legacy bool) StructuredReport {
	version := res.SchemaVersion
	if version <= 0 {
		version = 1
	}
	return StructuredReport{Report: view, Result: res, SchemaVersion: version, Legacy: legacy}
}

// ExportMarkdown 按需给出一条报告的 Markdown 正文（P9.10）。
//
// 为什么是「按需渲染」而不是「再归档一份 .md」：一次体检同时存 JSON 与 Markdown 两份时，
// 报告列表就会出现两条几乎相同的行，而用户要的只是一份能看的报告。正文只有一个事实源
// （JSON），Markdown 在下载的那一刻由同一套渲染器生成，两边永远不会版本不一致，归档里也
// 不会长出「哪一份才是最新的」这种问题。
//
// 这一行本身就是 Markdown 时原样交回，不做二次解析：老报告（P8 之前只有 .md）本来就没有
// 结构化正文，硬解只会得到一个 ErrReportNotStructured，而用户要的东西就在这一行里。
func (p *ReportPlatform) ExportMarkdown(ctx context.Context, id string, scope ReportScope) (ReportView, []byte, error) {
	view, body, err := p.readReportBody(ctx, id, scope)
	if err != nil {
		return ReportView{}, nil, err
	}
	if !looksLikeJSON(body) {
		return view, body, nil
	}
	res, _, err := report.ParseReportJSON(body)
	if err != nil {
		return ReportView{}, nil, err
	}
	md, err := report.MarkdownRenderer{}.Render(res)
	if err != nil {
		return ReportView{}, nil, fmt.Errorf("render markdown: %w", err)
	}
	return view, md, nil
}

// readReportBody 取正文并做长度闸门。LimitReader 多读 1 字节是用来区分「正好等于上限」
// 与「超过上限」的，否则边界上的合法报告会被误判成超大。
func (p *ReportPlatform) readReportBody(ctx context.Context, id string, scope ReportScope) (ReportView, []byte, error) {
	view, rc, _, err := p.OpenContent(ctx, id, scope)
	if err != nil {
		return ReportView{}, nil, err
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(io.LimitReader(rc, maxStructuredReportBytes+1))
	if err != nil {
		return view, nil, fmt.Errorf("read report body: %w", err)
	}
	if len(body) > maxStructuredReportBytes {
		return view, nil, fmt.Errorf("%w: %d bytes", ErrReportTooLarge, len(body))
	}
	return view, body, nil
}

// looksLikeJSON 只判断「第一个非空白字符是不是 {」，不做语法解析：真正的解析交给
// report.ParseReportJSON，这里只用来决定要不要去找同一轮的 JSON 行。
func looksLikeJSON(body []byte) bool {
	for _, b := range body {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		case 0xEF, 0xBB, 0xBF:
			continue
		case '{':
			return true
		default:
			return false
		}
	}
	return false
}
