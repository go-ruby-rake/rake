// Copyright (c) the go-ruby-rake/rake authors
//
// SPDX-License-Identifier: BSD-3-Clause

// Package rake is a pure-Go (no cgo), MRI-faithful port of the task-graph core
// of Ruby's Rake (the rake-13.x gem): Rake::Task / Rake::FileTask, the
// Rake::TaskManager registry and namespace/scope resolution, Rake::FileList
// filtering, and Rake::InvocationChain circular-dependency detection.
//
// What it is — and isn't. The *pure-compute* half of Rake — the task DAG, the
// depth-first prerequisite-first invoke order, the invoke-once guard, circular
// detection, FileTask out-of-date timestamp logic, namespace/scope lookup, rule
// synthesis, and FileList pattern/exclude filtering — is fully deterministic and
// needs no interpreter, so it lives here as plain Go. The *effectful* half — the
// action bodies (the `do ... end` blocks), `sh`/FileUtils, real Dir.glob, and
// file mtimes — are SEAMS the host injects: an Action is a Go func the host (rbgo)
// wires to a Ruby block; FileList.Glob and FileTask timestamps are function
// fields the host wires to the real filesystem. Tests inject deterministic seams,
// so the whole suite is Ruby-free and reproducible across arches.
package rake

import "time"

// Action is a task's action body — the seam for a Ruby `do ... end` block.
// rbgo wires each Action to invoke the corresponding Ruby block (passing the
// task and its arguments); tests pass plain Go closures. An Action may return
// an error to abort the invocation (mirroring an exception escaping the block).
type Action func(t TaskItem, args Args) error

// Args carries a task's invocation arguments by name — the pure-compute slice
// of Rake::TaskArguments (the bookkeeping; value coercion stays in the host).
type Args struct {
	names  []string
	values []string
}

// NewArgs pairs argument names with the values passed to invoke
// (Rake::TaskArguments.new). Extra values past the named slots are ignored for
// lookup but preserved; missing values yield "".
func NewArgs(names, values []string) Args {
	return Args{names: append([]string(nil), names...), values: append([]string(nil), values...)}
}

// Lookup returns the value bound to argument name, or "" if unbound
// (Rake::TaskArguments#[]).
func (a Args) Lookup(name string) string {
	for i, n := range a.names {
		if n == name {
			if i < len(a.values) {
				return a.values[i]
			}
			return ""
		}
	}
	return ""
}

// Names returns the declared argument names.
func (a Args) Names() []string { return append([]string(nil), a.names...) }

// Values returns the supplied argument values.
func (a Args) Values() []string { return append([]string(nil), a.values...) }

// TaskItem is the behaviour every task kind exposes — Rake::Task and its
// subclass Rake::FileTask. It is the interface a prerequisite is resolved to,
// so the manager and the invoke walk treat plain tasks and file tasks
// uniformly.
type TaskItem interface {
	// Name is the fully-qualified task name (namespace-prefixed).
	Name() string
	// Needed reports whether the task's actions must run (Task#needed?:
	// always true; FileTask#needed?: out-of-date timestamp logic).
	Needed() bool
	// Timestamp is the task's time stamp (Task#timestamp: now; FileTask: the
	// file mtime, or the distant-future LATE sentinel when absent).
	Timestamp() time.Time
	// invokeWithCallChain runs the task, prerequisites first, under the cycle
	// detector — the protected Rake::Task#invoke_with_call_chain.
	invokeWithCallChain(args Args, chain *InvocationChain) error
	// task returns the embedded base Task (so FileTask reaches the shared state).
	task() *Task
}

// Task is the basic unit of work — the port of Rake::Task. It carries a name,
// prerequisites (ordinary and order-only), action bodies, the already-invoked
// guard, and the owning application for prerequisite lookup.
type Task struct {
	name                   string
	prerequisites          []string
	orderOnlyPrerequisites []string
	actions                []Action
	alreadyInvoked         bool
	invocationErr          error
	app                    *Application
	scope                  *Scope
	argNames               []string
	comments               []string
	sourcesSet             bool
	sources                []string
	// wrapper points at the enclosing TaskItem (a *FileTask) when this Task is
	// embedded, so the base methods can dispatch polymorphically to the
	// concrete override set (Needed/Timestamp/actions).
	wrapper TaskItem
}

// newTask constructs an empty Task named taskName owned by app, capturing the
// app's current scope (Rake::Task#initialize).
func newTask(taskName string, app *Application) *Task {
	return &Task{
		name:  taskName,
		app:   app,
		scope: app.currentScope,
	}
}

func (t *Task) task() *Task { return t }

// Name returns the task's fully-qualified name (Rake::Task#name).
func (t *Task) Name() string { return t.name }

// Prerequisites returns the ordinary prerequisite names (Rake::Task#prerequisites).
func (t *Task) Prerequisites() []string { return append([]string(nil), t.prerequisites...) }

// OrderOnlyPrerequisites returns the order-only prerequisite names
// (Rake::Task#order_only_prerequisites).
func (t *Task) OrderOnlyPrerequisites() []string {
	return append([]string(nil), t.orderOnlyPrerequisites...)
}

// Actions returns the task's action bodies (Rake::Task#actions).
func (t *Task) Actions() []Action { return append([]Action(nil), t.actions...) }

// Scope returns the namespace scope the task was defined in (Rake::Task#scope).
func (t *Task) Scope() *Scope { return t.scope }

// AlreadyInvoked reports whether invoke has already run (Rake::Task#already_invoked).
func (t *Task) AlreadyInvoked() bool { return t.alreadyInvoked }

// ArgNames returns the declared argument names (Rake::Task#arg_names).
func (t *Task) ArgNames() []string { return append([]string(nil), t.argNames...) }

// Sources returns the task's sources, defaulting to its prerequisites when no
// explicit sources were set (Rake::Task#sources).
func (t *Task) Sources() []string {
	if t.sourcesSet {
		return append([]string(nil), t.sources...)
	}
	return t.Prerequisites()
}

// Source is the first source, or "" when there are none (Rake::Task#source).
func (t *Task) Source() string {
	s := t.Sources()
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

// Enhance adds prerequisites (union, preserving order) and an optional action,
// returning t — Rake::Task#enhance.
func (t *Task) Enhance(deps []string, action Action) *Task {
	for _, d := range deps {
		if !contains(t.prerequisites, d) {
			t.prerequisites = append(t.prerequisites, d)
		}
	}
	if action != nil {
		t.actions = append(t.actions, action)
	}
	return t
}

// AddOrderOnly adds order-only prerequisites — the union minus any already an
// ordinary prerequisite (Rake::Task#| ). Returns t.
func (t *Task) AddOrderOnly(deps []string) *Task {
	for _, d := range deps {
		if contains(t.prerequisites, d) {
			continue
		}
		if !contains(t.orderOnlyPrerequisites, d) {
			t.orderOnlyPrerequisites = append(t.orderOnlyPrerequisites, d)
		}
	}
	return t
}

// setArgNames records the task's argument names (Rake::Task#set_arg_names).
func (t *Task) setArgNames(names []string) { t.argNames = append([]string(nil), names...) }

// Reenable clears the already-invoked guard so the task runs again on the next
// invoke (Rake::Task#reenable).
func (t *Task) Reenable() {
	t.alreadyInvoked = false
	t.invocationErr = nil
}

// Clear empties prerequisites, actions, comments, and arguments (Rake::Task#clear).
func (t *Task) Clear() *Task {
	t.prerequisites = nil
	t.orderOnlyPrerequisites = nil
	t.actions = nil
	t.comments = nil
	t.argNames = nil
	return t
}

// addComment appends a comment, de-duplicating (Rake::Task#add_comment).
func (t *Task) addComment(comment string) {
	if comment == "" {
		return
	}
	if !contains(t.comments, comment) {
		t.comments = append(t.comments, comment)
	}
}

// FullComment joins all comments with newlines, or "" when there are none
// (Rake::Task#full_comment).
func (t *Task) FullComment() string { return joinComments(t.comments, "\n", false) }

// Comment is the first sentence of each comment joined by " / "
// (Rake::Task#comment).
func (t *Task) Comment() string { return joinComments(t.comments, " / ", true) }

// PrerequisiteTasks resolves every prerequisite (ordinary then order-only) to
// its task via the application — Rake::Task#prerequisite_tasks. A prerequisite
// is looked up scoped first; if that resolves to t itself, the unscoped lookup
// is preferred (MRI's lookup_prerequisite, which lets a task depend on a
// same-named task in an outer scope).
func (t *Task) PrerequisiteTasks() ([]TaskItem, error) {
	all := append(append([]string(nil), t.prerequisites...), t.orderOnlyPrerequisites...)
	out := make([]TaskItem, 0, len(all))
	for _, pre := range all {
		ti, err := t.lookupPrerequisite(pre)
		if err != nil {
			return nil, err
		}
		out = append(out, ti)
	}
	return out, nil
}

func (t *Task) lookupPrerequisite(name string) (TaskItem, error) {
	scoped, err := t.app.getInScope(name, t.scope)
	if err != nil {
		return nil, err
	}
	if scoped == TaskItem(t) {
		unscoped, err := t.app.get(name)
		if err == nil && unscoped != nil {
			return unscoped, nil
		}
	}
	return scoped, nil
}

// Invoke runs the task, prerequisites first, under a fresh invocation chain —
// Rake::Task#invoke. args are the positional argument values bound to the
// task's declared arg names.
func (t *Task) Invoke(args ...string) error {
	ta := NewArgs(t.argNames, args)
	return t.invokeWithCallChain(ta, EmptyChain)
}

// invokeWithCallChain is the protected core (Rake::Task#invoke_with_call_chain):
// append self to the chain (raising on a cycle), honour the already-invoked
// guard (re-raising a remembered failure), mark invoked, run prerequisites in
// declared order depth-first, then execute if needed. A failure is remembered
// so a later invoke re-raises it.
func (t *Task) invokeWithCallChain(args Args, chain *InvocationChain) error {
	newChain, err := chain.Append(t.name)
	if err != nil {
		return err
	}

	if t.alreadyInvoked {
		if t.invocationErr != nil {
			return t.invocationErr
		}
		return nil
	}
	t.alreadyInvoked = true

	if err := t.invokePrerequisites(args, newChain); err != nil {
		t.invocationErr = err
		return err
	}
	if t.self().Needed() {
		if err := t.execute(args); err != nil {
			t.invocationErr = err
			return err
		}
	}
	return nil
}

// invokePrerequisites invokes each prerequisite in declared order, depth-first
// (Rake::Task#invoke_prerequisites; the serial, non-multitask path). Each
// prerequisite carries the growing chain so a cycle anywhere is detected.
func (t *Task) invokePrerequisites(args Args, chain *InvocationChain) error {
	pres, err := t.PrerequisiteTasks()
	if err != nil {
		return err
	}
	for _, p := range pres {
		prereqArgs := NewArgs(p.task().argNames, nil)
		if err := p.invokeWithCallChain(prereqArgs, chain); err != nil {
			return err
		}
	}
	return nil
}

// Execute runs the task's actions unconditionally (Rake::Task#execute), in the
// order they were added. With no actions it is a no-op (after rule
// enhancement, handled by the manager). args are passed to each action.
func (t *Task) Execute(args Args) error { return t.execute(args) }

func (t *Task) execute(args Args) error {
	// Rake::Task#execute: if the task has no actions, give the matching-rule
	// machinery a chance to attach some before running.
	if len(t.actions) == 0 && t.app != nil {
		_, _ = t.app.enhanceWithMatchingRule(t.name, 0)
	}
	for _, act := range t.self().task().actions {
		if err := act(t.self(), args); err != nil {
			return err
		}
	}
	return nil
}

// Needed reports whether a basic task's actions must run — always true
// (Rake::Task#needed?). FileTask overrides this.
func (t *Task) Needed() bool { return true }

// Timestamp is a basic task's time stamp — the current time
// (Rake::Task#timestamp). FileTask overrides this.
func (t *Task) Timestamp() time.Time { return nowFunc() }

// self returns the outermost TaskItem (the FileTask wrapper when present), so
// polymorphic Needed/Timestamp/actions dispatch correctly.
func (t *Task) self() TaskItem {
	if t.wrapper != nil {
		return t.wrapper
	}
	return t
}
