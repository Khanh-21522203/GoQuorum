// Package statemachine implements a generic, table-driven finite state
// machine: every (state, trigger) pair is either a declared edge or is
// rejected outright, so a subsystem's legal lifecycle is exhaustive and
// explicit rather than scattered across booleans and ad-hoc checks.
package statemachine
