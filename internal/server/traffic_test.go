package server

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"tunnelx/internal/proto"
	"tunnelx/internal/session"
)

func TestTrafficAggregationAndFinalSample(t *testing.T) {
	now := time.Now()
	m := newTrafficMeter(now)
	e := session.Session{ClientID: "e", Name: "Exporter"}
	a, doneA := m.begin(e, session.Session{ClientID: "i1", Name: "same name"})
	b, doneB := m.begin(e, session.Session{ClientID: "i2", Name: "same name"})
	a.bytes.Add(100)
	b.bytes.Add(300)
	m.sample(now.Add(2 * time.Second))
	v := m.snapshot()
	if len(v.Links) != 2 || v.Links[0].BytesPerSecond != 50 || v.Links[1].BytesPerSecond != 150 || v.Exporters[0].Bytes != 400 || len(v.Importers) != 2 {
		t.Fatalf("incorrect fan-out aggregation: %+v", v)
	}
	// Closing and reopening a pair before sampling must retain its last bytes.
	a.bytes.Add(40)
	doneA()
	a2, doneA2 := m.begin(e, session.Session{ClientID: "i1", Name: "renamed"})
	a2.bytes.Add(20)
	doneA2()
	doneB()
	m.sample(now.Add(3 * time.Second))
	v = m.snapshot()
	if len(v.Links) != 1 || v.Links[0].BytesPerSecond != 60 || v.Links[0].Connections != 0 || v.Exporters[0].Bytes != 460 || len(m.pairs) != 0 {
		t.Fatalf("final bytes lost or counted twice: %+v", v)
	}
	m.sample(now.Add(4 * time.Second))
	v = m.snapshot()
	if len(v.Links) != 0 || v.Exporters[0].Bytes != 460 {
		t.Fatalf("closed links did not expire: %+v", v)
	}
	// A snapshot must not hand callers mutable internal slices.
	v.Exporters[0].Bytes = 0
	if m.snapshot().Exporters[0].Bytes != 460 {
		t.Fatal("snapshot aliases totals")
	}
}

type partialTrafficWriter struct{}

func (partialTrafficWriter) Write(p []byte) (int, error) { return 3, io.ErrClosedPipe }

func TestTrafficWriterCountsPartialWrites(t *testing.T) {
	var counter atomic.Int64
	// strings.Reader exercises io.Copy's WriterTo path.
	n, err := io.Copy(trafficWriter{partialTrafficWriter{}, &counter}, strings.NewReader("abcdef"))
	if n != 3 || counter.Load() != 3 || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("n=%d counter=%d err=%v", n, counter.Load(), err)
	}
}

func TestTrafficConcurrentTransfers(t *testing.T) {
	now := time.Now()
	m := newTrafficMeter(now)
	var writers sync.WaitGroup
	for range 30 {
		writers.Add(1)
		go func() {
			defer writers.Done()
			p, done := m.begin(session.Session{ClientID: "same"}, session.Session{ClientID: "same"})
			defer done()
			for range 100 {
				p.bytes.Add(7)
			}
		}()
	}
	for range 10 {
		m.sample(time.Now())
		_ = m.snapshot()
	}
	writers.Wait()
	m.sample(time.Now().Add(time.Second))
	v := m.snapshot()
	if len(v.Exporters) != 1 || len(v.Importers) != 1 || v.Exporters[0].Bytes != 21000 || v.Importers[0].Bytes != 21000 {
		t.Fatalf("concurrent/self transfer totals: %+v", v)
	}
}

func TestTrafficPersistsAndAccumulatesOnServerRestart(t *testing.T) {
	cfg := managementConfig(t)
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	p, done := s.traffic.begin(session.Session{ClientID: "exporter"}, session.Session{ClientID: "importer"})
	p.bytes.Add(1234)
	done()
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if v := s.traffic.snapshot(); len(v.Exporters) != 1 || v.Exporters[0].Bytes != 1234 {
		t.Fatalf("shutdown missed final sample: %+v", v)
	}
	next, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	v, err := (*adminBackend)(next).Traffic(context.Background())
	if err != nil || len(v.Exporters) != 1 || len(v.Importers) != 1 || len(v.Links) != 0 || !v.Since.Equal(s.traffic.since) || !v.Persistent || !v.PersistenceHealthy || v.PersistedAt == nil {
		t.Fatalf("new run did not restore history: %+v %v", v, err)
	}
	if v.Exporters[0].Bytes != 1234 || v.Importers[0].Bytes != 1234 {
		t.Fatalf("restored bytes: %+v", v)
	}
	p, done = next.traffic.begin(session.Session{ClientID: "exporter"}, session.Session{ClientID: "importer"})
	p.bytes.Add(66)
	done()
	if err = next.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	v = third.traffic.snapshot()
	if len(v.Exporters) != 1 || len(v.Importers) != 1 || v.Exporters[0].Bytes != 1300 || v.Importers[0].Bytes != 1300 {
		t.Fatalf("history did not accumulate: %+v", v)
	}
}

func TestTrafficCountsOpenSSHTransfer(t *testing.T) {
	s, addr, key := startServerInstance(t)
	exporter := dialWith(t, addr, key)
	pc, ctrl := openTestControl(t, exporter, proto.RoleExporter, "cross-exporter")
	defer ctrl.Close()
	forwarded := exporter.HandleChannelOpen(proto.ForwardChannelType)
	target := publishManagementTunnels(t, pc, []proto.TunnelSpec{{TunnelID: "traffic", SrcPort: 80}}).Targets[0]
	ownerChannel := make(chan ssh.Channel, 1)
	go func() {
		incoming := <-forwarded
		ch, requests, err := incoming.Accept()
		if err != nil {
			ownerChannel <- nil
			return
		}
		go ssh.DiscardRequests(requests)
		ownerChannel <- ch
	}()
	importer := dialWith(t, addr, key)
	_, ctrl2 := openTestControl(t, importer, proto.RoleImporter, "cross-importer")
	defer ctrl2.Close()
	timer := time.AfterFunc(5*time.Second, func() { importer.Close(); exporter.Close() })
	defer timer.Stop()
	payload, _ := proto.EncodeOpen(target)
	in, requests, err := importer.OpenChannel(proto.OpenChannelType, payload)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	go ssh.DiscardRequests(requests)
	out := <-ownerChannel
	if out == nil {
		t.Fatal("exporter did not accept channel")
	}
	defer out.Close()
	if _, err = in.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadFull(out, make([]byte, 5)); err != nil {
		t.Fatal(err)
	}
	if _, err = out.Write([]byte("response")); err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadFull(in, make([]byte, 8)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		v := s.traffic.snapshot()
		if len(v.Exporters) == 1 && v.Exporters[0].Bytes == 13 {
			if v.Importers[0].Bytes != 13 || len(v.Links) != 1 || v.Links[0].Connections != 1 {
				t.Fatalf("wrong live snapshot: %+v", v)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("open transfer was not sampled: %+v", v)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
