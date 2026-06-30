// Copyright (c) the go-ruby-rake/rake authors
//
// SPDX-License-Identifier: BSD-3-Clause

package rake

import "strings"

// Scope is Rake's namespace stack — the port of Rake::Scope, which in MRI is a
// LinkedList subclass used as an immutable cons-list of namespace names. The
// head is the innermost namespace; the empty scope (Scope == nil) is the
// top-level / Null Object (Rake::Scope::EMPTY).
//
// Like MRI it is a persistent (immutable) singly-linked list: Cons returns a
// fresh head sharing the same tail, so a saved Scope is never mutated by a
// later namespace push/pop.
type Scope struct {
	head string
	tail *Scope
}

// Cons returns a new scope with name pushed onto s (MRI Scope#conj / cons).
// Calling on a nil (empty) scope starts a one-element scope.
func (s *Scope) Cons(name string) *Scope {
	return &Scope{head: name, tail: s}
}

// Empty reports whether s is the top-level (Null Object) scope.
func (s *Scope) Empty() bool { return s == nil }

// Head returns the innermost namespace name (empty string for the empty scope).
func (s *Scope) Head() string {
	if s == nil {
		return ""
	}
	return s.head
}

// Tail returns the enclosing scope (nil for the empty scope; MRI returns
// EMPTY, whose tail is itself).
func (s *Scope) Tail() *Scope {
	if s == nil {
		return nil
	}
	return s.tail
}

// names returns the scope names from outermost to innermost (MRI maps the list
// — innermost-first — then reverses).
func (s *Scope) names() []string {
	var inner []string
	for cur := s; !cur.Empty(); cur = cur.tail {
		inner = append(inner, cur.head)
	}
	// inner is innermost-first; reverse to outermost-first.
	out := make([]string, len(inner))
	for i, n := range inner {
		out[len(inner)-1-i] = n
	}
	return out
}

// Path is Rake::Scope#path — the namespace names joined outer:inner by ":".
// The empty scope's path is "".
func (s *Scope) Path() string {
	return strings.Join(s.names(), ":")
}

// PathWithTaskName is Rake::Scope#path_with_task_name — the scope path prefixed
// onto taskName. The empty scope returns taskName unchanged (EmptyScope).
func (s *Scope) PathWithTaskName(taskName string) string {
	if s.Empty() {
		return taskName
	}
	return s.Path() + ":" + taskName
}

// Trim drops the n innermost scope levels (Rake::Scope#trim). It never trims
// past the top-level scope.
func (s *Scope) Trim(n int) *Scope {
	result := s
	for n > 0 && !result.Empty() {
		result = result.tail
		n--
	}
	return result
}

// ScopeMake builds a scope from names given outermost-first (Rake::Scope.make
// is called innermost-first; the manager builds it from a reversed split, so
// this helper takes the natural outer→inner order the caller already has).
func ScopeMake(names ...string) *Scope {
	var s *Scope
	for _, n := range names {
		s = s.Cons(n)
	}
	return s
}
