package config

// 会话正文容量闸门（P8.6 / ADR-046 / D-35）。
//
// 会话正文不外置到对象桶：内容寻址下整篇外置等于把写放大搬进桶，因此改为给单会话
// 正文设上限。上限本身是「这台平台的形状」，属静态引擎键（不进配置中心动态键），
// 判定与拒写在 internal/store 的会话写入路径落地，配置面只负责给出有效值。
const (
	// DefaultSessionMaxBytes 是单会话正文上限的默认值：12MiB。取值依据是一次大集群
	// 体检（47 命名空间 / 533 Pod）的工具结果约 2MiB 量级，12MiB 容得下连续多轮大
	// 结果而不至于让一行 MySQL 长文本无限膨胀。
	DefaultSessionMaxBytes = int64(12 << 20)

	// defaultSessionMaxBytesText 与 DefaultSessionMaxBytes 同步，供启动/校验提示
	// 用人话复述默认值；两者不一致由 session_capacity_test.go 拦下。
	defaultSessionMaxBytesText = "12MiB"

	// SessionMemoryWarningPercent 是预警阈值百分比：达上限该比例即发可读预警事件，
	// 超上限才拒写。拒写不静默截断（AGENTS.md：不做假实现）。
	SessionMemoryWarningPercent = 85
)

// EffectiveSessionMaxBytes 返回单会话正文上限的有效字节数：未配置（<=0）取默认值，
// 让「没写过这个键」与「显式写了默认值」行为一致。
func (a AgentConfig) EffectiveSessionMaxBytes() int64 {
	if a.SessionMaxBytes <= 0 {
		return DefaultSessionMaxBytes
	}
	return a.SessionMaxBytes
}

// defaultSessionMaxBytes 把常量转成 int64 供 viper 默认值使用。
func defaultSessionMaxBytes() int64 {
	return DefaultSessionMaxBytes
}
