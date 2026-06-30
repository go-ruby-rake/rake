// Copyright (c) the go-ruby-rake/rake authors
//
// SPDX-License-Identifier: BSD-3-Clause

package rake

import (
	"errors"
	"reflect"
	"regexp"
	"testing"
	"time"
)

// recorder collects the order tasks execute in, so prerequisite ordering and
// the invoke-once guard can be asserted deterministically.
type recorder struct{ order []string }

func (r *recorder) act(name string) Action {
	return func(t TaskItem, a Args) error {
		r.order = append(r.order, name)
		return nil
	}
}

func def(t *testing.T, app *Application, name string, deps []string, a Action) TaskItem {
	t.Helper()
	return app.DefineTask(PlainTask, name, nil, deps, nil, a)
}

// TestInvokeOrderDepthFirst asserts the depth-first, prerequisite-first,
// each-once invoke order matches the rake gem: a, b, c, top.
func TestInvokeOrderDepthFirst(t *testing.T) {
	app := NewApplication()
	r := &recorder{}
	def(t, app, "a", nil, r.act("a"))
	def(t, app, "b", []string{"a"}, r.act("b"))
	def(t, app, "c", []string{"a", "b"}, r.act("c"))
	top := def(t, app, "top", []string{"c", "b"}, r.act("top"))
	if err := top.task().Invoke(); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	want := []string{"a", "b", "c", "top"}
	if !reflect.DeepEqual(r.order, want) {
		t.Fatalf("order = %v, want %v", r.order, want)
	}
}

// TestInvokeOnceGuard asserts a task runs at most once per invoke chain and
// that Reenable lets it run again — matching Task#already_invoked / #reenable.
func TestInvokeOnceGuard(t *testing.T) {
	app := NewApplication()
	n := 0
	once := app.DefineTask(PlainTask, "once", nil, nil, nil, func(TaskItem, Args) error {
		n++
		return nil
	})
	_ = once.task().Invoke()
	_ = once.task().Invoke()
	if n != 1 {
		t.Fatalf("after two invokes n=%d, want 1", n)
	}
	once.task().Reenable()
	if once.task().AlreadyInvoked() {
		t.Fatal("Reenable should clear already-invoked")
	}
	_ = once.task().Invoke()
	if n != 2 {
		t.Fatalf("after reenable+invoke n=%d, want 2", n)
	}
}

// TestCircularDependency asserts the cycle detector raises with MRI's exact
// "Circular dependency detected: TOP => x => y => x" message.
func TestCircularDependency(t *testing.T) {
	app := NewApplication()
	def(t, app, "x", []string{"y"}, nil)
	y := def(t, app, "y", []string{"x"}, nil)
	_ = y // referenced to define
	x := app.Lookup("x", nil)
	err := x.task().Invoke()
	var ce *CircularDependencyError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v (%T), want *CircularDependencyError", err, err)
	}
	want := "Circular dependency detected: TOP => x => y => x"
	if ce.Error() != want {
		t.Fatalf("message = %q, want %q", ce.Error(), want)
	}
}

// TestRememberedFailure asserts a failed invocation is remembered and re-raised
// on a later invoke (the @invocation_exception path), and that an unrelated
// task that already ran returns its remembered success silently.
func TestRememberedFailure(t *testing.T) {
	app := NewApplication()
	boom := errors.New("boom")
	bad := app.DefineTask(PlainTask, "bad", nil, nil, nil, func(TaskItem, Args) error {
		return boom
	})
	if err := bad.task().Invoke(); !errors.Is(err, boom) {
		t.Fatalf("first invoke err=%v, want boom", err)
	}
	if err := bad.task().Invoke(); !errors.Is(err, boom) {
		t.Fatalf("second invoke must re-raise remembered err, got %v", err)
	}
	// A task that already succeeded returns nil on re-invoke.
	good := app.DefineTask(PlainTask, "good", nil, nil, nil, nil)
	_ = good.task().Invoke()
	if err := good.task().Invoke(); err != nil {
		t.Fatalf("re-invoke of succeeded task: %v", err)
	}
}

// TestPrerequisiteFailurePropagates asserts a failing prerequisite aborts the
// dependent task before its action runs.
func TestPrerequisiteFailurePropagates(t *testing.T) {
	app := NewApplication()
	boom := errors.New("dep-failed")
	def(t, app, "dep", nil, func(TaskItem, Args) error { return boom })
	ran := false
	top := app.DefineTask(PlainTask, "top", nil, []string{"dep"}, nil, func(TaskItem, Args) error {
		ran = true
		return nil
	})
	if err := top.task().Invoke(); !errors.Is(err, boom) {
		t.Fatalf("err=%v, want boom", err)
	}
	if ran {
		t.Fatal("top action ran despite failing prerequisite")
	}
}

// TestNamespaceResolution asserts namespace bookkeeping: nested tasks key under
// "outer:inner", prerequisites resolve within the namespace, and a "^"-prefixed
// prerequisite climbs to the enclosing scope.
func TestNamespaceResolution(t *testing.T) {
	app := NewApplication()
	r := &recorder{}
	app.InNamespace("outer", func(ns *NameSpace) {
		app.DefineTask(PlainTask, "inner", nil, nil, nil, r.act("outer:inner"))
		app.DefineTask(PlainTask, "wrap", nil, []string{"inner"}, nil, r.act("outer:wrap"))
	})
	main := def(t, app, "main", []string{"outer:wrap"}, r.act("main"))
	if err := main.task().Invoke(); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	want := []string{"outer:inner", "outer:wrap", "main"}
	if !reflect.DeepEqual(r.order, want) {
		t.Fatalf("order=%v want %v", r.order, want)
	}
	if got := app.Lookup("outer:inner", nil); got == nil || got.Name() != "outer:inner" {
		t.Fatalf("lookup outer:inner = %v", got)
	}
}

// TestCaretScopeTrim asserts "^shared" from inside a namespace resolves to the
// top-level shared task, not the namespace-local one.
func TestCaretScopeTrim(t *testing.T) {
	app := NewApplication()
	r := &recorder{}
	def(t, app, "shared", nil, r.act("top:shared"))
	app.InNamespace("n", func(ns *NameSpace) {
		app.DefineTask(PlainTask, "shared", nil, nil, nil, r.act("n:shared"))
		app.DefineTask(PlainTask, "use", nil, []string{"^shared"}, nil, r.act("n:use"))
	})
	use := app.Lookup("n:use", nil)
	if err := use.task().Invoke(); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	want := []string{"top:shared", "n:use"}
	if !reflect.DeepEqual(r.order, want) {
		t.Fatalf("order=%v want %v", r.order, want)
	}
}

// TestEmbeddedNamespaceName asserts a task defined with an embedded namespace
// path ("a:b:c") keys under that full path and folds the leading segments into
// the scope.
func TestEmbeddedNamespaceName(t *testing.T) {
	app := NewApplication()
	ti := app.DefineTask(PlainTask, "a:b:c", nil, nil, nil, nil)
	if ti.Name() != "a:b:c" {
		t.Fatalf("name=%q want a:b:c", ti.Name())
	}
	if app.Lookup("a:b:c", nil) == nil {
		t.Fatal("a:b:c not found")
	}
}

// TestRakePrefixLookup asserts a "rake:"-prefixed lookup resolves at top level
// even from inside a namespace.
func TestRakePrefixLookup(t *testing.T) {
	app := NewApplication()
	def(t, app, "build", nil, nil)
	var got TaskItem
	app.InNamespace("ns", func(ns *NameSpace) {
		got = app.Lookup("rake:build", nil)
	})
	if got == nil || got.Name() != "build" {
		t.Fatalf("rake:build = %v", got)
	}
}

// fixedTime injects a deterministic mtime map as the Stat seam.
func statMap(m map[string]time.Time) func(string) (time.Time, bool) {
	return func(name string) (time.Time, bool) {
		ts, ok := m[name]
		return ts, ok
	}
}

// TestFileTaskNeeded covers the three FileTask#needed? cases against injected
// mtimes: target newer than source (not needed), source newer (needed), target
// missing (needed).
func TestFileTaskNeeded(t *testing.T) {
	base := time.Unix(1000, 0)
	app := NewApplication()
	fs := map[string]time.Time{
		"src.txt": base,
		"out.txt": base.Add(time.Second), // out newer than src
	}
	app.Stat = statMap(fs)
	app.DefineTask(FileKind, "src.txt", nil, nil, nil, nil)
	outTI := app.DefineTask(FileKind, "out.txt", nil, []string{"src.txt"}, nil, nil)
	out := outTI.(*FileTask)

	if out.Needed() {
		t.Fatal("out newer than src: needed? should be false")
	}
	// src now newer than out.
	fs["src.txt"] = base.Add(2 * time.Second)
	if !out.Needed() {
		t.Fatal("src newer than out: needed? should be true")
	}
	// out missing.
	delete(fs, "out.txt")
	if !out.Needed() {
		t.Fatal("out missing: needed? should be true")
	}
	// Timestamp of a missing file is the LATE sentinel.
	if !out.Timestamp().Equal(late) {
		t.Fatalf("missing-file timestamp=%v, want late", out.Timestamp())
	}
}

// TestFileTaskBuildAll asserts the build-all flag forces a fresh-looking
// FileTask to be considered needed.
func TestFileTaskBuildAll(t *testing.T) {
	base := time.Unix(1000, 0)
	app := NewApplication()
	app.Stat = statMap(map[string]time.Time{
		"src": base,
		"out": base.Add(time.Second),
	})
	app.DefineTask(FileKind, "src", nil, nil, nil, nil)
	outTI := app.DefineTask(FileKind, "out", nil, []string{"src"}, nil, nil)
	out := outTI.(*FileTask)
	if out.Needed() {
		t.Fatal("precondition: should be up to date")
	}
	app.BuildAll = true
	if !out.Needed() {
		t.Fatal("BuildAll must force needed")
	}
}

// TestFileTaskNonFilePrereq asserts a FileTask whose prerequisite is a plain
// task (whose timestamp is "now", always later than a fixed mtime) is needed.
func TestFileTaskNonFilePrereq(t *testing.T) {
	app := NewApplication()
	app.Stat = statMap(map[string]time.Time{"out": time.Unix(1000, 0)})
	def(t, app, "gen", nil, nil) // plain task, timestamp = now
	outTI := app.DefineTask(FileKind, "out", nil, []string{"gen"}, nil, nil)
	out := outTI.(*FileTask)
	if !out.Needed() {
		t.Fatal("plain-task prereq (now) is newer than fixed mtime -> needed")
	}
}

// TestFileTaskNoStatSeam asserts that with no Stat seam every file is absent,
// so a FileTask is always needed and its timestamp is LATE.
func TestFileTaskNoStatSeam(t *testing.T) {
	app := NewApplication()
	ti := app.DefineTask(FileKind, "x", nil, nil, nil, nil)
	ft := ti.(*FileTask)
	if !ft.Needed() {
		t.Fatal("no Stat seam -> always needed")
	}
	if !ft.Timestamp().Equal(late) {
		t.Fatal("no Stat seam -> LATE timestamp")
	}
}

// TestRuleSynthesis asserts a pattern rule (".o" => ".c") synthesizes a file
// task whose source is the defined ".c" task, and that invoking runs the rule
// action with the right source.
func TestRuleSynthesis(t *testing.T) {
	app := NewApplication()
	var gotSource, gotName string
	app.CreateRule(".o", nil, []string{".c"}, nil, func(ti TaskItem, a Args) error {
		gotSource = ti.task().Source()
		gotName = ti.Name()
		return nil
	})
	app.DefineTask(PlainTask, "foo.c", nil, nil, nil, nil)
	task, err := app.Get("foo.o")
	if err != nil {
		t.Fatalf("get foo.o: %v", err)
	}
	if err := task.task().Invoke(); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if gotSource != "foo.c" || gotName != "foo.o" {
		t.Fatalf("rule ran with source=%q name=%q, want foo.c/foo.o", gotSource, gotName)
	}
}

// TestRuleNoMatch asserts a rule whose source cannot be resolved does not apply,
// leaving the task undefined.
func TestRuleNoMatch(t *testing.T) {
	app := NewApplication()
	app.CreateRule(".o", nil, []string{".c"}, nil, func(TaskItem, Args) error { return nil })
	// foo.c is neither a file nor a defined task -> rule fails -> undefined.
	if _, err := app.Get("foo.o"); err == nil {
		t.Fatal("expected undefined-task error")
	}
}

// TestUndefinedTask asserts the "Don't know how to build task" error.
func TestUndefinedTask(t *testing.T) {
	app := NewApplication()
	_, err := app.Get("nope")
	var ue *UndefinedTaskError
	if !errors.As(err, &ue) {
		t.Fatalf("err=%v want *UndefinedTaskError", err)
	}
	if ue.Error() != "Don't know how to build task 'nope'" {
		t.Fatalf("msg=%q", ue.Error())
	}
}

// TestSynthesizeFileTask asserts an existing on-disk file (Stat seam) is
// synthesized into a no-action FileTask when looked up.
func TestSynthesizeFileTask(t *testing.T) {
	app := NewApplication()
	app.Stat = statMap(map[string]time.Time{"README": time.Unix(5, 0)})
	ti, err := app.Get("README")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, ok := ti.(*FileTask); !ok {
		t.Fatalf("synthesized task is %T, want *FileTask", ti)
	}
}

// TestEnhanceAndClear covers enhance union semantics, order-only prereqs, and
// clear.
func TestEnhanceAndClear(t *testing.T) {
	app := NewApplication()
	ti := app.DefineTask(PlainTask, "t", nil, []string{"a"}, []string{"e"}, nil)
	tk := ti.task()
	tk.Enhance([]string{"a", "b"}, nil) // "a" deduped, "b" added
	if got := tk.Prerequisites(); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("prereqs=%v", got)
	}
	if got := tk.OrderOnlyPrerequisites(); !reflect.DeepEqual(got, []string{"e"}) {
		t.Fatalf("order-only=%v", got)
	}
	tk.AddOrderOnly([]string{"a"}) // already an ordinary prereq -> skipped
	tk.AddOrderOnly([]string{"e"}) // already order-only -> skipped
	tk.AddOrderOnly([]string{"f"})
	if got := tk.OrderOnlyPrerequisites(); !reflect.DeepEqual(got, []string{"e", "f"}) {
		t.Fatalf("order-only after add=%v", got)
	}
	tk.Clear()
	if len(tk.Prerequisites()) != 0 || len(tk.Actions()) != 0 || len(tk.OrderOnlyPrerequisites()) != 0 {
		t.Fatal("clear did not empty the task")
	}
}

// TestDescriptionAndComment covers desc bookkeeping, comment de-dup, the
// full-comment join, and the first-sentence comment.
func TestDescriptionAndComment(t *testing.T) {
	app := NewApplication()
	app.Desc("Build the thing. Extra detail.")
	ti := app.DefineTask(PlainTask, "t", nil, nil, nil, nil)
	app.Desc("Build the thing. Extra detail.") // duplicate -> not added twice
	app.DefineTask(PlainTask, "t", nil, nil, nil, nil)
	app.Desc("Second comment")
	app.DefineTask(PlainTask, "t", nil, nil, nil, nil)
	tk := ti.task()
	if got := tk.FullComment(); got != "Build the thing. Extra detail.\nSecond comment" {
		t.Fatalf("full=%q", got)
	}
	if got := tk.Comment(); got != "Build the thing / Second comment" {
		t.Fatalf("comment=%q", got)
	}
	// A task with no comments.
	plain := app.DefineTask(PlainTask, "u", nil, nil, nil, nil)
	if plain.task().FullComment() != "" || plain.task().Comment() != "" {
		t.Fatal("no-comment task should yield empty strings")
	}
}

// TestFirstSentenceEdgeCases exercises the sentence splitter: decimal point not
// a terminator, bang terminator, newline terminator, no terminator.
func TestFirstSentenceEdgeCases(t *testing.T) {
	cases := map[string]string{
		"Pi is 3.14 ok. yes": "Pi is 3.14 ok",
		"Bang! more":         "Bang",
		"line one\nline two": "line one",
		"no terminator here": "no terminator here",
		"Ends with period.":  "Ends with period",
		"Ends with bang!":    "Ends with bang",
	}
	for in, want := range cases {
		if got := firstSentenceOf(in); got != want {
			t.Errorf("firstSentenceOf(%q)=%q want %q", in, got, want)
		}
	}
}

// TestRecordMetadataOff asserts that with metadata recording off, desc is not
// attached.
func TestRecordMetadataOff(t *testing.T) {
	app := NewApplication()
	app.RecordTaskMetadata = false
	app.Desc("ignored")
	ti := app.DefineTask(PlainTask, "t", nil, nil, nil, nil)
	if ti.task().FullComment() != "" {
		t.Fatal("metadata off: comment should be empty")
	}
}

// TestTasksAndScope covers Tasks (sorted), TasksInScope, NameSpace methods,
// Clear, and TaskDefined.
func TestTasksAndScope(t *testing.T) {
	app := NewApplication()
	def(t, app, "zebra", nil, nil)
	def(t, app, "alpha", nil, nil)
	var ns *NameSpace
	ns = app.InNamespace("grp", func(n *NameSpace) {
		app.DefineTask(PlainTask, "one", nil, nil, nil, nil)
		app.DefineTask(PlainTask, "two", nil, nil, nil, nil)
	})
	all := app.Tasks()
	if all[0].Name() != "alpha" {
		t.Fatalf("tasks not sorted: %v", names(all))
	}
	if !app.TaskDefined("grp:one") {
		t.Fatal("grp:one should be defined")
	}
	inScope := app.TasksInScope(ns.Scope())
	if len(inScope) != 2 {
		t.Fatalf("in-scope=%v", names(inScope))
	}
	if got := ns.Get("one"); got == nil || got.Name() != "grp:one" {
		t.Fatalf("ns.Get(one)=%v", got)
	}
	if len(ns.Tasks()) != 2 {
		t.Fatalf("ns.Tasks()=%v", names(ns.Tasks()))
	}
	app.Clear()
	if len(app.Tasks()) != 0 {
		t.Fatal("clear did not empty registry")
	}
}

// TestAnonymousNamespace asserts an empty namespace name generates "_anon_N".
func TestAnonymousNamespace(t *testing.T) {
	app := NewApplication()
	var s1, s2 *Scope
	app.InNamespace("", func(ns *NameSpace) { s1 = ns.Scope() })
	app.InNamespace("", func(ns *NameSpace) { s2 = ns.Scope() })
	if s1.Head() != "_anon_1" || s2.Head() != "_anon_2" {
		t.Fatalf("anon names = %q, %q", s1.Head(), s2.Head())
	}
}

func names(ts []TaskItem) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name()
	}
	return out
}

// TestArgs covers argument binding/lookup including missing and out-of-range.
func TestArgs(t *testing.T) {
	a := NewArgs([]string{"x", "y"}, []string{"1"})
	if a.Lookup("x") != "1" {
		t.Fatalf("x=%q", a.Lookup("x"))
	}
	if a.Lookup("y") != "" {
		t.Fatalf("y(unbound)=%q", a.Lookup("y"))
	}
	if a.Lookup("z") != "" {
		t.Fatalf("z(undeclared)=%q", a.Lookup("z"))
	}
	if !reflect.DeepEqual(a.Names(), []string{"x", "y"}) {
		t.Fatalf("names=%v", a.Names())
	}
	if !reflect.DeepEqual(a.Values(), []string{"1"}) {
		t.Fatalf("values=%v", a.Values())
	}
}

// TestArgNamesViaInvoke asserts declared arg names are stored on the task and
// passed through invoke to the action.
func TestArgNamesViaInvoke(t *testing.T) {
	app := NewApplication()
	var got string
	ti := app.DefineTask(PlainTask, "greet", []string{"who"}, nil, nil, func(t TaskItem, a Args) error {
		got = a.Lookup("who")
		return nil
	})
	if !reflect.DeepEqual(ti.task().ArgNames(), []string{"who"}) {
		t.Fatalf("arg names=%v", ti.task().ArgNames())
	}
	if err := ti.task().Invoke("world"); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if got != "world" {
		t.Fatalf("got=%q", got)
	}
}

// TestExecuteRunsActionsUnconditionally asserts Execute runs actions directly
// regardless of needed?.
func TestExecuteRunsActions(t *testing.T) {
	app := NewApplication()
	n := 0
	ti := app.DefineTask(PlainTask, "t", nil, nil, nil, func(TaskItem, Args) error {
		n++
		return nil
	})
	if err := ti.task().Execute(NewArgs(nil, nil)); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if n != 1 {
		t.Fatalf("execute n=%d", n)
	}
}

// TestSourcesDefaultAndExplicit covers Source/Sources defaulting to prereqs and
// the explicit-sources path used by rules.
func TestSourcesDefaultAndExplicit(t *testing.T) {
	app := NewApplication()
	ti := app.DefineTask(PlainTask, "t", nil, []string{"p1", "p2"}, nil, nil)
	if got := ti.task().Sources(); !reflect.DeepEqual(got, []string{"p1", "p2"}) {
		t.Fatalf("default sources=%v", got)
	}
	if ti.task().Source() != "p1" {
		t.Fatalf("source=%q", ti.task().Source())
	}
	// No-prereq task -> empty source.
	empty := app.DefineTask(PlainTask, "e", nil, nil, nil, nil)
	if empty.task().Source() != "" {
		t.Fatalf("empty source=%q", empty.task().Source())
	}
}

// --- FileList ---

func globMap(m map[string][]string) func(string) []string {
	return func(p string) []string { return m[p] }
}

// TestFileListIncludeExclude covers literal includes, glob expansion via the
// seam, default ignores, and regexp/glob/literal/predicate excludes.
func TestFileListIncludeExclude(t *testing.T) {
	g := globMap(map[string][]string{
		"lib/*.rb": {"lib/b.rb", "lib/a.rb", "lib/a.bak"}, // unsorted on purpose
		"*.o":      {"x.o", "core"},
	})
	fl := NewFileList(g, "lib/*.rb")
	fl.Include("README", "*.o")
	got := fl.To()
	// Globs are sorted before adding; a.bak is a default ignore pattern, so it
	// is dropped. The "*.o" glob {x.o, core} sorts to {core, x.o}. Expected:
	// lib/a.rb, lib/b.rb (sorted glob), README (literal), then core, x.o.
	want := []string{"lib/a.rb", "lib/b.rb", "README", "core", "x.o"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%v want %v", got, want)
	}

	// Now exclude with each matcher kind.
	fl2 := NewFileList(g, "lib/*.rb")
	fl2.Include("README")
	fl2.ExcludeRegexp(regexp.MustCompile(`README`))
	fl2.Exclude("lib/a.rb") // literal
	if got := fl2.To(); !reflect.DeepEqual(got, []string{"lib/b.rb"}) {
		t.Fatalf("after exclude got=%v", got)
	}
}

// TestFileListGlobExclude covers a glob-pattern exclude applied after resolve.
func TestFileListGlobExclude(t *testing.T) {
	g := globMap(map[string][]string{"src/*": {"src/a.c", "src/b.h", "src/c.c"}})
	fl := NewFileList(g, "src/*")
	fl.Exclude("src/*.h") // glob exclude
	if got := fl.To(); !reflect.DeepEqual(got, []string{"src/a.c", "src/c.c"}) {
		t.Fatalf("got=%v", got)
	}
}

// TestFileListExcludeFunc covers a predicate exclude and exclude applied while
// not pending (immediate resolve_exclude).
func TestFileListExcludeFunc(t *testing.T) {
	fl := NewFileList(nil)
	fl.Include("keep.txt", "drop.txt")
	fl.Resolve() // no longer pending
	fl.ExcludeFunc(func(s string) bool { return s == "drop.txt" })
	if got := fl.To(); !reflect.DeepEqual(got, []string{"keep.txt"}) {
		t.Fatalf("got=%v", got)
	}
	// Exclude (literal) while not pending also resolves immediately.
	fl.Exclude("keep.txt")
	if got := fl.To(); len(got) != 0 {
		t.Fatalf("got=%v want empty", got)
	}
	// ExcludeRegexp while not pending.
	fl3 := NewFileList(nil).Include("a", "b")
	fl3.Resolve()
	fl3.ExcludeRegexp(regexp.MustCompile("^a$"))
	if got := fl3.To(); !reflect.DeepEqual(got, []string{"b"}) {
		t.Fatalf("got=%v", got)
	}
}

// TestFileListClearExclude asserts clearing excludes lets default-ignored files
// through.
func TestFileListClearExclude(t *testing.T) {
	g := globMap(map[string][]string{"*": {"a.rb", "a.bak"}})
	fl := NewFileList(g, "*")
	fl.ClearExclude()
	if got := fl.To(); !reflect.DeepEqual(got, []string{"a.bak", "a.rb"}) {
		t.Fatalf("got=%v", got)
	}
}

// TestFileListTransforms covers Sub, Ext, Existing, String, and the nil-glob
// path.
func TestFileListTransforms(t *testing.T) {
	fl := NewFileList(nil, "a.c", "b.c")
	if got := fl.Sub(regexp.MustCompile(`\.c$`), ".o").To(); !reflect.DeepEqual(got, []string{"a.o", "b.o"}) {
		t.Fatalf("sub=%v", got)
	}
	if got := fl.Ext(".o").To(); !reflect.DeepEqual(got, []string{"a.o", "b.o"}) {
		t.Fatalf("ext=%v", got)
	}
	if got := fl.String(); got != "a.c b.c" {
		t.Fatalf("string=%q", got)
	}
	exists := func(s string) bool { return s == "a.c" }
	if got := fl.Existing(exists).To(); !reflect.DeepEqual(got, []string{"a.c"}) {
		t.Fatalf("existing=%v", got)
	}
	if got := fl.Existing(nil).To(); len(got) != 0 {
		t.Fatalf("existing(nil)=%v", got)
	}
}

// TestSwapExt covers extension swapping incl. the no-extension and
// path-with-dot-in-dir cases.
func TestSwapExt(t *testing.T) {
	cases := map[string]string{
		"foo.c":     ".o",
		"foo":       ".o",
		"dir.x/foo": ".o",
	}
	wants := map[string]string{
		"foo.c":     "foo.o",
		"foo":       "foo.o",
		"dir.x/foo": "dir.x/foo.o",
	}
	for in, ext := range cases {
		if got := swapExt(in, ext); got != wants[in] {
			t.Errorf("swapExt(%q,%q)=%q want %q", in, ext, got, wants[in])
		}
	}
}

// --- LinkedList-derived structures ---

// TestScope covers path, path-with-name, trim past top, and ScopeMake.
func TestScope(t *testing.T) {
	s := ScopeMake("a", "b") // outer a, inner b
	if s.Path() != "a:b" {
		t.Fatalf("path=%q", s.Path())
	}
	if s.PathWithTaskName("t") != "a:b:t" {
		t.Fatalf("pwtn=%q", s.PathWithTaskName("t"))
	}
	if s.Trim(5).Path() != "" {
		t.Fatal("trim past top should yield empty scope")
	}
	if s.Trim(1).Path() != "a" {
		t.Fatalf("trim1=%q", s.Trim(1).Path())
	}
	var empty *Scope
	if !empty.Empty() || empty.Path() != "" || empty.PathWithTaskName("t") != "t" {
		t.Fatal("empty scope semantics")
	}
	if empty.Head() != "" || empty.Tail() != nil {
		t.Fatal("empty head/tail")
	}
	if s.Tail().Head() != "a" {
		t.Fatalf("tail head=%q", s.Tail().Head())
	}
}

// TestInvocationChain covers member, append, string, and the cycle error.
func TestInvocationChain(t *testing.T) {
	c1, err := EmptyChain.Append("a")
	if err != nil {
		t.Fatal(err)
	}
	c2, _ := c1.Append("b")
	if c2.String() != "TOP => a => b" {
		t.Fatalf("string=%q", c2.String())
	}
	if !c2.Member("a") || c2.Member("z") {
		t.Fatal("member")
	}
	if EmptyChain.Member("x") {
		t.Fatal("empty chain has no members")
	}
	if _, err := c2.Append("a"); err == nil {
		t.Fatal("expected circular error")
	}
}
