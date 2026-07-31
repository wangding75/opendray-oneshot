package channeladapter

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type recordedInput struct {
	sessionID string
	data      []byte
	at        time.Time
}

type recordingInputter struct {
	mu     sync.Mutex
	calls  []recordedInput
	failAt int
	err    error
}

func (r *recordingInputter) Input(_ context.Context, sessionID string, data []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil && len(r.calls) == r.failAt {
		return r.err
	}
	r.calls = append(r.calls, recordedInput{sessionID: sessionID, data: append([]byte(nil), data...), at: time.Now()})
	return nil
}

func (r *recordingInputter) snapshot() []recordedInput {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedInput(nil), r.calls...)
}

func TestInputSubmitterRuneByRuneThenEnter(t *testing.T) {
	recorder := &recordingInputter{}
	submitter := newInputSubmitterForTest(recorder, 0, 0)
	if err := submitter.Submit(context.Background(), "ses-1", "hi"); err != nil {
		t.Fatal(err)
	}
	got := recorder.snapshot()
	if len(got) != 3 || string(got[0].data) != "h" || string(got[1].data) != "i" || string(got[2].data) != "\r" {
		t.Fatalf("writes = %#v; want h, i, carriage return", got)
	}
	for _, call := range got {
		if call.sessionID != "ses-1" {
			t.Fatalf("session id = %q; want ses-1", call.sessionID)
		}
	}
}

func TestInputSubmitterSplitsUTF8ByRune(t *testing.T) {
	recorder := &recordingInputter{}
	submitter := newInputSubmitterForTest(recorder, 0, 0)
	if err := submitter.Submit(context.Background(), "ses-1", "你好"); err != nil {
		t.Fatal(err)
	}
	got := recorder.snapshot()
	if len(got) != 3 || string(got[0].data) != "你" || string(got[1].data) != "好" || string(got[2].data) != "\r" {
		t.Fatalf("writes = %#v; want 你, 好, carriage return", got)
	}
}

func TestInputSubmitterPreservesInterKeyDelay(t *testing.T) {
	recorder := &recordingInputter{}
	submitter := newInputSubmitterForTest(recorder, 2*time.Millisecond, 8*time.Millisecond)
	if err := submitter.Submit(context.Background(), "ses-1", "ab"); err != nil {
		t.Fatal(err)
	}
	got := recorder.snapshot()
	if len(got) != 3 {
		t.Fatalf("writes = %d; want 3", len(got))
	}
	if got[1].at.Sub(got[0].at) < time.Millisecond {
		t.Fatalf("rune delay = %s; want >=1ms", got[1].at.Sub(got[0].at))
	}
	if got[2].at.Sub(got[1].at) < 7*time.Millisecond {
		t.Fatalf("submit delay = %s; want >=7ms", got[2].at.Sub(got[1].at))
	}
}

func TestInputSubmitterEmptyBodySendsOnlyEnter(t *testing.T) {
	recorder := &recordingInputter{}
	submitter := newInputSubmitterForTest(recorder, 0, 0)
	if err := submitter.Submit(context.Background(), "ses-1", ""); err != nil {
		t.Fatal(err)
	}
	got := recorder.snapshot()
	if len(got) != 1 || string(got[0].data) != "\r" {
		t.Fatalf("writes = %#v; want only carriage return", got)
	}
}

func TestInputSubmitterCancelledDoesNotSubmitEnter(t *testing.T) {
	recorder := &recordingInputter{}
	submitter := newInputSubmitterForTest(recorder, time.Hour, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := submitter.Submit(ctx, "ses-1", "hello")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v; want context.Canceled", err)
	}
	for _, call := range recorder.snapshot() {
		if string(call.data) == "\r" {
			t.Fatal("carriage return emitted after cancellation")
		}
	}
}

func TestInputSubmitterStopsAfterMidStreamFailure(t *testing.T) {
	boom := errors.New("pty closed")
	recorder := &recordingInputter{failAt: 1, err: boom}
	submitter := newInputSubmitterForTest(recorder, 0, 0)
	err := submitter.Submit(context.Background(), "ses-1", "abc")
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v; want %v", err, boom)
	}
	got := recorder.snapshot()
	if len(got) != 1 || string(got[0].data) != "a" {
		t.Fatalf("writes = %#v; want only a", got)
	}
}

func TestInputSubmitterRejectsMissingInputter(t *testing.T) {
	if err := NewInputSubmitter(nil).Submit(context.Background(), "ses-1", "hi"); err == nil {
		t.Fatal("expected missing inputter error")
	}
}
