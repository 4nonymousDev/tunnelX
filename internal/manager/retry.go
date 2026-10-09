package manager

import (
	"errors"
	"math/rand"
	"time"

	"tunnelx/internal/proto"
	"tunnelx/internal/tunnel"
)

// Only a duplicate reported during device registration gets a recovery window.
// The protocol's generic duplicate-ID classification remains non-retryable.
type helloFailure struct{ error }

func (e *helloFailure) Unwrap() error { return e.error }

type duplicateIDPolicy struct {
	gracePeriod        time.Duration
	minDelay, maxDelay time.Duration
}

func defaultDuplicateIDPolicy() duplicateIDPolicy {
	return duplicateIDPolicy{2 * time.Minute, 5 * time.Second, 10 * time.Second}
}

type duplicateIDRetry struct {
	policy   duplicateIDPolicy
	deadline time.Time
}

func (r *duplicateIDRetry) reset() { r.deadline = time.Time{} }

func (r *duplicateIDRetry) next(err error, now time.Time) (*tunnel.Fault, time.Duration, bool) {
	var hello *helloFailure
	var response *proto.Error
	if !errors.As(err, &hello) || !errors.As(hello, &response) || response.Code != proto.CodeDuplicateID {
		return nil, 0, false
	}
	if r.deadline.IsZero() {
		r.deadline = now.Add(r.policy.gracePeriod)
	}
	remaining := r.deadline.Sub(now)
	if remaining <= 0 {
		return &tunnel.Fault{Err: err, Reason: "本机标识持续被另一会话占用，已停止自动重试。请检查其他 TunnelX 实例，处理后重新连接"}, 0, true
	}
	delay := r.policy.minDelay
	if span := r.policy.maxDelay - delay; span > 0 {
		delay += time.Duration(rand.Int63n(int64(span)))
	}
	if delay > remaining {
		delay = remaining
	}
	return &tunnel.Fault{Err: err, Retryable: true, Reason: "本机标识被另一会话占用，正在限时重试（可能是旧会话尚未清理或其他实例正在运行）"}, delay, true
}
