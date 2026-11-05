package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/k8s-ai/k8s-ai/internal/session"
)

// 会话正文容量闸门（P8.6 / ADR-046 / D-35）。
//
// 一期到三期把会话整篇序列化进 sessions.data，MySQL 侧曾在真机撞出
// 「Error 1406 Data too long」：写失败发生在驱动层，用户看到的是整轮对话记录无声消失。
// P8.6 的口径是「有上限、有预警、拒写得人话」：达上限的 85% 发预警，超上限直接拒写并
// 说明原因，绝不做静默截断（AGENTS.md：不允许假实现）。
//
// 判定只认序列化后的字节数，且放在写入唯一漏斗上，因此 Create、Sessions().Update、
// Tx.UpdateSession 三条路都过不去同一个闸门。

// ErrSessionTooLarge 表示单会话正文超过 agent.session_max_bytes 上限，本次写入被拒绝。
var ErrSessionTooLarge = errors.New("session data exceeds size limit")

// DefaultSessionMaxBytes 是兜底上限。上限的「主人」是 internal/config
// （agent.session_max_bytes，默认 12MiB），装配层用 WithSessionMaxBytes 把有效值传进来；
// 这个常量只保护「直接用 store 而没经过配置装配」的调用方（含测试与 CLI 旁路）。
const DefaultSessionMaxBytes = int64(12 << 20)

// sessionMemoryWarningPercent 是预警比例，与 config.SessionMemoryWarningPercent 同值，
// 两处一致性由 config 侧与 store 侧的测试分别锁住。
const sessionMemoryWarningPercent = 85

// SessionSizeReport 是一次会话写入的容量状况（与 session.SizeReport 同字段，
// store 侧不反向依赖 session 包的能力类型，由 sessionsync 做转换）。
type SessionSizeReport struct {
	Size    int64
	Limit   int64
	Warning bool
}

// SessionSizeStore 是 SessionStore 的可选能力：更新会话并回报容量状况。
type SessionSizeStore interface {
	UpdateSized(ctx context.Context, sess *session.Session) (SessionSizeReport, error)
}

// evaluateSessionSize 是唯一的容量判定：超限返回包装 ErrSessionTooLarge 的可读错误，
// 达预警比例则在报告里置 Warning。limit <= 0 时退回兜底默认值。
func evaluateSessionSize(size, limit int64) (SessionSizeReport, error) {
	if limit <= 0 {
		limit = DefaultSessionMaxBytes
	}
	rep := SessionSizeReport{Size: size, Limit: limit}
	if size > limit {
		return rep, fmt.Errorf(
			"%w: 会话正文已达 %s，超过上限 %s（配置键 agent.session_max_bytes）。本轮对话未保存，"+
				"请新建会话继续，或调大该上限（MySQL 单行上限 16MiB，再大请改用 PVC 上的 dir 后端）",
			ErrSessionTooLarge, FormatBytes(size), FormatBytes(limit))
	}
	rep.Warning = size*100 >= limit*int64(sessionMemoryWarningPercent)
	return rep, nil
}

// sessionIndexSnapshot 从落库副本推导索引镜像列（message_count / title）。
//
// 标题必须重算而不是照抄入参：入参的 Title 可能来自上一次读取的快照，正文这时已经变了。
// 例外是「正文缺席但镜像有值」——那是从列表读来的会话（List 不读正文），此时照抄既有
// 镜像，否则一次无关的 Update 就会把标题和条数抹平。
func sessionIndexSnapshot(row *session.Session) (messageCount int, title string) {
	if len(row.Messages) == 0 && row.MessageCount > 0 {
		return row.MessageCount, row.Title
	}
	probe := *row
	probe.Title = ""
	return len(row.Messages), probe.DisplayTitle()
}

// FormatBytes 给人看的体积写法：整数走整 MiB，其余留一位小数。
func FormatBytes(n int64) string {
	const unit = int64(1 << 20)
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value := float64(n) / float64(unit)
	if value == float64(int64(value)) {
		return fmt.Sprintf("%d MiB", int64(value))
	}
	return fmt.Sprintf("%.1f MiB", value)
}
