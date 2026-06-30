// Copyright (c) the go-ruby-rake/rake authors
//
// SPDX-License-Identifier: BSD-3-Clause

package rake

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// TaskKind selects which Task subclass define_task creates — a plain task
// (`task`) or a file task (`file`). It is the Go stand-in for the task_class
// argument MRI's TaskManager#define_task receives.
type TaskKind int

const (
	// PlainTask is Rake::Task — namespace-scoped, always-needed.
	PlainTask TaskKind = iota
	// File is Rake::FileTask — file-name-keyed, timestamp-driven, scope-ignoring.
	FileKind
)

// Application is the task registry and namespace resolver — the port of
// Rake::Application together with the Rake::TaskManager mixin. It owns the task
// table, the rule list, the live namespace scope, and the pending description.
//
// Two seams hang off it: Stat resolves a file name to its mtime (for FileTask
// timestamps; rbgo wires File.mtime), and Glob lists files matching a pattern
// (for FileList; rbgo wires Dir.glob). Both are nil by default, which makes
// every file "absent" and every glob empty — fine for the pure task-graph tests.
type Application struct {
	tasks           map[string]TaskItem
	rules           []rule
	currentScope    *Scope
	lastDescription string
	anonSeed        int

	// RecordTaskMetadata mirrors TaskManager.record_task_metadata: when true,
	// define_task attaches the pending description to the task. MRI defaults it
	// off; the DSL turns it on. We default it on so desc/Comment round-trips.
	RecordTaskMetadata bool

	// BuildAll forces every FileTask to be considered out of date
	// (Rake's --build-all / options.build_all).
	BuildAll bool

	// Stat is the file-mtime seam (Rake::FileTask via File.mtime). It returns
	// the file's modification time and whether it exists. nil → always absent.
	Stat func(name string) (time.Time, bool)

	// Glob is the directory-glob seam (Rake::FileList.glob via Dir.glob). It
	// returns the names matching pattern. Results are sorted by FileList, as
	// MRI sorts Dir.glob output. nil → empty.
	Glob func(pattern string) []string
}

type rule struct {
	pattern    *regexp.Regexp
	rawPattern string
	argNames   []string
	deps       []string
	orderOnly  []string
	action     Action
}

// NewApplication returns an empty task manager with the top-level scope active
// and metadata recording on (Rake::Application.new + TaskManager#initialize).
func NewApplication() *Application {
	return &Application{
		tasks:              map[string]TaskItem{},
		currentScope:       nil, // EMPTY scope
		RecordTaskMetadata: true,
	}
}

// CurrentScope returns the live namespace scope (Rake::TaskManager#current_scope).
func (a *Application) CurrentScope() *Scope { return a.currentScope }

// Desc records the description applied to the next defined task
// (Rake's `desc` DSL → last_description). It is consumed by the following
// DefineTask, then cleared.
func (a *Application) Desc(description string) { a.lastDescription = description }

// getDescription returns and clears the pending description
// (Rake::TaskManager#get_description).
func (a *Application) getDescription() string {
	d := a.lastDescription
	a.lastDescription = ""
	return d
}

// DefineTask defines (or, when one already exists, enhances) a task — the port
// of Rake::TaskManager#define_task. For a PlainTask whose name embeds namespace
// segments ("a:b:c"), the leading segments extend the scope used to key the
// task (MRI splits on ":" and folds the outer segments into the scope); a
// FileTask ignores the scope entirely (its name is the file path).
//
// deps are the prerequisites, orderOnly the order-only prerequisites, argNames
// the declared argument names, and action an optional body. It returns the
// defined task.
func (a *Application) DefineTask(kind TaskKind, name string, argNames, deps, orderOnly []string, action Action) TaskItem {
	original := a.currentScope
	defer func() { a.currentScope = original }()

	taskName := name
	if kind != FileKind {
		// MRI: split "a:b:c" → name "c" with outer ["a","b"] folded onto scope.
		parts := strings.Split(taskName, ":")
		taskName = parts[len(parts)-1]
		outer := parts[:len(parts)-1]
		if len(outer) > 0 {
			// definition_scope (outer, outer→inner) + existing scope.
			s := original
			for _, seg := range outer {
				s = s.Cons(seg)
			}
			a.currentScope = s
		}
	}

	fqName := scopeName(kind, a.currentScope, taskName)
	task := a.intern(kind, fqName)
	if len(argNames) > 0 {
		task.task().setArgNames(argNames)
	}
	if a.RecordTaskMetadata {
		task.task().addComment(strings.TrimSpace(a.getDescription()))
	}
	task.task().Enhance(formatDeps(deps), action)
	if orderOnly != nil {
		task.task().AddOrderOnly(formatDeps(orderOnly))
	}
	return task
}

// scopeName applies the scope to a task name per kind (Task.scope_name /
// FileTask.scope_name). Plain tasks are namespace-prefixed; file tasks ignore
// the scope (the bare path).
func scopeName(kind TaskKind, scope *Scope, taskName string) string {
	if kind == FileKind {
		return taskName
	}
	return scope.PathWithTaskName(taskName)
}

// intern returns the existing task for fqName, or creates one of the given kind
// (Rake::TaskManager#intern — the @tasks[name] ||= new idiom).
func (a *Application) intern(kind TaskKind, fqName string) TaskItem {
	if t, ok := a.tasks[fqName]; ok {
		return t
	}
	var t TaskItem
	if kind == FileKind {
		t = newFileTask(fqName, a)
	} else {
		t = newTask(fqName, a)
	}
	a.tasks[fqName] = t
	return t
}

// Get resolves a task by name (Rake::TaskManager#[]): a straight lookup, else
// rule synthesis, else a file-task synthesized from an existing file, else the
// "Don't know how to build task" error.
func (a *Application) Get(taskName string) (TaskItem, error) { return a.get(taskName) }

func (a *Application) get(taskName string) (TaskItem, error) {
	if t := a.Lookup(taskName, nil); t != nil {
		return t, nil
	}
	if t, err := a.enhanceWithMatchingRule(taskName, 0); err != nil {
		return nil, err
	} else if t != nil {
		return t, nil
	}
	if t := a.synthesizeFileTask(taskName); t != nil {
		return t, nil
	}
	return nil, &UndefinedTaskError{Name: taskName}
}

// getInScope resolves a task name relative to an explicit scope (the form
// Rake::Task#lookup_prerequisite uses: application[name, scope]). Like Get it
// falls through to rule synthesis / file synthesis / the undefined error.
func (a *Application) getInScope(taskName string, scope *Scope) (TaskItem, error) {
	if t := a.Lookup(taskName, scope); t != nil {
		return t, nil
	}
	if t, err := a.enhanceWithMatchingRule(taskName, 0); err != nil {
		return nil, err
	} else if t != nil {
		return t, nil
	}
	if t := a.synthesizeFileTask(taskName); t != nil {
		return t, nil
	}
	return nil, &UndefinedTaskError{Name: taskName}
}

// synthesizeFileTask defines a no-action FileTask when an actual file matches
// the name (Rake::TaskManager#synthesize_file_task). Without a Stat seam no
// file ever exists, so this returns nil.
func (a *Application) synthesizeFileTask(taskName string) TaskItem {
	if a.Stat == nil {
		return nil
	}
	if _, ok := a.Stat(taskName); !ok {
		return nil
	}
	return a.DefineTask(FileKind, taskName, nil, nil, nil, nil)
}

// Lookup performs a straight scoped lookup with no rule/file synthesis
// (Rake::TaskManager#lookup). It honours the scope hints in the name:
// "rake:NAME" resolves at top level, "^…" trims that many scope levels.
// A nil scope means the current scope.
func (a *Application) Lookup(taskName string, initialScope *Scope) TaskItem {
	scope := initialScope
	if scope == nil {
		scope = a.currentScope
	}
	switch {
	case strings.HasPrefix(taskName, "rake:"):
		scope = nil // top-level
		taskName = strings.TrimPrefix(taskName, "rake:")
	case strings.HasPrefix(taskName, "^"):
		n := 0
		for n < len(taskName) && taskName[n] == '^' {
			n++
		}
		scope = scope.Trim(n)
		taskName = taskName[n:]
	}
	return a.lookupInScope(taskName, scope)
}

// lookupInScope walks outward from scope, trying scope:name at each level until
// a task is found or the top level is exhausted (Rake::TaskManager#lookup_in_scope).
func (a *Application) lookupInScope(name string, scope *Scope) TaskItem {
	for {
		tn := scope.PathWithTaskName(name)
		if t, ok := a.tasks[tn]; ok {
			return t
		}
		if scope.Empty() {
			break
		}
		scope = scope.Tail()
	}
	return nil
}

// TaskDefined reports whether a task with the name is already registered
// (Rake::Task.task_defined? → lookup != nil).
func (a *Application) TaskDefined(taskName string) bool {
	return a.Lookup(taskName, nil) != nil
}

// Tasks returns every defined task sorted by name (Rake::TaskManager#tasks).
func (a *Application) Tasks() []TaskItem {
	out := make([]TaskItem, 0, len(a.tasks))
	for _, t := range a.tasks {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// TasksInScope returns the tasks defined in scope and its sub-scopes
// (Rake::TaskManager#tasks_in_scope — names prefixed by "scope:").
func (a *Application) TasksInScope(scope *Scope) []TaskItem {
	prefix := scope.Path()
	var out []TaskItem
	for _, t := range a.Tasks() {
		if strings.HasPrefix(t.Name(), prefix+":") {
			out = append(out, t)
		}
	}
	return out
}

// Clear forgets every task and rule (Rake::TaskManager#clear).
func (a *Application) Clear() {
	a.tasks = map[string]TaskItem{}
	a.rules = nil
}

// InNamespace evaluates body with name pushed onto the scope, returning a
// NameSpace over the nested scope (Rake::TaskManager#in_namespace). A nil/empty
// name yields an anonymous "_anon_N" namespace.
func (a *Application) InNamespace(name string, body func(ns *NameSpace)) *NameSpace {
	if name == "" {
		name = a.generateName()
	}
	a.currentScope = a.currentScope.Cons(name)
	ns := &NameSpace{manager: a, scope: a.currentScope}
	defer func() { a.currentScope = a.currentScope.Tail() }()
	if body != nil {
		body(ns)
	}
	return ns
}

func (a *Application) generateName() string {
	a.anonSeed++
	return fmt.Sprintf("_anon_%d", a.anonSeed)
}

// CreateRule registers a synthesis rule (Rake::TaskManager#create_rule). The
// string pattern is regexp-quoted and anchored at end-of-name (MRI quotes it and
// appends "$"), so it always compiles. deps are the source extensions/names,
// action the rule body.
func (a *Application) CreateRule(pattern string, argNames, deps, orderOnly []string, action Action) {
	re := regexp.MustCompile(regexp.QuoteMeta(pattern) + "$")
	a.rules = append(a.rules, rule{
		pattern:    re,
		rawPattern: pattern,
		argNames:   argNames,
		deps:       deps,
		orderOnly:  orderOnly,
		action:     action,
	})
}

// enhanceWithMatchingRule tries each rule against taskName, synthesizing a
// FileTask from the first whose pattern matches and whose every source resolves
// (an existing file, a defined task, or another rule) — Rake::TaskManager#
// enhance_with_matching_rule. Returns nil when no rule applies; errors when the
// recursion guard (depth 16) trips.
func (a *Application) enhanceWithMatchingRule(taskName string, level int) (TaskItem, error) {
	if level >= 16 {
		return nil, &RuleRecursionOverflowError{Target: taskName}
	}
	for _, r := range a.rules {
		if r.pattern == nil || !r.pattern.MatchString(taskName) {
			continue
		}
		task, err := a.attemptRule(taskName, r, level)
		if err != nil {
			return nil, err
		}
		if task != nil {
			if r.orderOnly != nil {
				task.task().AddOrderOnly(formatDeps(r.orderOnly))
			}
			return task, nil
		}
	}
	return nil, nil
}

// attemptRule builds the rule's source list, resolves each source, and on full
// success defines the FileTask with those sources as prerequisites
// (Rake::TaskManager#attempt_rule). A source that resolves through a nested
// rule contributes that rule's task name; an unresolvable source aborts the rule.
func (a *Application) attemptRule(taskName string, r rule, level int) (TaskItem, error) {
	sources := a.makeSources(taskName, r)
	prereqs := make([]string, 0, len(sources))
	for _, src := range sources {
		switch {
		case a.fileExists(src) || a.TaskDefined(src):
			prereqs = append(prereqs, src)
		default:
			parent, err := a.enhanceWithMatchingRule(src, level+1)
			if err != nil {
				return nil, err
			}
			if parent == nil {
				return nil, nil // FAIL — no source, rule does not apply
			}
			prereqs = append(prereqs, parent.Name())
		}
	}
	t := a.DefineTask(FileKind, taskName, r.argNames, prereqs, nil, r.action)
	t.task().sourcesSet = true
	t.task().sources = prereqs
	return t, nil
}

// makeSources turns a rule's source extensions into concrete source names for
// taskName (Rake::TaskManager#make_sources). Supported forms: a "." extension
// (replace the matched suffix / swap the file extension) and a plain
// string/name (used verbatim). The proc/pathmap forms stay host-side.
func (a *Application) makeSources(taskName string, r rule) []string {
	out := make([]string, 0, len(r.deps))
	for _, ext := range r.deps {
		switch {
		case strings.HasPrefix(ext, "."):
			// Replace the part the pattern matched with ext; if that is a no-op
			// (the suffix did not match), swap the file extension instead.
			src := r.pattern.ReplaceAllString(taskName, ext)
			if src == taskName || src == ext {
				src = swapExt(taskName, ext)
			}
			out = append(out, src)
		default:
			out = append(out, ext)
		}
	}
	return out
}

// fileExists reports whether name resolves to a file via the Stat seam.
func (a *Application) fileExists(name string) bool {
	if a.Stat == nil {
		return false
	}
	_, ok := a.Stat(name)
	return ok
}

// swapExt replaces (or appends) a file's extension (String#ext) — used by the
// rule source builder when the suffix substitution is a no-op.
func swapExt(name, newExt string) string {
	if i := strings.LastIndex(name, "."); i >= 0 && !strings.ContainsAny(name[i:], "/\\") {
		return name[:i] + newExt
	}
	return name + newExt
}

// formatDeps normalises a dependency list (Rake::Task.format_deps — pass-through
// for plain strings; nil → empty).
func formatDeps(deps []string) []string {
	if deps == nil {
		return nil
	}
	return append([]string(nil), deps...)
}

// UndefinedTaskError is the error Get raises for an unknown task. Message
// mirrors MRI's "Don't know how to build task '…'".
type UndefinedTaskError struct{ Name string }

func (e *UndefinedTaskError) Error() string {
	return fmt.Sprintf("Don't know how to build task '%s'", e.Name)
}

// RuleRecursionOverflowError is Rake::RuleRecursionOverflowError — raised when
// rule synthesis recurses past 16 levels.
type RuleRecursionOverflowError struct{ Target string }

func (e *RuleRecursionOverflowError) Error() string {
	return "Rule Recursion Too Deep: " + e.Target
}

// NameSpace is the lookup object returned by InNamespace (Rake::NameSpace): it
// resolves names within, and lists the tasks of, a captured scope.
type NameSpace struct {
	manager *Application
	scope   *Scope
}

// Get resolves name within the namespace's scope (Rake::NameSpace#[]).
func (n *NameSpace) Get(name string) TaskItem { return n.manager.Lookup(name, n.scope) }

// Scope returns the namespace's scope (Rake::NameSpace#scope).
func (n *NameSpace) Scope() *Scope { return n.scope }

// Tasks lists the tasks defined in this and nested namespaces
// (Rake::NameSpace#tasks).
func (n *NameSpace) Tasks() []TaskItem { return n.manager.TasksInScope(n.scope) }
