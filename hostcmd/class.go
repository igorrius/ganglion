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

// lossy are the lines of commands that delete or cut what cannot be got back.
var lossy = map[texts.Key]bool{
	texts.CmdRemove: true, texts.CmdDockerVolumeRm: true, texts.CmdDockerRm: true, texts.CmdDockerSystemPrune: true,
	texts.CmdJournalVacuum: true, texts.CmdTruncate: true, texts.CmdDockerRmi: true, texts.CmdDockerImagePrune: true,
	texts.CmdDockerBuilderPrune: true, texts.CmdGoClean: true, texts.CmdCargoClean: true,
}

// loss ranks how much a line loses: volumes first, then any other deletion.
func loss(e Entry) int {
	switch {
	case e.Line == texts.CmdDockerVolumeRm:
		return 2
	case lossy[e.Line]:
		return 1
	default:
		return 0
	}
}

// over returns f when it is more severe than e, or as severe and not saying
// less of what is lost: a flag says more of a command than its program does,
// and a later flag never hides a deletion behind a milder line. Otherwise
// the later one wins.
func over(e, f Entry) Entry {
	if severity[f.Class] > severity[e.Class] || severity[f.Class] == severity[e.Class] && loss(f) >= loss(e) {
		return f
	}
	return e
}

// rule classes a program's arguments (argv without the program).
type rule func(args []string) Entry

// nested classes the arguments of a program that runs another command from
// them (env, xargs, find -exec); inner classes that command.
type nested func(args []string, inner rule) Entry

// Classify returns the class of argv. A program given by a path is change,
// since the file there may be anything, unless its name is forbidden. An
// unknown program is change (default deny). An empty argv is change; the
// tool refuses it before.
func Classify(argv []string) Entry {
	c := classifier{seen: map[span]Entry{}}
	return c.classify(argv)
}

// span is a stretch of one argv, by its first element and its length.
type span struct {
	first *string
	n     int
}

// classifier remembers the class of each stretch of argv it has classed, so
// that nested wrappers ("env env env ...") class each stretch once and the
// work stays polynomial in the length of argv. A wrapper classes only
// shorter stretches, so the recursion ends.
type classifier struct{ seen map[span]Entry }

func (c *classifier) classify(argv []string) Entry {
	if len(argv) == 0 {
		return unknown
	}
	k := span{&argv[0], len(argv)}
	if e, ok := c.seen[k]; ok {
		return e
	}
	e := c.class(argv)
	c.seen[k] = e
	return e
}

func (c *classifier) class(argv []string) Entry {
	r, known := c.rule(filepath.Base(argv[0]))
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

// rule finds the rule of a program; a wrapper's inner commands are classed
// by c.
func (c *classifier) rule(name string) (rule, bool) {
	if w, ok := wrappers[name]; ok {
		return func(args []string) Entry { return w(args, c.classify) }, true
	}
	r, ok := table[name]
	return r, ok
}

// above returns f when it is more severe than e, otherwise e: the command a
// wrapper runs can only move the wrapper up.
func above(e, f Entry) Entry {
	if severity[f.Class] > severity[e.Class] {
		return f
	}
	return e
}
