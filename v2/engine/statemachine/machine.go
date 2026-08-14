package statemachine

import "fmt"

// Action is a side effect run when a Machine crosses an Edge, before the
// state changes take visible effect to callers.
type Action func() error

// Edge is one legal (state, trigger) -> state transition.
type Edge[S comparable, T comparable] struct {
	From, To S
	Trigger  T
	Action   Action // nil = no side effect
}

type edgeKey[S comparable, T comparable] struct {
	state   S
	trigger T
}

// Machine is a table-driven state machine over states of type S and
// triggers of type T. It holds exactly one current state and dispatches
// Handle calls through a fixed edge table built at construction time.
type Machine[S comparable, T comparable] struct {
	state        S
	table        map[edgeKey[S, T]]Edge[S, T]
	onTransition func(Edge[S, T])
	onInvalid    func(state S, trigger T) error
}

// Option configures optional Machine behavior.
type Option[S comparable, T comparable] func(*Machine[S, T])

// WithOnTransition registers a hook invoked after every successful
// transition, once the state change and Action have both taken effect.
func WithOnTransition[S comparable, T comparable](fn func(Edge[S, T])) Option[S, T] {
	return func(m *Machine[S, T]) { m.onTransition = fn }
}

// WithOnInvalid overrides the error Handle returns when called with a
// trigger that has no edge from the current state. The default returns an
// *InvalidTransitionError.
func WithOnInvalid[S comparable, T comparable](fn func(state S, trigger T) error) Option[S, T] {
	return func(m *Machine[S, T]) { m.onInvalid = fn }
}

// New builds a Machine starting in initial, accepting exactly the given
// edges. New panics if two edges share the same (From, Trigger) pair: the
// table must be unambiguous by construction.
func New[S comparable, T comparable](initial S, edges []Edge[S, T], opts ...Option[S, T]) *Machine[S, T] {
	m := &Machine[S, T]{
		state: initial,
		table: make(map[edgeKey[S, T]]Edge[S, T], len(edges)),
	}
	for _, e := range edges {
		key := edgeKey[S, T]{state: e.From, trigger: e.Trigger}
		if _, dup := m.table[key]; dup {
			panic("statemachine: duplicate edge for the same (state, trigger) pair")
		}
		m.table[key] = e
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// State returns the machine's current state.
func (m *Machine[S, T]) State() S {
	return m.state
}

// CanHandle reports whether trigger has a declared edge from the current
// state, without applying it.
func (m *Machine[S, T]) CanHandle(trigger T) bool {
	_, ok := m.table[edgeKey[S, T]{state: m.state, trigger: trigger}]
	return ok
}

// Handle applies trigger from the current state. If a matching edge
// exists: its Action runs (if any), the state changes to the edge's To,
// the onTransition hook (if any) is notified, and Action's error (if any)
// is returned. If no matching edge exists, the state is left unchanged and
// the onInvalid hook's error is returned.
func (m *Machine[S, T]) Handle(trigger T) error {
	edge, ok := m.table[edgeKey[S, T]{state: m.state, trigger: trigger}]
	if !ok {
		if m.onInvalid != nil {
			return m.onInvalid(m.state, trigger)
		}
		return &InvalidTransitionError[S, T]{State: m.state, Trigger: trigger}
	}
	var err error
	if edge.Action != nil {
		err = edge.Action()
	}
	m.state = edge.To
	if m.onTransition != nil {
		m.onTransition(edge)
	}
	return err
}

// InvalidTransitionError reports that Trigger has no declared edge from
// State.
type InvalidTransitionError[S comparable, T comparable] struct {
	State   S
	Trigger T
}

func (e *InvalidTransitionError[S, T]) Error() string {
	return fmt.Sprintf("statemachine: trigger %v is not valid from state %v", e.Trigger, e.State)
}
