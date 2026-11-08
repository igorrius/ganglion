package hostcmd

import (
	"maps"
	"slices"
	"strings"

	"github.com/igorrius/ganglion/texts"
)

// The entries. Each is one consequence line; several programs share one.
var (
	read        = Entry{Read, texts.CmdRead}
	content     = Entry{Content, texts.CmdContent}
	unknown     = Entry{Change, texts.CmdUnknown}
	byPath      = Entry{Change, texts.CmdPath}
	script      = Entry{Script, texts.CmdScript}
	root        = Entry{Forbidden, texts.CmdRoot}
	outside     = Entry{Forbidden, texts.CmdOutside}
	allEnv      = Entry{Forbidden, texts.CmdAllEnv}
	scriptFile  = Entry{Forbidden, texts.CmdScriptFile}
	dockerOther = Entry{Forbidden, texts.CmdDockerOther}
	remove      = Entry{Change, texts.CmdRemove}
	move        = Entry{Change, texts.CmdMove}
	create      = Entry{Change, texts.CmdCreate}
	perms       = Entry{Change, texts.CmdPerms}
	kill        = Entry{Change, texts.CmdKill}
	archive     = Entry{Change, texts.CmdArchive}
	truncate    = Entry{Change, texts.CmdTruncate}
	network     = Entry{Change, texts.CmdNetwork}
	goClean     = Entry{Change, texts.CmdGoClean}
	cargoClean  = Entry{Change, texts.CmdCargoClean}
	imagePrune  = Entry{Change, texts.CmdDockerImagePrune}
	rmi         = Entry{Change, texts.CmdDockerRmi}
	buildPrune  = Entry{Change, texts.CmdDockerBuilderPrune}
	containerRm = Entry{Change, texts.CmdDockerRm}
	dockerStop  = Entry{Change, texts.CmdDockerStop}
	volumeRm    = Entry{Change, texts.CmdDockerVolumeRm}
	systemPrune = Entry{Change, texts.CmdDockerSystemPrune}
	serviceStop = Entry{Change, texts.CmdSystemctlStop}
	vacuum      = Entry{Change, texts.CmdJournalVacuum}
	wrapper     = Entry{Forbidden, texts.CmdWrapper}
)

// Subcommands named in several tables.
const (
	subKill    = "kill"
	subInspect = "inspect"
	subPrune   = "prune"
	subStop    = "stop"
	subLogs    = "logs"
	optFile    = "--file"
	optVolumes = "--volumes"
)

func fixed(e Entry) rule { return func([]string) Entry { return e } }

func every(e Entry, names ...string) map[string]rule {
	m := make(map[string]rule, len(names))
	for _, n := range names {
		m[n] = fixed(e)
	}
	return m
}

// lookupFlag finds the entry a flag moves a command to. A flag matches whole
// or as "--name=value"; a long flag also by any prefix of at least "--x", as
// getopt takes an abbreviation; a short cluster ("-uf", "-ofile") by each of
// its letters. Several matches give the most severe, so a flag is only ever
// read up, never down.
func lookupFlag(flags map[string]Entry, a string) (Entry, bool) {
	e, found, _ := matchFlag(flags, nil, nil, a)
	return e, found
}

// matchFlag is lookupFlag for options that take a value. takes always do
// (the next argument when it is not attached); attached may have a value
// attached ("-Iseconds"). In a cluster the letters stop at the first option
// with a value, since the rest of the token is that value. skip says the
// next argument is the value of an option, not a flag.
func matchFlag(flags map[string]Entry, takes, attached []string, a string) (Entry, bool, bool) {
	m := &matcher{flags: flags, takes: takes, attached: attached}
	name, _, hasValue := strings.Cut(a, "=")
	if f, ok := flags[name]; ok || slices.Contains(takes, name) || slices.Contains(attached, name) {
		return f, ok, !hasValue && slices.Contains(takes, name)
	}
	switch {
	case strings.HasPrefix(a, "--") && len(name) >= 3:
		m.long(name, hasValue)
	case strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && len(a) > 2:
		m.cluster(a, hasValue)
	}
	return m.best, m.found, m.skip
}

// matcher collects the entries a token matches, keeping the most severe.
type matcher struct {
	flags       map[string]Entry
	takes       []string
	attached    []string
	best        Entry
	found, skip bool
}

func (m *matcher) pick(k string) {
	f, ok := m.flags[k]
	if !ok {
		return
	}
	e := m.best
	if !m.found || severity[f.Class] > severity[e.Class] ||
		severity[f.Class] == severity[e.Class] && (loss(f) > loss(e) || loss(f) == loss(e) && f.Line < e.Line) {
		m.best, m.found = f, true
	}
}

// long matches an abbreviation of a long option.
func (m *matcher) long(name string, hasValue bool) {
	for _, k := range slices.Concat(slices.Collect(maps.Keys(m.flags)), m.takes) {
		if strings.HasPrefix(k, "--") && strings.HasPrefix(k, name) {
			m.pick(k)
			m.skip = m.skip || !hasValue && slices.Contains(m.takes, k)
		}
	}
}

// cluster matches the letters of a short cluster up to the first option that
// takes a value.
func (m *matcher) cluster(a string, hasValue bool) {
	for i, r := range a[1:] {
		k := "-" + string(r)
		m.pick(k)
		if slices.Contains(m.takes, k) {
			m.skip = i+2 == len(a) && !hasValue
			return
		}
		if slices.Contains(m.attached, k) {
			return
		}
	}
}

// withFlags is base moved by any of flags (see lookupFlag). Every argument is
// read, a lone "--" included: find takes its start points after it.
func withFlags(base Entry, flags map[string]Entry) rule {
	return withValues(base, flags, nil, nil)
}

// withValues is withFlags for a program whose options take values, so that a
// value that looks like a flag ("date -d -2days") is not read as one, and the
// letters of an attached value ("date -Iseconds") are not read as flags.
func withValues(base Entry, flags map[string]Entry, takes, attached []string) rule {
	return func(args []string) Entry {
		e := base
		for i := 0; i < len(args); i++ {
			f, ok, skip := matchFlag(flags, takes, attached, args[i])
			if ok {
				e = over(e, f)
			}
			if skip {
				i++
			}
		}
		return e
	}
}

// subcommands classes by the first argument that is not a flag; other is a
// subcommand not listed, or none. A flag before the subcommand that is not in
// allowed gives other: it may take the next argument as its value or point
// the program elsewhere (docker --host).
func subcommands(allowed []string, subs map[string]rule, other Entry) rule {
	return func(args []string) Entry {
		for i, a := range args {
			if strings.HasPrefix(a, "-") {
				if slices.Contains(allowed, a) {
					continue
				}
				return other
			}
			if r, ok := subs[a]; ok {
				return r(args[i+1:])
			}
			return other
		}
		return other
	}
}

// dashed reads "--flag" as "-flag" for a program whose flag package takes both.
func dashed(r rule) rule {
	return func(args []string) Entry {
		out := make([]string, len(args))
		for i, a := range args {
			if strings.HasPrefix(a, "--") && len(a) > 2 {
				a = a[1:]
			}
			out[i] = a
		}
		return r(out)
	}
}

// inline is an interpreter that runs a script written out after one of
// flags; any other use reads a file or its input, which a question cannot
// show (ruling 1 of the M3d plan).
func inline(flags ...string) rule {
	return func(args []string) Entry {
		if len(args) >= 2 && slices.Contains(flags, args[0]) {
			return script
		}
		return scriptFile
	}
}

// inlineOnly is inline for an interpreter that keeps parsing options after
// the code (node -r, perl -M), which load files: any later option makes it a
// script from a file.
func inlineOnly(flags ...string) rule {
	run := inline(flags...)
	return func(args []string) Entry {
		if e := run(args); e.Class != Script {
			return e
		}
		for _, a := range args[2:] {
			if strings.HasPrefix(a, "-") {
				return scriptFile
			}
		}
		return script
	}
}

// awkFiles are the long options of awk that load a program, a library or
// debugger commands; awkFileLetters are the short ones. gawk's short options
// take values in several ways (-v, -F, -e, and the optional attached values
// of -d, -D, -L, -o, -p), so the grammar is not modelled: any single-dash
// argument with one of these letters, anywhere in argv, names a file (fail
// closed, ruling 1 of the M3d plan). -W (gawk, mawk) takes any of them by
// name, so W is one of the letters.
var (
	awkFiles       = []string{"--file", "--exec", "--include", "--load", "--debug"}
	awkFileLetters = "fEilWD"
)

// awk is the program as an argument, unless an option may name a file or the
// program may load one. Any "@" counts as a directive: gawk accepts space or
// a backslash-newline between "@" and include or load, and inline awk rarely
// needs "@" otherwise.
func awk(args []string) Entry {
	for _, a := range args {
		switch {
		case strings.Contains(a, "@"):
			return scriptFile
		case strings.HasPrefix(a, "--"):
			// mawk compares long names ignoring case.
			if lower := strings.ToLower(a); slices.ContainsFunc(awkFiles, func(p string) bool { return longPrefix(lower, p) }) {
				return scriptFile
			}
		case strings.HasPrefix(a, "-") && strings.ContainsAny(a[1:], awkFileLetters):
			return scriptFile
		}
	}
	return script
}

// longPrefix reports whether a is an abbreviation (at least "--x") of the
// long option opt.
func longPrefix(a, opt string) bool {
	name, _, _ := strings.Cut(a, "=")
	return strings.HasPrefix(opt, "--") && strings.HasPrefix(name, "--") && len(name) >= 3 && strings.HasPrefix(opt, name)
}

// sed is the program as an argument, unless --file or a short cluster with f
// names a file.
func sed(args []string) Entry {
	for _, a := range args {
		if longPrefix(a, optFile) {
			return scriptFile
		}
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "f") {
			return scriptFile
		}
	}
	return script
}

// env with a program runs it with an environment the agent chose: a
// script, or the class of the program when that is more severe; without one
// it prints every variable. Which argument is the program depends on env's
// options, so every suffix that starts at an argument that is not an option
// or an assignment is classed (over-counting only moves it up). -S splits a
// string into a command with env's own quoting and ${VAR} expansion, which
// the table does not read: a wrapper.
func env(args []string, inner rule) Entry {
	e := envBase(args)
	if e != script {
		return e
	}
	for i, a := range args {
		if !strings.HasPrefix(a, "-") && !strings.Contains(a, "=") {
			e = above(e, inner(args[i:]))
		}
	}
	return e
}

// envBase is env's own class, from its options before the program.
func envBase(args []string) Entry {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-u" || a == "--unset" || a == "-C" || a == "--chdir":
			i++
		case longPrefix(a, "--split-string") || !strings.HasPrefix(a, "--") && strings.HasPrefix(a, "-") && strings.Contains(a, "S"):
			return wrapper
		case strings.HasPrefix(a, "-") || strings.Contains(a, "="):
		default:
			return script
		}
	}
	return allEnv
}

// xargs runs a command built from its arguments and its input: a script,
// or the class of the command when that is more severe. Its options take
// values, so every suffix that starts at an argument that is not an option
// is classed.
func xargs(args []string, inner rule) Entry {
	e := script
	for i, a := range args {
		if !strings.HasPrefix(a, "-") {
			e = above(e, inner(args[i:]))
		}
	}
	return e
}

// findExec are the actions of find that run a command.
var findExec = []string{"-exec", "-execdir", "-ok", "-okdir"}

var findFlags = withFlags(read, map[string]Entry{
	"-delete": remove, "-exec": script, "-execdir": script, "-ok": script, "-okdir": script,
	"-fprint": create, "-fprint0": create, "-fprintf": create, "-fls": create, "-files0-from": content,
})

// find is read unless an action moves it; the command of each -exec,
// -execdir, -ok and -okdir (up to ";", or "+" right after "{}", as find
// ends it) moves it further when it is more severe than a script.
func find(args []string, inner rule) Entry {
	e := findFlags(args)
	for i, a := range args {
		if !slices.Contains(findExec, a) {
			continue
		}
		end := i + 1
		for end < len(args) && args[end] != ";" && (args[end] != "+" || args[end-1] != "{}") {
			end++
		}
		e = above(e, inner(args[i+1:end]))
	}
	return e
}

// wrappers are the programs that run another command from their arguments.
var wrappers = map[string]nested{"env": env, "xargs": xargs, "find": find}

// psFields are the columns that show no command line or environment.
var psFields = []string{"pid", "ppid", "pgid", "user", "uid", "comm", "ucomm", "rss", "vsz", "pcpu", "%cpu", "pmem",
	"%mem", "etime", "etimes", "stat", "state", "start", "lstart", "time", "nlwp", "ni", "pri", "tty"}

// ps is read only with flags that keep to names (ruling 3); every other
// form, BSD letters included, may print command lines or environments.
func ps(args []string) Entry {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "-e", "-A", "--no-headers":
		case "-p", "--pid", "-u", "-U", "--user", "--sort":
			i++
		case "-o", "--format":
			if i+1 >= len(args) || !safeFields(args[i+1]) {
				return content
			}
			i++
		default:
			return content
		}
	}
	return read
}

func safeFields(list string) bool {
	for f := range strings.SplitSeq(list, ",") {
		name, _, _ := strings.Cut(f, "=") // a header after "=" is only a title
		if !slices.Contains(psFields, name) {
			return false
		}
	}
	return true
}

// top is read only in batch mode with a count; interactive or with full
// command lines it is content.
func top(args []string) Entry {
	batch := false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-b":
			batch = true
		case a == "-n" && i+1 < len(args):
			i++
		case a == "-bn1" || a == "-n1":
			batch = batch || a == "-bn1"
		default:
			return content
		}
	}
	if !batch {
		return content
	}
	return read
}

// journalFlags: --disk-usage only reads the size; the rest changes the
// journal or needs root. Any other argument shows entries.
var journalFlags = map[string]Entry{
	"--disk-usage": read, "--vacuum-size": vacuum, "--vacuum-time": vacuum, "--vacuum-files": vacuum,
	"--rotate": root, "--flush": root, "--sync": root, "--setup-keys": root,
	"--relinquish-var": root, "--smart-relinquish-var": root,
}

func journalctl(args []string) Entry {
	if len(args) == 0 {
		return content
	}
	e := read
	for _, a := range args {
		if f, ok := lookupFlag(journalFlags, a); ok {
			e = over(e, f)
		} else {
			e = over(e, content)
		}
	}
	return e
}

// systemctlRemote are the options that reach another machine or the system's
// own manager; anywhere in the command they make it a root command.
var systemctlRemote = withFlags(read, map[string]Entry{
	"-H": root, "--host": root, "-M": root, "--machine": root, "--system": root, "--global": root,
})

// systemctlFlags are the options allowed before the subcommand.
var systemctlFlags = []string{"--user", "--no-pager"}

// systemctl acts on the owner's own services only with --user; without it
// a change needs root.
func systemctl(args []string) Entry {
	if systemctlRemote(args).Class == Forbidden {
		return root
	}
	if slices.Contains(args, "--user") {
		return subcommands(systemctlFlags, map[string]rule{
			subStop: fixed(serviceStop), subKill: fixed(serviceStop), "status": fixed(content),
			"list-units": fixed(read), "list-timers": fixed(read), "is-active": fixed(read), "is-failed": fixed(read),
		}, outside)(args)
	}
	return subcommands(systemctlFlags, map[string]rule{
		"status": fixed(content), "list-units": fixed(read), "is-active": fixed(read), "is-failed": fixed(read),
	}, root)(args)
}

// hostname reads the name; a name given as an argument or read from a file
// sets it.
func hostname(args []string) Entry {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			return root
		}
	}
	return withFlags(read, map[string]Entry{"-F": root, optFile: root, "-b": root, "--boot": root})(args)
}

// positionals counts the arguments that are not options: "-" is one,
// everything after "--" is, and the value of a valued option is not. With
// stopAtFirst every argument after the first positional is one too (xxd).
func positionals(args, valued []string, cluster, stopAtFirst bool) int {
	n := 0
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case stopAtFirst && n > 0:
			n++
		case a == "--":
			return n + len(args) - i - 1
		case a == "-" || !strings.HasPrefix(a, "-"):
			n++
		case slices.Contains(valued, a):
			i++
		case cluster && !strings.HasPrefix(a, "--") && len(a) > 2 && slices.Contains(valued, "-"+a[len(a)-1:]):
			i++
		}
	}
	return n
}

var (
	dateFlags   = map[string]Entry{"-f": content, optFile: content, "-s": root, "--set": root}
	dateTakes   = []string{"-d", "--date", "-r", "--reference", "-f", optFile, "-s", "--set"}
	dateAttach  = []string{"-I"}
	dateOptions = withValues(read, dateFlags, dateTakes, dateAttach)
)

// date reads the time; an operand that is not +FORMAT sets the clock.
func date(args []string) Entry {
	e := dateOptions(args)
	flags := true
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case flags && a == "--":
			flags = false
		case flags && strings.HasPrefix(a, "-") && a != "-":
			if _, _, skip := matchFlag(dateFlags, dateTakes, dateAttach, a); skip {
				i++
			}
		case !strings.HasPrefix(a, "+"):
			return over(e, root)
		}
	}
	return e
}

// uniq with two arguments that are not options writes the second as a file.
func uniq(args []string) Entry {
	valued := []string{"-f", "-s", "-w", "--skip-fields", "--skip-chars", "--check-chars"}
	if positionals(args, valued, true, false) >= 2 {
		return create
	}
	return content
}

// xxd never combines short options; -r writes a file back, and so does a
// second file argument.
func xxd(args []string) Entry {
	valued := []string{"-g", "-groupsize", "-c", "-cols", "-l", "-len", "-s", "-seek", "-o", "-offset", "-n", "-name", "-R"}
	if slices.Contains(args, "-r") || slices.Contains(args, "-revert") || positionals(args, valued, false, true) >= 2 {
		return create
	}
	return content
}

// tarScript are the options that run a program. The first argument may be
// the old style, letters with no dash.
var tarScript = withFlags(archive, map[string]Entry{
	"-I": script, "--use-compress-program": script, "--checkpoint-action": script, "--to-command": script,
	"--info-script": script, "-F": script, "--new-volume-script": script, "--rsh-command": script,
	"--rmt-command": script,
})

func tar(args []string) Entry {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		args = append([]string{"-" + args[0]}, args[1:]...)
	}
	return tarScript(args)
}

// zipScript: -TT names the program that tests the archive.
var zipScript = withFlags(archive, map[string]Entry{"--unzip-command": script})

func zip(args []string) Entry {
	for _, a := range args {
		if strings.HasPrefix(a, "-TT") {
			return script
		}
	}
	return zipScript(args)
}

// psListing are the options of docker's listings that print command lines.
var psListing = withFlags(read, map[string]Entry{"--no-trunc": content, "--format": content})

// dockerRm: -v also deletes the container's anonymous volumes.
var dockerRm = withFlags(containerRm, map[string]Entry{"-v": volumeRm, optVolumes: volumeRm})

// docker runs only the subcommands listed (ruling 4); no option before the
// subcommand is taken (--host reaches another machine).
var docker = subcommands(nil, map[string]rule{
	"ps": psListing, "images": fixed(read), "version": fixed(read),
	subLogs: fixed(content), subInspect: fixed(content),
	"rm": dockerRm, "rmi": fixed(rmi), subStop: fixed(dockerStop), subKill: fixed(dockerStop),
	"run": fixed(outside), "exec": fixed(outside), "create": fixed(outside), "start": fixed(outside), "restart": fixed(outside),
	"system": subcommands(nil, map[string]rule{
		"df": fixed(read), subPrune: withFlags(systemPrune, map[string]Entry{optVolumes: volumeRm}),
	}, dockerOther),
	"image": subcommands(nil, map[string]rule{
		"ls": fixed(read), "history": fixed(read), subInspect: fixed(content), subPrune: fixed(imagePrune), "rm": fixed(rmi),
	}, dockerOther),
	"builder": subcommands(nil, map[string]rule{subPrune: fixed(buildPrune), "du": fixed(read)}, dockerOther),
	"buildx":  subcommands(nil, map[string]rule{subPrune: fixed(buildPrune), "du": fixed(read)}, dockerOther),
	"container": subcommands(nil, map[string]rule{
		"ls": psListing, subInspect: fixed(content), subLogs: fixed(content), "rm": dockerRm,
		subPrune: fixed(containerRm), subStop: fixed(dockerStop),
	}, dockerOther),
	"volume": subcommands(nil, map[string]rule{
		"ls": fixed(read), subInspect: fixed(content), "rm": fixed(volumeRm), subPrune: fixed(volumeRm),
	}, dockerOther),
	"compose": subcommands(nil, map[string]rule{
		"ps": psListing, subLogs: fixed(content), subStop: fixed(dockerStop),
		"down": withFlags(containerRm, map[string]Entry{"-v": volumeRm, optVolumes: volumeRm, "--rmi": rmi}),
	}, outside),
}, dockerOther)

// programs are the programs with a rule of their own.
var programs = map[string]rule{
	"ps": ps, "top": top, "journalctl": journalctl, "systemctl": systemctl, "docker": docker,
	"awk": awk, "gawk": awk, "mawk": awk, "sed": sed, "hostname": hostname, "uniq": uniq,
	"tar": tar, "zip": zip,
	"go": dashed(subcommands(nil, map[string]rule{
		"version": fixed(read), "clean": fixed(goClean),
		"env": withFlags(content, map[string]Entry{"-w": outside, "-u": outside}),
	}, unknown)),
	"cargo": subcommands(nil, map[string]rule{"clean": fixed(cargoClean)}, unknown),
	"bash":  inline("-c"), "sh": inline("-c"), "dash": inline("-c"), "zsh": inline("-c"), "ksh": inline("-c"),
	"fish": inline("-c"), "python": inline("-c"), "python3": inline("-c"),
	"node": inlineOnly("-e", "--eval", "-p", "--print"), "perl": inlineOnly("-e", "-E"),
	"du":   withFlags(read, map[string]Entry{"--files0-from": content}),
	"wc":   withFlags(read, map[string]Entry{"--files0-from": content}),
	"date": date,
	"file": withFlags(read, map[string]Entry{
		"-f": content, "--files-from": content, "-C": create, "--compile": create, "-m": content, "--magic-file": content,
	}),
	"tree": withFlags(read, map[string]Entry{"-o": create, "-R": create, "--fromfile": content}),
	"sort": withFlags(content, map[string]Entry{"-o": create, "--output": create, "--compress-program": script}),
	"rg":   withFlags(content, map[string]Entry{"--pre": script}),
	"less": withFlags(content, map[string]Entry{"--log-file": create, "-o": create, "-O": create, "--LOG-FILE": create}),
	"xxd":  xxd,
}

// groups are the programs that share one entry whatever their arguments.
var groups = []struct {
	e     Entry
	names []string
}{
	{read, []string{"df", "ls", "free", "uptime", "uname", "whoami", "id", "nproc", "lsblk", "findmnt",
		"stat", "which", "realpath", "pwd", "sleep", "true", "false", "echo"}},
	{content, []string{"cat", "head", "tail", "more", "grep", "egrep", "fgrep", "zcat", "bzcat", "xzcat",
		"zgrep", "strings", "od", "hexdump", "base64", "diff", "cmp", "cut", "tr", "jq",
		"column", "nl", "tac", "rev"}},
	{remove, []string{"rm", "rmdir", "shred", "unlink"}},
	{move, []string{"mv", "cp", "install"}},
	{create, []string{"mkdir", "touch", "ln", "mkfifo"}},
	{perms, []string{"chmod", "chown", "chgrp"}},
	{kill, []string{"kill", "pkill", "killall"}},
	{archive, []string{"gzip", "gunzip", "unzip", "xz", "unxz", "zstd", "bzip2"}},
	{truncate, []string{"truncate"}},
	{network, []string{"curl", "wget"}},
	{root, []string{"sudo", "su", "doas", "pkexec", "run0", "sg", "newgrp", "chroot"}},
	{allEnv, []string{"printenv"}},
	{wrapper, []string{"timeout", "nice", "ionice", "stdbuf", "time", "flock", "chrt", "taskset", "strace", "ltrace",
		"watch", "script", "unbuffer", "expect", "firejail", "bwrap", "systemd-inhibit", "dbus-run-session", "busybox"}},
	{outside, []string{"systemd-run", "crontab", "at", "batch", "nohup", "setsid", "screen", "tmux", "ssh", "scp",
		"sftp", "nc", "ncat", "socat", "nsenter", "unshare", "podman", "rsync"}},
}

// table is every program the node knows, by name.
var table = func() map[string]rule {
	t := maps.Clone(programs)
	for _, g := range groups {
		maps.Copy(t, every(g.e, g.names...))
	}
	return t
}()
