// Package hostcmd is the class table of host commands (R-CMD-2, ADR-0038):
// which commands run by themselves, which need the owner's press and which
// never run, decided from argv in code; the model never chooses a class.
// Each class carries a fixed consequence line (R-CMD-6). It also parses the
// arguments of host.exec and plan.show, which the policy reads too.
package hostcmd

import (
	"path/filepath"
	"strings"

	"github.com/igorrius/ganglion/texts"
)

// Class is what a command does.
type Class string

// The classes, from the least to the most severe.
const (
	Read      Class = "read"
	Content   Class = "content"
	Change    Class = "change"
	Script    Class = "script"
	Forbidden Class = "forbidden"
)

// severity orders the classes: a flag or an argument can only move a
// command up.
var severity = map[Class]int{Read: 0, Content: 1, Change: 2, Script: 3, Forbidden: 4}

// NeedsPress reports whether a command of the class runs only after the
// owner's press: a step of an approved plan, a confirmation, or a grant.
func (c Class) NeedsPress() bool { return c == Content || c == Change || c == Script }

// Entry is a command's class and its fixed consequence line.
type Entry struct {
	Class Class
	Line  texts.Key
}

// over returns f when it is at least as severe as e: a flag says more of
// a command than its program does.
func over(e, f Entry) Entry {
	if severity[f.Class] >= severity[e.Class] {
		return f
	}
	return e
}

// rule classes a program's arguments (argv without the program).
type rule func(args []string) Entry

// Classify returns the class of argv. A program given by a path is change,
// since the file there may be anything, unless its name is forbidden. An
// unknown program is change (default deny). An empty argv is change; the
// tool refuses it before.
func Classify(argv []string) Entry {
	if len(argv) == 0 {
		return unknown
	}
	r, known := table[filepath.Base(argv[0])]
	if strings.ContainsRune(argv[0], '/') {
		if known {
			if e := r(argv[1:]); e.Class == Forbidden {
				return e
			}
		}
		return byPath
	}
	if !known {
		return unknown
	}
	return r(argv[1:])
}
