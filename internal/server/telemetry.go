package server

import (
	"context"
	"time"
)

// Anonymous events never wait for disk. Both the store and ordinary logs have
// fixed cardinality and rate; no peer-controlled text is used as a metric key.
func (s *Server) recordRejected(fp, remote, result string) {
	if s.store != nil {
		s.store.RecordRejection(result)
	}
	if s.allowLog() {
		s.logf("拒绝请求汇总: %s（已限制日志速率）", result)
	}
}
func (s *Server) allowLog() bool {
	now := time.Now().UnixNano()
	old := s.logAfter.Load()
	return now >= old && s.logAfter.CompareAndSwap(old, now+int64(5*time.Second))
}
func (s *Server) limitedLog(format string, args ...any) {
	if s.allowLog() {
		s.logf(format, args...)
	}
}
func (s *Server) dbContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 3*time.Second)
}
func (s *Server) queueAudit(job func(context.Context)) {
	select {
	case <-s.auditStop:
		s.limitedLog("服务收尾期间审计未完成")
		return
	default:
	}
	select {
	case s.auditJobs <- job:
	default:
		s.limitedLog("审计收尾队列已满，起始记录保留为待核对")
	}
}
func (s *Server) runAuditJobs() {
	defer close(s.auditDone)
	for {
		select {
		case job := <-s.auditJobs:
			ctx, cancel := s.dbContext()
			job(ctx)
			cancel()
		case <-s.auditStop:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			for {
				select {
				case <-ctx.Done():
					s.limitedLog("审计收尾超时，未完成记录需要核对")
					return
				case job := <-s.auditJobs:
					job(ctx)
				default:
					return
				}
			}
		}
	}
}
