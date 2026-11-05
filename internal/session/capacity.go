package session

// 会话正文容量能力（P8.6 / ADR-046 / D-35）。
//
// 判定与拒写发生在持久化层（internal/store 的会话写入路径），本包只定义「写一次正文
// 顺带把容量状况带回来」的可选能力，避免 session 包反向依赖 store。会话管理器与 Chat
// 服务用类型断言取用：文件/内存后端没有容量闸门，断言失败就走普通 Update。

// SizeReport 是一次会话正文写入的容量状况。
type SizeReport struct {
	// Size 是本次落库的正文序列化字节数。
	Size int64
	// Limit 是判定所用的上限（有效配置值）。
	Limit int64
	// Warning 表示正文已达上限的预警比例：写成功了，但快满了。
	Warning bool
}

// SizedStore 是 Store 的可选能力：更新会话并回报正文容量。
type SizedStore interface {
	UpdateSized(s *Session) (SizeReport, error)
}
