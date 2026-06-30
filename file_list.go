// Copyright (c) the go-ruby-rake/rake authors
//
// SPDX-License-Identifier: BSD-3-Clause

package rake

import (
	"path"
	"regexp"
	"sort"
	"strings"
)

// globMeta matches the glob metacharacters Rake uses to decide whether an
// included pattern needs filesystem expansion (Rake::FileList::GLOB_PATTERN,
// %r{[*?\[\{]}).
var globMeta = regexp.MustCompile(`[*?\[{]`)

// defaultIgnorePatterns are the patterns FileList excludes by default
// (Rake::FileList::DEFAULT_IGNORE_PATTERNS): CVS/.svn dirs, *.bak, and editor
// backups ending in "~".
var defaultIgnorePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(^|[/\\])CVS([/\\]|$)`),
	regexp.MustCompile(`(^|[/\\])\.svn([/\\]|$)`),
	regexp.MustCompile(`\.bak$`),
	regexp.MustCompile(`~$`),
}

// FileList is a lazily-resolved list of file names — the port of
// Rake::FileList. Include records glob patterns and literal names; Exclude
// records patterns (regexp, glob, literal, or predicate) to filter out.
// Resolution is deferred: the patterns are expanded (via the Glob seam) and the
// excludes applied only when the list is observed (To / Resolve). The actual
// Dir.glob is the seam — a func(pattern) []string injected at construction;
// everything else (literal adds, default ignores, exclude filtering) is pure.
type FileList struct {
	glob            func(pattern string) []string
	pendingAdd      []string
	pending         bool
	items           []string
	excludePatterns []excludeMatcher
	excludeProcs    []func(string) bool
}

// excludeMatcher is one exclude entry, kept as a typed pattern so resolution
// matches MRI's case-by-type dispatch (Regexp vs glob vs literal).
type excludeMatcher struct {
	re      *regexp.Regexp // a regexp exclude
	glob    string         // a glob exclude (contains glob metacharacters)
	literal string         // a literal-name exclude
}

// NewFileList creates a FileList over the given include patterns, expanding
// globs through glob (the Dir.glob seam; nil → no expansion). It seeds the
// default ignore patterns, exactly as Rake::FileList#initialize does.
func NewFileList(glob func(pattern string) []string, patterns ...string) *FileList {
	fl := &FileList{glob: glob}
	for _, re := range defaultIgnorePatterns {
		fl.excludePatterns = append(fl.excludePatterns, excludeMatcher{re: re})
	}
	for _, p := range patterns {
		fl.Include(p)
	}
	return fl
}

// Include records file names / glob patterns to add (Rake::FileList#include).
// Resolution is deferred until the list is observed. Returns fl.
func (fl *FileList) Include(filenames ...string) *FileList {
	fl.pendingAdd = append(fl.pendingAdd, filenames...)
	fl.pending = true
	return fl
}

// Exclude records patterns to remove (Rake::FileList#exclude). A pattern with
// glob metacharacters is matched as a glob, otherwise as a literal name; use
// ExcludeRegexp for a regexp and ExcludeFunc for a predicate. When nothing is
// pending, the exclusion is applied immediately (MRI's resolve_exclude unless
// @pending). Returns fl.
func (fl *FileList) Exclude(patterns ...string) *FileList {
	for _, p := range patterns {
		if globMeta.MatchString(p) {
			fl.excludePatterns = append(fl.excludePatterns, excludeMatcher{glob: p})
		} else {
			fl.excludePatterns = append(fl.excludePatterns, excludeMatcher{literal: p})
		}
	}
	if !fl.pending {
		fl.resolveExclude()
	}
	return fl
}

// ExcludeRegexp records a regexp exclude (the Regexp branch of
// Rake::FileList#exclude). Returns fl.
func (fl *FileList) ExcludeRegexp(res ...*regexp.Regexp) *FileList {
	for _, re := range res {
		fl.excludePatterns = append(fl.excludePatterns, excludeMatcher{re: re})
	}
	if !fl.pending {
		fl.resolveExclude()
	}
	return fl
}

// ExcludeFunc records a predicate exclude — entries for which fn returns true
// are removed (the block form of Rake::FileList#exclude). Returns fl.
func (fl *FileList) ExcludeFunc(fn func(string) bool) *FileList {
	fl.excludeProcs = append(fl.excludeProcs, fn)
	if !fl.pending {
		fl.resolveExclude()
	}
	return fl
}

// ClearExclude drops every exclude pattern and predicate, including the default
// ignores (Rake::FileList#clear_exclude). Returns fl.
func (fl *FileList) ClearExclude() *FileList {
	fl.excludePatterns = nil
	fl.excludeProcs = nil
	return fl
}

// Resolve expands pending includes (globs via the seam, literals verbatim) and
// applies the excludes (Rake::FileList#resolve). Idempotent until the next
// Include. Returns fl.
func (fl *FileList) Resolve() *FileList {
	if !fl.pending {
		return fl
	}
	fl.pending = false
	for _, fn := range fl.pendingAdd {
		fl.resolveAdd(fn)
	}
	fl.pendingAdd = nil
	fl.resolveExclude()
	return fl
}

// resolveAdd expands one include entry: a glob pattern is matched against the
// filesystem (excludes applied as each match is added), a literal is appended
// (Rake::FileList#resolve_add).
func (fl *FileList) resolveAdd(fn string) {
	if globMeta.MatchString(fn) {
		fl.addMatching(fn)
	} else {
		fl.items = append(fl.items, fn)
	}
}

// addMatching globs pattern (via the seam, sorted as MRI sorts Dir.glob) and
// appends each match not already excluded (Rake::FileList#add_matching).
func (fl *FileList) addMatching(pattern string) {
	var matches []string
	if fl.glob != nil {
		matches = append(matches, fl.glob(pattern)...)
	}
	sort.Strings(matches)
	for _, fn := range matches {
		if !fl.excludedFromList(fn) {
			fl.items = append(fl.items, fn)
		}
	}
}

// resolveExclude removes every currently-excluded entry (Rake::FileList#
// resolve_exclude → reject!).
func (fl *FileList) resolveExclude() {
	kept := fl.items[:0]
	for _, fn := range fl.items {
		if !fl.excludedFromList(fn) {
			kept = append(kept, fn)
		}
	}
	fl.items = kept
}

// excludedFromList reports whether fn matches any exclude pattern or predicate
// (Rake::FileList#excluded_from_list?). Glob excludes use pathname-aware
// fnmatch semantics.
func (fl *FileList) excludedFromList(fn string) bool {
	for _, m := range fl.excludePatterns {
		switch {
		case m.re != nil:
			if m.re.MatchString(fn) {
				return true
			}
		case m.glob != "":
			if matchedGlob, _ := path.Match(m.glob, fn); matchedGlob {
				return true
			}
		default:
			if fn == m.literal {
				return true
			}
		}
	}
	for _, p := range fl.excludeProcs {
		if p(fn) {
			return true
		}
	}
	return false
}

// To resolves the list and returns its file names (Rake::FileList#to_a).
func (fl *FileList) To() []string {
	fl.Resolve()
	return append([]string(nil), fl.items...)
}

// String resolves and joins the list with spaces (Rake::FileList#to_s).
func (fl *FileList) String() string {
	return strings.Join(fl.To(), " ")
}

// Sub returns a new list with re→rep applied to every entry
// (Rake::FileList#sub).
func (fl *FileList) Sub(re *regexp.Regexp, rep string) *FileList {
	out := NewFileList(fl.glob)
	out.ClearExclude()
	for _, fn := range fl.To() {
		out.items = append(out.items, re.ReplaceAllString(fn, rep))
	}
	return out
}

// Ext returns a new list with each entry's file extension swapped to newExt
// (Rake::FileList#ext → String#ext).
func (fl *FileList) Ext(newExt string) *FileList {
	out := NewFileList(fl.glob)
	out.ClearExclude()
	for _, fn := range fl.To() {
		out.items = append(out.items, swapExt(fn, newExt))
	}
	return out
}

// Existing returns a new list of the entries that exist on the filesystem
// (Rake::FileList#existing → File.exist?), via the Stat-like exists predicate.
// exists is the filesystem seam (rbgo wires File.exist?); nil → nothing exists.
func (fl *FileList) Existing(exists func(string) bool) *FileList {
	out := NewFileList(fl.glob)
	out.ClearExclude()
	seen := map[string]bool{}
	for _, fn := range fl.To() {
		if exists != nil && exists(fn) && !seen[fn] {
			seen[fn] = true
			out.items = append(out.items, fn)
		}
	}
	return out
}
