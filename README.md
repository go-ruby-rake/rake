<p align="center"><img src="https://raw.githubusercontent.com/go-ruby-rake/brand/main/social/go-ruby-rake-rake.png" alt="go-ruby-rake/rake" width="720"></p>

# rake — go-ruby-rake

[![Docs](https://img.shields.io/badge/docs-mkdocs--material-DC2626)](https://go-ruby-rake.github.io/docs/)
[![License](https://img.shields.io/badge/license-BSD--3--Clause-blue)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.26.4%2B-00ADD8)](https://go.dev/dl/)
[![Coverage](https://img.shields.io/badge/coverage-100%25-1a7f37)](#tests--coverage)

**A pure-Go (no cgo) reimplementation of the task-graph core of Ruby's
[Rake](https://github.com/ruby/rake)** — the `rake-13.x` gem's `Rake::Task` /
`Rake::FileTask`, the `Rake::TaskManager` registry with namespace/scope
resolution and rule synthesis, `Rake::FileList` filtering, and
`Rake::InvocationChain` circular-dependency detection. It reproduces Rake's
depth-first prerequisite-first **invoke order**, the **invoke-once guard**,
**circular detection**, **FileTask `needed?`** timestamp logic, and
**namespace resolution** — validated byte-for-byte against the real `rake` gem —
**without any Ruby runtime**.

It is the Rake backend for
[go-embedded-ruby](https://github.com/go-embedded-ruby/ruby), but is a
**standalone, reusable** module — a sibling of
[go-ruby-marshal](https://github.com/go-ruby-marshal/marshal),
[go-ruby-yaml](https://github.com/go-ruby-yaml/yaml),
[go-ruby-regexp](https://github.com/go-ruby-regexp/regexp),
[go-ruby-erb](https://github.com/go-ruby-erb/erb), and
[go-ruby-pstore](https://github.com/go-ruby-pstore/pstore).

> **What it is — and isn't.** The *pure-compute* half of Rake — the task DAG, the
> depth-first prerequisite-first invoke order, the invoke-once guard, circular
> detection, FileTask out-of-date timestamp logic, namespace/scope lookup, rule
> synthesis, and FileList pattern/exclude filtering — is fully deterministic and
> needs **no interpreter**, so it lives here as plain Go. The *effectful* half —
> the action bodies (the `do ... end` blocks), `sh` / `FileUtils`, real
> `Dir.glob`, and file mtimes — are **seams** the host injects: an `Action` is a
> Go func rbgo wires to a Ruby block; `FileTask` timestamps come from an injected
> `Stat` function, and `FileList` globbing from an injected `Glob` function.
> Tests inject deterministic seams, so the whole suite is Ruby-free and
> reproducible across arches.

## Features

Faithful port of Rake's task-graph semantics, validated against the `rake` gem
(`ruby -rrake`, Rake 13.x) on every supported platform:

- **Depth-first, prerequisite-first invoke order** — `Task#invoke` runs every
  prerequisite (ordinary then order-only, in declared order) before the task's
  own actions, depth-first, **each task at most once** (the
  `@already_invoked` guard); `Reenable` clears the guard, and a remembered
  failure is re-raised on a later invoke.
- **Circular-dependency detection** — `Rake::InvocationChain` raises with MRI's
  exact `"Circular dependency detected: TOP => x => y => x"` message.
- **`FileTask#needed?`** — a file target is rebuilt when it is missing or older
  than any transitive prerequisite (or when build-all is forced); a missing file
  reports the `Rake::LATE` distant-future time stamp. Timestamps are injected, so
  the logic is tested deterministically.
- **Namespace / scope resolution** — `namespace`/`task`/`file` bookkeeping,
  `define_task`, `lookup`/`[]`, embedded-namespace names (`"a:b:c"`), the
  `"rake:"` (top-level) and `"^"` (scope-trim) hints, `synthesize_file_task`, and
  the `desc` comment/description table.
- **Rule synthesis** — `rule pattern => sources` matches a task name, resolves
  each source (existing file / defined task / nested rule), and synthesizes a
  `FileTask` with those sources as prerequisites, guarding against runaway
  recursion (`Rake::RuleRecursionOverflowError`).
- **`Rake::FileList`** — lazy include/exclude filtering with default ignore
  patterns; regexp, glob, literal, and predicate excludes; `sub`/`ext`/`existing`
  transforms. The actual `Dir.glob` is a seam.

CGO-free, **100% test coverage**, `gofmt` + `go vet` clean, and green across the
six 64-bit Go targets (amd64, arm64, riscv64, loong64, ppc64le, s390x) and three
OSes (Linux, macOS, Windows).

## Install

```sh
go get github.com/go-ruby-rake/rake
```

## Usage

```go
package main

import (
	"fmt"

	"github.com/go-ruby-rake/rake"
)

func main() {
	app := rake.NewApplication()

	var order []string
	rec := func(name string) rake.Action {
		return func(t rake.TaskItem, a rake.Args) error {
			order = append(order, name) // the action body is a seam; here a closure
			return nil
		}
	}

	// task :a ; task :b => :a ; task :top => [:b, :a]
	app.DefineTask(rake.PlainTask, "a", nil, nil, nil, rec("a"))
	app.DefineTask(rake.PlainTask, "b", nil, []string{"a"}, nil, rec("b"))
	top := app.DefineTask(rake.PlainTask, "top", nil, []string{"b", "a"}, nil, rec("top"))

	_ = top.(*rake.Task).Invoke()
	fmt.Println(order) // [a b top] — prerequisites first, each once
}
```

## API

```go
// Application is the task registry + namespace resolver (Rake::Application /
// Rake::TaskManager). Two seams hang off it: Stat (FileTask mtimes, File.mtime)
// and Glob (FileList expansion, Dir.glob).
func NewApplication() *Application
func (a *Application) DefineTask(kind TaskKind, name string, argNames, deps, orderOnly []string, action Action) TaskItem
func (a *Application) Get(taskName string) (TaskItem, error)            // Rake::Task[]
func (a *Application) Lookup(taskName string, scope *Scope) TaskItem    // straight scoped lookup
func (a *Application) TaskDefined(taskName string) bool
func (a *Application) Tasks() []TaskItem                                // sorted by name
func (a *Application) InNamespace(name string, body func(ns *NameSpace)) *NameSpace
func (a *Application) CreateRule(pattern string, argNames, deps, orderOnly []string, action Action)
func (a *Application) Desc(description string)

// Stat / Glob / BuildAll are the injected seams + flag.
type Application struct {
	Stat     func(name string) (time.Time, bool) // File.mtime seam
	Glob     func(pattern string) []string       // Dir.glob seam
	BuildAll bool                                 // --build-all
	// ...
}

// TaskItem is the behaviour shared by Task and FileTask.
type TaskItem interface {
	Name() string
	Needed() bool
	Timestamp() time.Time
	// ...
}

// Task (Rake::Task): the basic unit of work.
func (t *Task) Invoke(args ...string) error
func (t *Task) Execute(args Args) error
func (t *Task) Enhance(deps []string, action Action) *Task
func (t *Task) Reenable()
func (t *Task) PrerequisiteTasks() ([]TaskItem, error)
func (t *Task) Comment() string
func (t *Task) FullComment() string

// FileTask (Rake::FileTask): timestamp-driven; Needed/Timestamp override Task.
type FileTask struct{ *Task }

// Action is a task action body — the seam for a Ruby `do ... end` block.
type Action func(t TaskItem, args Args) error

// InvocationChain (Rake::InvocationChain): circular-dependency detection.
type InvocationChain struct{ /* … */ }
func (c *InvocationChain) Append(invocation string) (*InvocationChain, error)
type CircularDependencyError struct{ Message string }

// FileList (Rake::FileList): lazy include/exclude filtering; Glob is a seam.
func NewFileList(glob func(pattern string) []string, patterns ...string) *FileList
func (fl *FileList) Include(filenames ...string) *FileList
func (fl *FileList) Exclude(patterns ...string) *FileList
func (fl *FileList) To() []string
```

## What rbgo binds

The host (`rbgo`) wires the seams to the real Ruby runtime and filesystem:

- each `Action` invokes the corresponding Ruby task block (the `do ... end`
  body), where `sh` / `FileUtils` live;
- `Application.Stat` → `File.mtime` (FileTask freshness);
- `Application.Glob` → `Dir.glob` (FileList expansion);
- argument values flow through `Args` to `Rake::TaskArguments`.

Everything else — the DAG, the invoke order and once-guard, circular detection,
FileTask `needed?`, namespace/scope/rule resolution, and FileList filtering — is
this library.

## Tests & coverage

The suite pairs deterministic, Ruby-free tests over the in-process task graph
(which alone hold coverage at 100%, so the qemu cross-arch and Windows lanes pass
the gate) with a **differential rake-gem oracle**: it replays the same task
graphs through the real `ruby -rrake` and asserts identical invoke order,
namespace resolution, the byte-exact circular-dependency message, and the three
`FileTask#needed?` outcomes. The oracle scripts `$stdout.binmode` so Windows
text-mode never corrupts the comparison, and skip themselves where `ruby` (or the
rake gem) is absent.

```sh
COVERPKG=$(go list ./... | paste -sd, -)
go test -race -coverpkg="$COVERPKG" -coverprofile=cover.out ./...
go tool cover -func=cover.out | tail -1   # 100.0%
```

## License

BSD-3-Clause — see [LICENSE](LICENSE). Copyright the go-ruby-rake/rake authors.
