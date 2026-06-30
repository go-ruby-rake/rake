// Copyright (c) the go-ruby-rake/rake authors
//
// SPDX-License-Identifier: BSD-3-Clause

package rake

import "fmt"

// InvocationChain tracks the chain of task invocations to detect circular
// dependencies — the port of Rake::InvocationChain (itself a LinkedList
// subclass). It is an immutable cons-list of task names; the empty chain
// (nil) renders as "TOP" (Rake::InvocationChain::EMPTY).
type InvocationChain struct {
	head string
	tail *InvocationChain
}

// EmptyChain is the Null Object for an empty chain (Rake::InvocationChain::EMPTY).
var EmptyChain *InvocationChain

// Member reports whether invocation is already somewhere in the chain
// (Rake::InvocationChain#member?). The empty chain contains nothing.
func (c *InvocationChain) Member(invocation string) bool {
	for cur := c; cur != nil; cur = cur.tail {
		if cur.head == invocation {
			return true
		}
	}
	return false
}

// String renders the chain TOP => a => b (Rake::InvocationChain#to_s). The
// empty chain is "TOP".
func (c *InvocationChain) String() string {
	if c == nil {
		return "TOP"
	}
	return c.tail.String() + " => " + c.head
}

// Append returns a new chain with invocation appended, or an error if
// invocation already appears — Rake's "Circular dependency detected" guard
// (Rake::InvocationChain#append, which fails with RuntimeError). The returned
// error is a *CircularDependencyError carrying the exact MRI message.
func (c *InvocationChain) Append(invocation string) (*InvocationChain, error) {
	if c.Member(invocation) {
		return nil, &CircularDependencyError{
			Message: fmt.Sprintf("Circular dependency detected: %s => %s", c.String(), invocation),
		}
	}
	return &InvocationChain{head: invocation, tail: c}, nil
}

// CircularDependencyError is the RuntimeError Rake raises when a task's
// prerequisite graph contains a cycle. Message is byte-for-byte MRI's
// "Circular dependency detected: TOP => x => y => x".
type CircularDependencyError struct {
	Message string
}

func (e *CircularDependencyError) Error() string { return e.Message }
