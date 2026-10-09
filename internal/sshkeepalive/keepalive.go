// Package sshkeepalive monitors SSH transports with bounded, serialized probes.
package sshkeepalive

import (
	"errors"
	"fmt"
	"time"
)

const (
	Interval    = 15 * time.Second
	Timeout     = 60 * time.Second
	RequestType = "keepalive@openssh.com"
)

var ErrTimeout = errors.New("SSH keepalive response timeout")

// Sender is the portion of an SSH transport needed by the monitor. Closing it
// must unblock SendRequest, including a request blocked while writing.
type Sender interface {
	SendRequest(string, bool, []byte) (bool, []byte, error)
	Close() error
}

// Run monitors responses until done closes or the transport fails. A negative
// SSH reply still proves liveness. The timeout starts when monitoring begins and
// resets on every reply; only one request can be outstanding at a time.
//
// fail records the failure and must close the transport without waiting for Run.
// Run also closes the transport on exit and joins its request worker, so callers
// can cancel even a probe blocked while writing or waiting for a reply. Nonpositive
// interval and timeout use the package defaults.
func Run(sender Sender, done <-chan struct{}, interval, timeout time.Duration, fail func(error)) {
	if interval <= 0 {
		interval = Interval
	}
	if timeout <= 0 {
		timeout = Timeout
	}

	probes := make(chan struct{}, 1)
	results := make(chan error, 1)
	stop := make(chan struct{})
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		for {
			select {
			case <-stop:
				return
			case <-probes:
				select {
				case <-stop:
					return
				default:
				}
				_, _, err := sender.SendRequest(RequestType, true, nil)
				select {
				case results <- err:
				case <-stop:
					return
				}
			}
		}
	}()
	defer func() {
		close(stop)
		_ = sender.Close()
		<-workerDone
	}()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	pending := false
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if !pending {
				probes <- struct{}{}
				pending = true
			}
		case err := <-results:
			if err != nil {
				select {
				case <-done:
					return
				default:
				}
				fail(fmt.Errorf("SSH keepalive request failed: %w", err))
				return
			}
			pending = false
			deadline.Reset(timeout)
		case <-deadline.C:
			select {
			case <-done:
				return
			default:
			}
			fail(ErrTimeout)
			return
		}
	}
}
