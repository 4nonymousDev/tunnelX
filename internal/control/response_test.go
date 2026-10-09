package control

import (
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"tunnelx/internal/proto"
)

func TestBufferedReplySurvivesClosureOrTimeout(t *testing.T) {
	for _, closed := range []bool{true, false} {
		for _, rejection := range []bool{true, false} {
			name := "timeout"
			if closed {
				name = "closed"
			}
			if rejection {
				name += "/duplicate"
			} else {
				name += "/success"
			}
			t.Run(name, func(t *testing.T) {
				// Both events are ready before waiting, without relying on network
				// scheduling. Repetition exercises either branch of Go's select.
				for range 100 {
					c := &Client{respCh: make(chan respond, 1), done: make(chan struct{}), cause: io.EOF}
					expired := make(chan time.Time, 1)
					if closed {
						close(c.done)
					} else {
						expired <- time.Now()
					}
					var msg any = proto.HelloOK{V: proto.Version, Type: proto.TypeHelloOK, SessionID: "registered-session", Fingerprint: "registered-key"}
					envelope := proto.Envelope{V: proto.Version, Type: proto.TypeHelloOK}
					if rejection {
						msg = proto.Error{V: proto.Version, Type: proto.TypeError, Code: proto.CodeDuplicateID, Msg: "identity occupied"}
						envelope.Type = proto.TypeError
					}
					raw, err := json.Marshal(msg)
					if err != nil {
						t.Fatal(err)
					}
					c.respCh <- respond{env: envelope, raw: raw}
					var reply proto.HelloOK
					err = c.awaitResponse(expired, proto.TypeHelloOK, &reply)
					if rejection {
						var response *proto.Error
						if !errors.As(err, &response) || response.Code != proto.CodeDuplicateID {
							t.Fatalf("buffered duplicate became %T (%v)", err, err)
						}
					} else if err != nil || reply.SessionID != "registered-session" || reply.Fingerprint != "registered-key" {
						t.Fatalf("buffered hello reply lost: %+v, %v", reply, err)
					}
				}
			})
		}
	}
}

func TestClosedWithoutBufferedReplyReturnsCause(t *testing.T) {
	for _, cause := range []error{io.EOF, nil} {
		c := &Client{respCh: make(chan respond, 1), done: make(chan struct{}), cause: cause}
		close(c.done)
		err := c.awaitResponse(make(chan time.Time), proto.TypeHelloOK, nil)
		if cause != nil {
			if !errors.Is(err, cause) {
				t.Fatalf("closure cause = %v, want %v", err, cause)
			}
		} else if err == nil || err.Error() != "控制通道已关闭" {
			t.Fatalf("closure without cause = %v", err)
		}
	}
}
