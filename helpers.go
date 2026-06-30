// Copyright (c) the go-ruby-rake/rake authors
//
// SPDX-License-Identifier: BSD-3-Clause

package rake

import (
	"strings"
	"time"
)

// nowFunc is the clock seam for Task#timestamp (a basic task's stamp is
// Time.now). The host leaves it; tests override it for determinism.
var nowFunc = time.Now

// contains reports whether s appears in list (the cheap, order-preserving set
// membership used by Rake's |= union semantics for prerequisites/comments).
func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// joinComments renders a task's comment list (Rake::Task#full_comment /
// #comment). With firstSentence set, each comment is reduced to its first
// sentence before joining (the one-line #comment form). An empty list yields "".
func joinComments(comments []string, sep string, firstSentence bool) string {
	if len(comments) == 0 {
		return ""
	}
	parts := make([]string, len(comments))
	for i, c := range comments {
		if firstSentence {
			parts[i] = firstSentenceOf(c)
		} else {
			parts[i] = c
		}
	}
	return strings.Join(parts, sep)
}

// firstSentenceOf returns the first sentence of s — the substring up to the
// first ". " / "! " (a period/bang followed by a space, after a word char),
// a trailing "."/"!", or a newline (Rake::Task#first_sentence). Decimal points
// mid-number do not terminate a sentence.
func firstSentenceOf(s string) string {
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '\n' {
			return string(runes[:i])
		}
		if r == '.' || r == '!' {
			// Trailing "." or "!" at end of string terminates.
			if i == len(runes)-1 {
				return string(runes[:i])
			}
			// "." / "!" followed by a space/tab terminates only when the
			// preceding char is a word character (so "3.14" is not split).
			next := runes[i+1]
			if (next == ' ' || next == '\t') && i > 0 && isWordChar(runes[i-1]) {
				return string(runes[:i])
			}
		}
	}
	return s
}

// isWordChar reports whether r is a Ruby \w character (ASCII letters, digits,
// underscore) — the lookbehind anchor in first_sentence's regex.
func isWordChar(r rune) bool {
	return r == '_' ||
		(r >= '0' && r <= '9') ||
		(r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z')
}
