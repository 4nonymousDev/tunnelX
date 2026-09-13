package tunnel

import (
	"math/rand"
	"time"
)

// 退避参数。
// 不设次数上限：使用场景（笔记本合盖外出、服务器维护重启）里"最终能自动恢复"
// 远比"及时放弃"重要。最坏情况等待 BackoffMax，无需任何手动干预。
// Backoff parameters.
// Retries are unlimited because eventual automatic recovery after a laptop sleeps or
// a server is maintained matters more than giving up quickly. The worst-case wait is
// BackoffMax and requires no manual intervention.
const (
	BackoffInitial = 5 * time.Second
	BackoffMax     = 5 * time.Minute
	backoffFactor  = 2
	// jitterFrac 是随机抖动比例。多条隧道通常在同一时刻因同一原因失败，
	// 无抖动会导致它们此后始终同步重试，对服务端形成周期性尖峰。
	// jitterFrac is random jitter. Without it, tunnels failing together would keep
	// retrying in lockstep and create periodic load spikes on the server.
	jitterFrac = 0.2
)

// Backoff 是指数退避计时器。非并发安全——每条隧道各自持有一个。
// Backoff is an exponential-backoff timer. It is not concurrency-safe; each tunnel owns one.
type Backoff struct {
	next time.Duration
	rnd  *rand.Rand
}

func NewBackoff() *Backoff {
	return &Backoff{
		next: BackoffInitial,
		rnd:  rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Next 返回下次重试前应等待的时长，并推进退避曲线。
// 曲线为 5s → 10s → 20s → 40s → 80s → … → 上限 5min（各带 ±20% 抖动）。
// Next returns the delay before the next retry and advances the curve:
// 5s, 10s, 20s, 40s, 80s, ... up to 5min, each with +/-20% jitter.
func (b *Backoff) Next() time.Duration {
	d := b.next

	b.next *= backoffFactor
	if b.next > BackoffMax {
		b.next = BackoffMax
	}

	// 抖动后仍可能略微超过上限，可以接受；重要的是不低于初始值。
	// Jitter may slightly exceed the cap; the important constraint is the lower bound.
	delta := float64(d) * jitterFrac
	jittered := time.Duration(float64(d) + (b.rnd.Float64()*2-1)*delta)
	if jittered < BackoffInitial/2 {
		jittered = BackoffInitial / 2
	}
	return jittered
}

// Reset 在连接成功后调用，使下次失败重新从初始间隔开始。
// Reset returns the delay to its initial value after a successful connection.
func (b *Backoff) Reset() {
	b.next = BackoffInitial
}
