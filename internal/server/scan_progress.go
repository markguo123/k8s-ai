package server

import (
	"time"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// scanStageSink 把扫描流水线推进的阶段账挂到任务上（P9）。
//
// 它刻意只做「接住快照、写进任务」这一件事：阶段状态怎么算、跳过的阶段怎么补、
// 要不要给百分比，全部由 service 侧的阶段账负责。服务端自己再数一遍 PhaseStarted/
// PhaseDone 事件只会造出第二套语义——历史上「终端显示跑完了、报告里还差一步」就是这么来的。
type scanStageSink struct {
	srv *Server
	job *ScanJob
}

// StagesUpdated 实现 model.StageSink。传入的切片是每次新建的快照，直接接管即可。
func (k scanStageSink) StagesUpdated(stages []model.StageRecord) {
	k.srv.mu.Lock()
	defer k.srv.mu.Unlock()
	k.job.Stages = stages
}

// PhaseStarted / PhaseSub / PhaseDone 是 model.ScanProgress 的必填方法，这里刻意留空：
// 阶段账已经通过 StagesUpdated 整份送过来了，再照事件流数一遍只会造出第二套阶段语义
// （CLI 的 Spinner 才需要逐事件渲染）。留空不等于假装工作——真正的数据面只有一个入口。
func (scanStageSink) PhaseStarted(string, string)                   {}
func (scanStageSink) PhaseSub(string, int, int, string)             {}
func (scanStageSink) PhaseDone(string, string, bool, time.Duration) {}

var _ model.ScanProgress = scanStageSink{}
var _ model.StageSink = scanStageSink{}

// setScanStages 在扫描结束后写入最终阶段账（含 write/archive 这些 Run 收尾才补上的阶段）。
func (s *Server) setScanStages(job *ScanJob, stages []model.StageRecord) {
	if len(stages) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job.Stages = stages
}

// scanJobSnapshot 在锁内取任务的副本：runJob 与进度回调都在改同一个任务，
// 把活的指针交给 json.Marshal 就是拿响应体去赌它这一毫秒没人写。
func (s *Server) scanJobSnapshot(id string) (ScanJob, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		return ScanJob{}, false
	}
	return *job, true
}
