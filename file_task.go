// Copyright (c) the go-ruby-rake/rake authors
//
// SPDX-License-Identifier: BSD-3-Clause

package rake

import "time"

// late is Rake::LATE — a sentinel "infinitely future" time stamp returned by a
// FileTask whose file does not exist, so a missing target always counts as
// out-of-date against any real prerequisite. (MRI compares LATE > anything.)
var late = time.Unix(1<<62, 0)

// FileTask is a task with time-based dependencies — the port of Rake::FileTask.
// It rebuilds (its actions run) when its target file is missing or older than
// any prerequisite. The file system is a SEAM: the owning Application's Stat
// function maps a name to (mtime, exists); rbgo wires it to File.mtime, tests
// inject a deterministic map.
type FileTask struct {
	*Task
}

// newFileTask wraps a base Task as a FileTask and back-links the wrapper so the
// base invoke walk dispatches Needed/Timestamp to the FileTask overrides.
func newFileTask(taskName string, app *Application) *FileTask {
	base := newTask(taskName, app)
	ft := &FileTask{Task: base}
	base.wrapper = ft
	return ft
}

// stat resolves a file name to its mtime via the application seam, defaulting
// to "absent" when no seam is wired.
func (f *FileTask) stat(name string) (time.Time, bool) {
	if f.app == nil || f.app.Stat == nil {
		return time.Time{}, false
	}
	return f.app.Stat(name)
}

// Timestamp is the target file's mtime, or the LATE sentinel when the file is
// absent (Rake::FileTask#timestamp — rescue Errno::ENOENT → Rake::LATE).
func (f *FileTask) Timestamp() time.Time {
	if mt, ok := f.stat(f.name); ok {
		return mt
	}
	return late
}

// Needed reports whether the target must be rebuilt — true if it is missing or
// out of date against any prerequisite, or when build-all is forced
// (Rake::FileTask#needed?).
func (f *FileTask) Needed() bool {
	mt, ok := f.stat(f.name)
	if !ok {
		// Errno::ENOENT → always needed.
		return true
	}
	return f.outOfDate(mt) || f.app.BuildAll
}

// outOfDate reports whether any transitive prerequisite has a later time stamp
// than the target's (Rake::FileTask#out_of_date?). A FileTask prerequisite also
// forces a rebuild when build-all is set; a non-FileTask prerequisite (a basic
// task, whose stamp is Time.now) compares by stamp alone.
func (f *FileTask) outOfDate(stamp time.Time) bool {
	pres := f.allPrerequisiteTasks()
	for _, p := range pres {
		if _, isFile := p.(*FileTask); isFile {
			if p.Timestamp().After(stamp) || f.app.BuildAll {
				return true
			}
		} else {
			if p.Timestamp().After(stamp) {
				return true
			}
		}
	}
	return false
}

// allPrerequisiteTasks returns every unique transitive prerequisite task
// (Rake::Task#all_prerequisite_tasks). A name is recorded once; a cycle stops
// at the already-seen node (so this is cycle-safe even when invoke would later
// flag the cycle). Lookup errors are skipped — out_of_date? only consults tasks
// that resolve.
func (t *Task) allPrerequisiteTasks() []TaskItem {
	seen := map[string]TaskItem{}
	var order []string
	t.collectPrerequisites(seen, &order)
	out := make([]TaskItem, 0, len(order))
	for _, n := range order {
		out = append(out, seen[n])
	}
	return out
}

func (t *Task) collectPrerequisites(seen map[string]TaskItem, order *[]string) {
	pres, err := t.PrerequisiteTasks()
	if err != nil {
		return
	}
	for _, p := range pres {
		if _, ok := seen[p.Name()]; ok {
			continue
		}
		seen[p.Name()] = p
		*order = append(*order, p.Name())
		p.task().collectPrerequisites(seen, order)
	}
}
