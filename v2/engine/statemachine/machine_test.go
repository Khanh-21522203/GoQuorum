package statemachine

import (
	"errors"
	"testing"
)

type doorState int

const (
	closed doorState = iota
	open
	locked
)

type doorTrigger int

const (
	triggerOpen doorTrigger = iota
	triggerClose
	triggerLock
	triggerUnlock
)

func newDoor(t *testing.T, onTransition func(Edge[doorState, doorTrigger])) *Machine[doorState, doorTrigger] {
	t.Helper()
	var opts []Option[doorState, doorTrigger]
	if onTransition != nil {
		opts = append(opts, WithOnTransition(onTransition))
	}
	return New(closed, []Edge[doorState, doorTrigger]{
		{From: closed, Trigger: triggerOpen, To: open},
		{From: open, Trigger: triggerClose, To: closed},
		{From: closed, Trigger: triggerLock, To: locked},
		{From: locked, Trigger: triggerUnlock, To: closed},
	}, opts...)
}

func TestMachine_LegalTransitionChangesState(t *testing.T) {
	m := newDoor(t, nil)
	if err := m.Handle(triggerOpen); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.State() != open {
		t.Fatalf("expected state %v, got %v", open, m.State())
	}
}

func TestMachine_IllegalTransitionLeavesStateUnchanged(t *testing.T) {
	m := newDoor(t, nil)
	// closed has no edge for triggerUnlock.
	err := m.Handle(triggerUnlock)
	if err == nil {
		t.Fatal("expected an error for an illegal transition")
	}
	var invalid *InvalidTransitionError[doorState, doorTrigger]
	if !errors.As(err, &invalid) {
		t.Fatalf("expected *InvalidTransitionError, got %T: %v", err, err)
	}
	if invalid.State != closed || invalid.Trigger != triggerUnlock {
		t.Fatalf("unexpected error fields: %+v", invalid)
	}
	if m.State() != closed {
		t.Fatalf("state must not change on an illegal transition, got %v", m.State())
	}
}

func TestMachine_ActionRunsOnTransition(t *testing.T) {
	var ran bool
	m := New(closed, []Edge[doorState, doorTrigger]{
		{From: closed, Trigger: triggerOpen, To: open, Action: func() error {
			ran = true
			return nil
		}},
	})
	if err := m.Handle(triggerOpen); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ran {
		t.Fatal("Action was not run")
	}
}

func TestMachine_ActionErrorStillTransitions(t *testing.T) {
	boom := errors.New("boom")
	m := New(closed, []Edge[doorState, doorTrigger]{
		{From: closed, Trigger: triggerOpen, To: open, Action: func() error { return boom }},
	})
	err := m.Handle(triggerOpen)
	if !errors.Is(err, boom) {
		t.Fatalf("expected boom, got %v", err)
	}
	if m.State() != open {
		t.Fatalf("state should still advance even if Action errors, got %v", m.State())
	}
}

func TestMachine_OnTransitionHookFiresWithEdge(t *testing.T) {
	var got Edge[doorState, doorTrigger]
	m := newDoor(t, func(e Edge[doorState, doorTrigger]) { got = e })
	_ = m.Handle(triggerOpen)
	if got.From != closed || got.To != open || got.Trigger != triggerOpen {
		t.Fatalf("unexpected edge in hook: %+v", got)
	}
}

func TestMachine_WithOnInvalidOverridesDefaultError(t *testing.T) {
	sentinel := errors.New("custom rejection")
	m := New(closed, []Edge[doorState, doorTrigger]{
		{From: closed, Trigger: triggerOpen, To: open},
	}, WithOnInvalid(func(doorState, doorTrigger) error { return sentinel }))

	if err := m.Handle(triggerLock); !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
}

func TestMachine_CanHandle(t *testing.T) {
	m := newDoor(t, nil)
	if !m.CanHandle(triggerOpen) {
		t.Fatal("expected triggerOpen to be handleable from closed")
	}
	if m.CanHandle(triggerUnlock) {
		t.Fatal("expected triggerUnlock to be rejected from closed")
	}
}

func TestNew_PanicsOnDuplicateEdge(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected New to panic on a duplicate (state, trigger) edge")
		}
	}()
	New(closed, []Edge[doorState, doorTrigger]{
		{From: closed, Trigger: triggerOpen, To: open},
		{From: closed, Trigger: triggerOpen, To: locked},
	})
}

func TestMachine_FullLifecycleSequence(t *testing.T) {
	m := newDoor(t, nil)
	sequence := []doorTrigger{triggerOpen, triggerClose, triggerLock, triggerUnlock}
	want := []doorState{open, closed, locked, closed}
	for i, trig := range sequence {
		if err := m.Handle(trig); err != nil {
			t.Fatalf("step %d: unexpected error: %v", i, err)
		}
		if m.State() != want[i] {
			t.Fatalf("step %d: expected state %v, got %v", i, want[i], m.State())
		}
	}
}
