// Copyright (c) the go-ruby-rake/rake authors
//
// SPDX-License-Identifier: BSD-3-Clause

package rake

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

// TestSimpleGetters covers the trivial accessors that the behavioural tests do
// not otherwise touch.
func TestSimpleGetters(t *testing.T) {
	app := NewApplication()
	if !app.CurrentScope().Empty() {
		t.Fatal("fresh app should have empty current scope")
	}
	ti := app.DefineTask(PlainTask, "t", nil, nil, nil, nil)
	if !ti.task().Scope().Empty() {
		t.Fatal("top-level task scope should be empty")
	}
	app.InNamespace("ns", func(ns *NameSpace) {
		nested := app.DefineTask(PlainTask, "x", nil, nil, nil, nil)
		if nested.task().Scope().Path() != "ns" {
			t.Fatalf("nested scope path=%q", nested.task().Scope().Path())
		}
		if app.CurrentScope().Path() != "ns" {
			t.Fatalf("current scope=%q", app.CurrentScope().Path())
		}
	})
}

// TestErrorStrings covers the error message formatters.
func TestErrorStrings(t *testing.T) {
	if (&RuleRecursionOverflowError{Target: "x.o"}).Error() != "Rule Recursion Too Deep: x.o" {
		t.Fatal("rule recursion error message")
	}
	if (&UndefinedTaskError{Name: "z"}).Error() != "Don't know how to build task 'z'" {
		t.Fatal("undefined task error message")
	}
}

// TestGetInScopeFileSynth covers getInScope falling through to file synthesis
// (the prerequisite-lookup path used by FileTask out_of_date?).
func TestGetInScopeFileSynth(t *testing.T) {
	app := NewApplication()
	app.Stat = statMap(map[string]time.Time{"data.bin": time.Unix(1, 0)})
	ti, err := app.getInScope("data.bin", nil)
	if err != nil {
		t.Fatalf("getInScope: %v", err)
	}
	if _, ok := ti.(*FileTask); !ok {
		t.Fatalf("want synthesized FileTask, got %T", ti)
	}
}

// TestGetInScopeUndefined covers getInScope's undefined-task error.
func TestGetInScopeUndefined(t *testing.T) {
	app := NewApplication()
	if _, err := app.getInScope("ghost", nil); err == nil {
		t.Fatal("expected undefined error")
	}
}

// TestPrerequisiteLookupError covers a prerequisite that cannot be resolved:
// PrerequisiteTasks and invoke both surface the undefined-task error.
func TestPrerequisiteLookupError(t *testing.T) {
	app := NewApplication()
	top := app.DefineTask(PlainTask, "top", nil, []string{"missing"}, nil, nil)
	if _, err := top.task().PrerequisiteTasks(); err == nil {
		t.Fatal("PrerequisiteTasks should error on missing prereq")
	}
	if err := top.task().Invoke(); err == nil {
		t.Fatal("Invoke should error on missing prereq")
	}
}

// TestUnscopedPrerequisiteFallback covers lookup_prerequisite preferring the
// unscoped task when the scoped lookup resolves to the task itself — a task in a
// namespace depending on a same-named task at the top level.
func TestUnscopedPrerequisiteFallback(t *testing.T) {
	app := NewApplication()
	r := &recorder{}
	// Top-level "lib" task.
	def(t, app, "lib", nil, r.act("top:lib"))
	app.InNamespace("lib", func(ns *NameSpace) {
		// A task named "lib" inside namespace "lib" -> "lib:lib" — but we want a
		// prereq "lib" that, scoped, resolves to itself, forcing the unscoped
		// fallback to the top-level "lib".
		app.DefineTask(PlainTask, "lib", nil, []string{"lib"}, nil, r.act("lib:lib"))
	})
	// Invoke lib:lib; its prereq "lib" scoped is itself -> falls back to top "lib".
	libInNs := app.Lookup("lib:lib", nil)
	if err := libInNs.task().Invoke(); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	want := []string{"top:lib", "lib:lib"}
	if !reflect.DeepEqual(r.order, want) {
		t.Fatalf("order=%v want %v", r.order, want)
	}
}

// TestLookupInScopeExhausted covers lookupInScope walking out to the top level
// and finding nothing.
func TestLookupInScopeExhausted(t *testing.T) {
	app := NewApplication()
	var got TaskItem
	app.InNamespace("a", func(ns *NameSpace) {
		app.InNamespace("b", func(ns2 *NameSpace) {
			got = app.Lookup("nowhere", nil) // walks a:b -> a -> top, all miss
		})
	})
	if got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

// TestCreateRuleValid asserts a registered rule whose pattern matches nothing
// yields no synthesis (enhance returns nil, nil).
func TestCreateRuleValid(t *testing.T) {
	app := NewApplication()
	app.CreateRule(".x", nil, nil, nil, nil)
	// No rules match an unrelated name -> nil, nil.
	got, err := app.enhanceWithMatchingRule("zzz", 0)
	if err != nil || got != nil {
		t.Fatalf("enhance=%v,%v want nil,nil", got, err)
	}
}

// TestRuleRecursionOverflow drives rule synthesis past the depth-16 guard with a
// self-referential extension chain (".a" => ".a"), and asserts the overflow
// error carries the original target.
func TestRuleRecursionOverflow(t *testing.T) {
	app := NewApplication()
	// A rule whose source is produced by the same rule, never resolving to a
	// file or task -> recursion until the guard trips.
	app.CreateRule(".a", nil, []string{".a"}, nil, func(TaskItem, Args) error { return nil })
	_, err := app.enhanceWithMatchingRule("foo.a", 0)
	var oe *RuleRecursionOverflowError
	if !errors.As(err, &oe) {
		t.Fatalf("err=%v want overflow", err)
	}
}

// TestGetRuleErrorPropagates covers Get and getInScope surfacing a rule
// recursion-overflow error raised during synthesis (rather than swallowing it
// as "no rule matched").
func TestGetRuleErrorPropagates(t *testing.T) {
	app := NewApplication()
	app.CreateRule(".a", nil, []string{".a"}, nil, func(TaskItem, Args) error { return nil })
	if _, err := app.Get("foo.a"); err == nil {
		t.Fatal("Get should surface the recursion-overflow error")
	}
	var oe *RuleRecursionOverflowError
	if _, err := app.getInScope("bar.a", nil); !errors.As(err, &oe) {
		t.Fatalf("getInScope err=%v want overflow", err)
	}
}

// TestGetInScopeRuleSynth covers getInScope resolving a name through a matching
// rule — the path taken when a task's prerequisite is itself rule-synthesized
// during prerequisite_tasks lookup.
func TestGetInScopeRuleSynth(t *testing.T) {
	app := NewApplication()
	app.DefineTask(PlainTask, "foo.c", nil, nil, nil, nil)
	app.CreateRule(".o", nil, []string{".c"}, nil, func(TaskItem, Args) error { return nil })
	ti, err := app.getInScope("foo.o", nil)
	if err != nil {
		t.Fatalf("getInScope: %v", err)
	}
	if ti.Name() != "foo.o" {
		t.Fatalf("name=%q", ti.Name())
	}
}

// TestRuleOrderOnly covers a rule that attaches order-only prerequisites to the
// synthesized task.
func TestRuleOrderOnly(t *testing.T) {
	app := NewApplication()
	app.DefineTask(PlainTask, "foo.c", nil, nil, nil, nil)
	def(t, app, "setup", nil, nil)
	app.CreateRule(".o", nil, []string{".c"}, []string{"setup"}, func(TaskItem, Args) error { return nil })
	ti, err := app.Get("foo.o")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got := ti.task().OrderOnlyPrerequisites(); !reflect.DeepEqual(got, []string{"setup"}) {
		t.Fatalf("order-only=%v", got)
	}
}

// TestRuleNestedSource covers a rule whose source is itself produced by another
// rule (the ENHANCE branch of attempt_rule).
func TestRuleNestedSource(t *testing.T) {
	app := NewApplication()
	// .o <= .c ; .c <= .y ; foo.y is a defined task.
	app.DefineTask(PlainTask, "foo.y", nil, nil, nil, nil)
	app.CreateRule(".c", nil, []string{".y"}, nil, func(TaskItem, Args) error { return nil })
	app.CreateRule(".o", nil, []string{".c"}, nil, func(TaskItem, Args) error { return nil })
	ti, err := app.Get("foo.o")
	if err != nil {
		t.Fatalf("get foo.o: %v", err)
	}
	// foo.o's source should be the synthesized foo.c.
	if ti.task().Source() != "foo.c" {
		t.Fatalf("source=%q want foo.c", ti.task().Source())
	}
	if !app.TaskDefined("foo.c") {
		t.Fatal("nested foo.c should have been synthesized")
	}
}

// TestRuleLiteralSource covers make_sources' plain-string branch (a literal
// source name, not an extension).
func TestRuleLiteralSource(t *testing.T) {
	app := NewApplication()
	def(t, app, "common", nil, nil) // a literal source that is a defined task
	app.CreateRule(".o", nil, []string{"common"}, nil, func(TaskItem, Args) error { return nil })
	ti, err := app.Get("foo.o")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if ti.task().Source() != "common" {
		t.Fatalf("source=%q want common", ti.task().Source())
	}
}

// TestRuleSuffixReplace covers make_sources' branch where the pattern suffix is
// replaced in place (rather than the extension being swapped). With pattern
// ".o" matching the trailing ".o", replacing it with ".c" yields foo.c — but to
// exercise the *replace* path distinctly we use a source that exists as a file.
func TestRuleSuffixReplace(t *testing.T) {
	app := NewApplication()
	app.Stat = statMap(map[string]time.Time{"foo.c": time.Unix(1, 0)})
	app.CreateRule(".o", nil, []string{".c"}, nil, func(TaskItem, Args) error { return nil })
	ti, err := app.Get("foo.o")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if ti.task().Source() != "foo.c" {
		t.Fatalf("source=%q", ti.task().Source())
	}
}

// TestFileExistsTrue covers fileExists' positive branch via the Stat seam.
func TestFileExistsTrue(t *testing.T) {
	app := NewApplication()
	app.Stat = statMap(map[string]time.Time{"present": time.Unix(1, 0)})
	if !app.fileExists("present") {
		t.Fatal("present should exist")
	}
	if app.fileExists("absent") {
		t.Fatal("absent should not exist")
	}
}

// TestCollectPrerequisitesCycleSafe covers all_prerequisite_tasks stopping at an
// already-seen node when the graph contains a cycle (so out_of_date? is
// cycle-safe even though invoke would flag the cycle).
func TestCollectPrerequisitesCycleSafe(t *testing.T) {
	app := NewApplication()
	app.Stat = statMap(map[string]time.Time{}) // nothing exists -> FileTasks LATE
	// a -> b -> a cycle, all FileTasks.
	app.DefineTask(FileKind, "a", nil, []string{"b"}, nil, nil)
	bTI := app.DefineTask(FileKind, "b", nil, []string{"a"}, nil, nil)
	b := bTI.(*FileTask)
	// allPrerequisiteTasks must terminate (cycle-safe) and include a (then its
	// prereq b is already seen).
	pres := b.allPrerequisiteTasks()
	got := names(pres)
	// b.collect: pre=a (record a); a.collect: pre=b (record b); b.collect: pre=a
	// (already seen, skipped). So the unique transitive set is [a, b].
	if !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("collected=%v want [a b]", got)
	}
}

// TestCollectPrerequisitesLookupError covers collect_prerequisites silently
// skipping a prerequisite that fails to resolve (out_of_date? only consults
// resolvable tasks).
func TestCollectPrerequisitesLookupError(t *testing.T) {
	app := NewApplication()
	app.Stat = statMap(map[string]time.Time{"out": time.Unix(10, 0)})
	// "out" depends on "phantom" which never resolves; allPrerequisiteTasks
	// swallows the error and returns nothing, so needed? is false.
	outTI := app.DefineTask(FileKind, "out", nil, []string{"phantom"}, nil, nil)
	out := outTI.(*FileTask)
	pres := out.allPrerequisiteTasks()
	if len(pres) != 0 {
		t.Fatalf("expected no resolvable prereqs, got %v", names(pres))
	}
	if out.Needed() {
		t.Fatal("with no resolvable prereqs and present file, needed? should be false")
	}
}

// TestDefineTaskFileKindIgnoresScope asserts a FileTask defined inside a
// namespace keys by its bare path (file tasks ignore the scope).
func TestDefineTaskFileKindIgnoresScope(t *testing.T) {
	app := NewApplication()
	var ti TaskItem
	app.InNamespace("ns", func(ns *NameSpace) {
		ti = app.DefineTask(FileKind, "build/out.o", nil, nil, nil, nil)
	})
	if ti.Name() != "build/out.o" {
		t.Fatalf("file task name=%q, want unscoped build/out.o", ti.Name())
	}
}
