package reactor

import (
	"sync"
	"testing"
	"time"
)

// fakeSource is a controllable EventSource for tests: Poll blocks until an
// event is pushed, Wake is called, or the deadline passes.
type fakeSource struct {
	mu     sync.Mutex
	events []Event
	wake   chan struct{}
}

func newFakeSource() *fakeSource {
	return &fakeSource{wake: make(chan struct{}, 1)}
}

func (f *fakeSource) push(ev Event) {
	f.mu.Lock()
	f.events = append(f.events, ev)
	f.mu.Unlock()
	_ = f.Wake()
}

func (f *fakeSource) Poll(dst []Event, deadline time.Time) ([]Event, error) {
	for {
		f.mu.Lock()
		if len(f.events) > 0 {
			dst = append(dst, f.events...)
			f.events = nil
			f.mu.Unlock()
			return dst, nil
		}
		f.mu.Unlock()

		if deadline.IsZero() {
			return dst, nil
		}
		wait := time.Until(deadline)
		if wait <= 0 {
			return dst, nil
		}
		select {
		case <-f.wake:
		case <-time.After(wait):
			return dst, nil
		}
	}
}

func (f *fakeSource) Wake() error {
	select {
	case f.wake <- struct{}{}:
	default:
	}
	return nil
}

func (f *fakeSource) Close() error { return nil }

func runInBackground(t *testing.T, r *Reactor) {
	t.Helper()
	errCh := make(chan error, 1)
	go func() { errCh <- r.Run() }()
	t.Cleanup(func() {
		r.RequestStop()
		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("Run returned error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Run did not return after RequestStop")
		}
	})
}

func TestScheduleOnce_FiresAfterDelay(t *testing.T) {
	r := New(newFakeSource())
	fired := make(chan struct{}, 1)
	r.ScheduleOnce(10*time.Millisecond, func() { fired <- struct{}{} })
	runInBackground(t, r)

	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("one-shot timer never fired")
	}
}

func TestScheduleEvery_FiresRepeatedly(t *testing.T) {
	r := New(newFakeSource())
	fires := make(chan struct{}, 8)
	r.ScheduleEvery(5*time.Millisecond, func() {
		select {
		case fires <- struct{}{}:
		default:
		}
	})
	runInBackground(t, r)

	for i := 0; i < 3; i++ {
		select {
		case <-fires:
		case <-time.After(time.Second):
			t.Fatalf("expected at least 3 fires, got %d", i)
		}
	}
}

func TestCancelTimer_PreventsFire(t *testing.T) {
	r := New(newFakeSource())
	fired := make(chan struct{}, 1)
	id := r.ScheduleOnce(20*time.Millisecond, func() { fired <- struct{}{} })
	r.CancelTimer(id)
	runInBackground(t, r)

	select {
	case <-fired:
		t.Fatal("canceled timer fired")
	case <-time.After(60 * time.Millisecond):
	}
}

func TestCancelTimer_SelfCancelWithinCallback(t *testing.T) {
	r := New(newFakeSource())
	var calls int
	callCh := make(chan struct{}, 8)
	var id TimerID
	id = r.ScheduleEvery(5*time.Millisecond, func() {
		calls++
		callCh <- struct{}{}
		r.CancelTimer(id)
	})
	runInBackground(t, r)

	select {
	case <-callCh:
	case <-time.After(time.Second):
		t.Fatal("timer never fired once")
	}
	// Give a would-be second fire a chance to happen before asserting it didn't.
	select {
	case <-callCh:
		t.Fatal("self-canceled timer fired again")
	case <-time.After(40 * time.Millisecond):
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 call, got %d", calls)
	}
}

func TestPostFunc_RunsOnReactorGoroutine(t *testing.T) {
	r := New(newFakeSource())
	runInBackground(t, r)

	done := make(chan struct{})
	r.PostFunc(func() { close(done) })

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("posted func never ran")
	}
}

func TestEventHandler_ReceivesPushedEvents(t *testing.T) {
	src := newFakeSource()
	r := New(src)
	got := make(chan Event, 1)
	r.SetEventHandler(func(ev Event) { got <- ev })
	runInBackground(t, r)

	src.push(Event{UserData: 42, Result: 7})

	select {
	case ev := <-got:
		if ev.UserData != 42 || ev.Result != 7 {
			t.Fatalf("unexpected event: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("event handler never invoked")
	}
}

func TestRequestStop_DrainsPendingTasksBeforeReturning(t *testing.T) {
	r := New(newFakeSource())
	ran := false
	r.PostFunc(func() { ran = true })
	r.RequestStop()

	if err := r.Run(); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !ran {
		t.Fatal("task posted before RequestStop was not drained")
	}
}

func TestWake_InterruptsBlockedPoll(t *testing.T) {
	src := newFakeSource()
	r := New(src)
	got := make(chan Event, 1)
	r.SetEventHandler(func(ev Event) { got <- ev })
	// No timers scheduled, so Poll would otherwise block for idleCap.
	runInBackground(t, r)

	start := time.Now()
	src.push(Event{UserData: 1})

	select {
	case <-got:
		if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
			t.Fatalf("Wake took too long to interrupt Poll: %v", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("event handler never invoked; Wake did not interrupt Poll")
	}
}

func TestReactor_RegisterFD_DirectDispatch(t *testing.T) {
	src := newFakeSource()
	r := New(src)

	fd10Events := make(chan Event, 1)
	fallbackEvents := make(chan Event, 1)

	r.RegisterFD(10, func(ev Event) { fd10Events <- ev })
	r.SetEventHandler(func(ev Event) { fallbackEvents <- ev })

	runInBackground(t, r)

	// UserData with FD=10 (10 << 32 | 1)
	ud10 := (uint64(10) << 32) | 1
	src.push(Event{UserData: ud10, Result: 100})

	select {
	case ev := <-fd10Events:
		if ev.Result != 100 {
			t.Fatalf("expected Result=100, got %d", ev.Result)
		}
	case <-time.After(time.Second):
		t.Fatal("FD=10 event handler never invoked")
	}

	// UserData with unregistered FD=20 should fall back to default handler
	ud20 := (uint64(20) << 32) | 2
	src.push(Event{UserData: ud20, Result: 200})

	select {
	case ev := <-fallbackEvents:
		if ev.Result != 200 {
			t.Fatalf("expected Result=200, got %d", ev.Result)
		}
	case <-time.After(time.Second):
		t.Fatal("fallback event handler never invoked")
	}

	// Unregister FD=10 and verify it falls back
	r.UnregisterFD(10)
	src.push(Event{UserData: ud10, Result: 300})

	select {
	case ev := <-fallbackEvents:
		if ev.Result != 300 {
			t.Fatalf("expected Result=300, got %d", ev.Result)
		}
	case <-time.After(time.Second):
		t.Fatal("fallback event handler after unregister never invoked")
	}
}
