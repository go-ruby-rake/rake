// Copyright (c) the go-ruby-rake/rake authors
//
// SPDX-License-Identifier: BSD-3-Clause

package rake

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// rubyBin locates a usable `ruby` once. The oracle tests skip themselves when it
// is absent (the qemu cross-arch lanes and the Windows lane), so the
// deterministic in-process suite alone drives the 100% gate there.
func rubyBin(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("ruby")
	if err != nil {
		t.Skip("ruby not on PATH; skipping rake-gem oracle")
	}
	// Require the rake gem; skip if it is not installed.
	if err := exec.Command(path, "-rrake", "-e", "Rake::VERSION").Run(); err != nil {
		t.Skip("rake gem not available; skipping rake-gem oracle")
	}
	return path
}

// rubyRake runs a Ruby script under the rake library and returns its trimmed
// stdout. The script $stdout.binmode's itself so Windows text-mode never turns
// our "\n"-joined comparison strings into "\r\n".
func rubyRake(t *testing.T, bin, script string) string {
	t.Helper()
	cmd := exec.Command(bin, "-rrake", "-e", "$stdout.binmode\n"+script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ruby error: %v\nscript:\n%s\noutput:\n%s", err, script, out)
	}
	return strings.TrimRight(string(out), "\n")
}

// TestOracleInvokeOrder asserts the depth-first, prerequisite-first, each-once
// invoke order this engine produces matches the order the real rake gem runs the
// same task graph in.
func TestOracleInvokeOrder(t *testing.T) {
	bin := rubyBin(t)
	rubyOrder := rubyRake(t, bin, `
order = []
Rake::Task.define_task(:a) { order << "a" }
Rake::Task.define_task(:b => :a) { order << "b" }
Rake::Task.define_task(:c => [:a, :b]) { order << "c" }
Rake::Task.define_task(:top => [:c, :b]) { order << "top" }
Rake::Task[:top].invoke
$stdout.write(order.join(" "))`)

	app := NewApplication()
	r := &recorder{}
	app.DefineTask(PlainTask, "a", nil, nil, nil, r.act("a"))
	app.DefineTask(PlainTask, "b", nil, []string{"a"}, nil, r.act("b"))
	app.DefineTask(PlainTask, "c", nil, []string{"a", "b"}, nil, r.act("c"))
	top := app.DefineTask(PlainTask, "top", nil, []string{"c", "b"}, nil, r.act("top"))
	if err := top.task().Invoke(); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	goOrder := strings.Join(r.order, " ")
	if goOrder != rubyOrder {
		t.Fatalf("invoke order mismatch:\n  go  = %q\n  ruby= %q", goOrder, rubyOrder)
	}
}

// TestOracleNamespaceOrder asserts namespace resolution + prerequisite ordering
// matches rake for a nested-namespace graph (including a "^"-scoped prereq).
func TestOracleNamespaceOrder(t *testing.T) {
	bin := rubyBin(t)
	rubyOrder := rubyRake(t, bin, `
order = []
task :shared do order << "top:shared" end
namespace :n do
  task :shared do order << "n:shared" end
  task :inner do order << "n:inner" end
  task :use => ["inner", "^shared"] do order << "n:use" end
end
task :main => "n:use" do order << "main" end
Rake::Task[:main].invoke
$stdout.write(order.join(" "))`)

	app := NewApplication()
	r := &recorder{}
	app.DefineTask(PlainTask, "shared", nil, nil, nil, r.act("top:shared"))
	app.InNamespace("n", func(ns *NameSpace) {
		app.DefineTask(PlainTask, "shared", nil, nil, nil, r.act("n:shared"))
		app.DefineTask(PlainTask, "inner", nil, nil, nil, r.act("n:inner"))
		app.DefineTask(PlainTask, "use", nil, []string{"inner", "^shared"}, nil, r.act("n:use"))
	})
	main := app.DefineTask(PlainTask, "main", nil, []string{"n:use"}, nil, r.act("main"))
	if err := main.task().Invoke(); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	goOrder := strings.Join(r.order, " ")
	if goOrder != rubyOrder {
		t.Fatalf("namespace order mismatch:\n  go  = %q\n  ruby= %q", goOrder, rubyOrder)
	}
}

// TestOracleCircular asserts this engine's circular-dependency message is
// byte-identical to the rake gem's.
func TestOracleCircular(t *testing.T) {
	bin := rubyBin(t)
	rubyMsg := rubyRake(t, bin, `
Rake::Task.define_task(:x => :y)
Rake::Task.define_task(:y => :x)
begin
  Rake::Task[:x].invoke
rescue RuntimeError => e
  $stdout.write(e.message)
end`)

	app := NewApplication()
	app.DefineTask(PlainTask, "x", nil, []string{"y"}, nil, nil)
	app.DefineTask(PlainTask, "y", nil, []string{"x"}, nil, nil)
	err := app.Lookup("x", nil).task().Invoke()
	if err == nil {
		t.Fatal("expected circular error")
	}
	if err.Error() != rubyMsg {
		t.Fatalf("circular message mismatch:\n  go  = %q\n  ruby= %q", err.Error(), rubyMsg)
	}
}

// TestOracleFileTaskNeeded asserts FileTask#needed? parity with rake across the
// three cases (target up to date, source newer, target missing) by replaying
// the same mtimes through the rake gem and through this engine.
func TestOracleFileTaskNeeded(t *testing.T) {
	bin := rubyBin(t)
	// Drive rake's FileTask#needed? with real temp files at controlled mtimes.
	rubyResult := rubyRake(t, bin, `
require "tempfile"
require "fileutils"
dir = Dir.mktmpdir
src = File.join(dir, "src"); out = File.join(dir, "out")
results = []
# Case 1: out newer than src -> not needed.
File.write(src, "s"); sleep 0.02; File.write(out, "o")
ft = Rake::FileTask.define_task(out => src)
results << ft.needed?
# Case 2: src newer than out -> needed.
sleep 0.02; File.write(src, "s2")
results << ft.needed?
# Case 3: out missing -> needed.
File.delete(out)
results << ft.needed?
FileUtils.rm_rf(dir)
$stdout.write(results.map { |b| b ? "1" : "0" }.join)`)

	// Mirror the same three states through this engine with injected mtimes.
	base := time.Unix(1000, 0)
	app := NewApplication()
	fs := map[string]time.Time{"src": base, "out": base.Add(time.Second)}
	app.Stat = statMap(fs)
	app.DefineTask(FileKind, "src", nil, nil, nil, nil)
	out := app.DefineTask(FileKind, "out", nil, []string{"src"}, nil, nil).(*FileTask)

	var got strings.Builder
	got.WriteString(boolDigit(out.Needed())) // case 1: not needed
	fs["src"] = base.Add(2 * time.Second)
	got.WriteString(boolDigit(out.Needed())) // case 2: needed
	delete(fs, "out")
	got.WriteString(boolDigit(out.Needed())) // case 3: needed

	if got.String() != rubyResult {
		t.Fatalf("FileTask needed? mismatch:\n  go  = %q\n  ruby= %q", got.String(), rubyResult)
	}
	if rubyResult != "011" {
		t.Fatalf("unexpected rake oracle result %q (want 011)", rubyResult)
	}
}

func boolDigit(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
