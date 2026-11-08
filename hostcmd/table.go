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
// or as "--name=value"; a long flag also by a prefix of at least "--x", as
// getopt takes an abbreviation; a short cluster ("-uf", "-ofile") by each of
// its letters. Several matches give the most severe, so a flag is only ever
// read up, never down.
func lookupFlag(flags map[string]Entry, a string) (Entry, bool) {
	name, _, _ := strings.Cut(a, "=")
	if f, ok := flags[name]; ok {
		return f, true
	}
	var best Entry
	found := false
	pick := func(f Entry) {
		if !found || severity[f.Class] > severity[best.Class] ||
			(severity[f.Class] == severity[best.Class] && f.Line < best.Line) {
			best, found = f, true
		}
	}
	switch {
	case strings.HasPrefix(a, "--") && len(name) >= 3:
		for k, f := range flags {
			if strings.HasPrefix(k, "--") && strings.HasPrefix(k, name) {
				pick(f)
			}
		}
	case strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && len(a) > 2:
		for _, r := range a[1:] {
			if f, ok := flags["-"+string(r)]; ok {
				pick(f)
			}
		}
	}
	return best, found
}

// withFlags is base moved by any of flags (see lookupFlag). Every argument is
// read, a lone "--" included: find takes its start points after it.
func withFlags(base Entry, flags map[string]Entry) rule {
	return func(args []string) Entry {
		e := base
		for _, a := range args {
			if f, ok := lookupFlag(flags, a); ok {
				e = over(e, f)
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

// awkFiles are the options of awk that load a program or a library.
var awkFiles = []string{"-f", "-E", "-i", "-l", optFile, "--exec", "--include", "--load"}

// awk is the program as an argument, unless an option names a file.
func awk(args []string) Entry {
	for _, a := range args {
		for _, p := range awkFiles {
			if strings.HasPrefix(a, p) || longPrefix(a, p) {
				return scriptFile
			}
		}
	}
	return script
}

// longPrefix reports whether a is an abbreviation (at least "--xy") of the
// long option opt.
func longPrefix(a, opt string) bool {
	name, _, _ := strings.Cut(a, "=")
	return strings.HasPrefix(opt, "--") && strings.HasPrefix(name, "--") && len(name) >= 4 && strings.HasPrefix(opt, name)
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
// script; without one it prints every variable.
func env(args []string) Entry {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-u" || a == "--unset" || a == "-C" || a == "--chdir":
			i++
		case a == "-S" || strings.HasPrefix(a, "--split-string"):
			return script
		case strings.HasPrefix(a, "-") || strings.Contains(a, "="):
		default:
			return script
		}
	}
	return allEnv
}

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

// uniq with two arguments that are not options writes the second as a file.
func uniq(args []string) Entry {
	n := 0
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			n++
		}
	}
	if n >= 2 {
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
	"find": withFlags(read, map[string]Entry{
		"-delete": remove, "-exec": script, "-execdir": script, "-ok": script, "-okdir": script,
		"-fprint": create, "-fprint0": create, "-fprintf": create, "-fls": create, "-files0-from": content,
	}),
	"ps": ps, "top": top, "journalctl": journalctl, "systemctl": systemctl, "docker": docker,
	"env": env, "awk": awk, "gawk": awk, "mawk": awk, "sed": sed, "hostname": hostname, "uniq": uniq,
	"tar": tar, "zip": zip,
	"go": subcommands(nil, map[string]rule{
		"version": fixed(read), "clean": fixed(goClean),
		"env": withFlags(content, map[string]Entry{"-w": outside, "-u": outside}),
	}, unknown),
	"cargo": subcommands(nil, map[string]rule{"clean": fixed(cargoClean)}, unknown),
	"bash":  inline("-c"), "sh": inline("-c"), "dash": inline("-c"), "zsh": inline("-c"), "ksh": inline("-c"),
	"fish": inline("-c"), "python": inline("-c"), "python3": inline("-c"),
	"node": inlineOnly("-e", "--eval", "-p", "--print"), "perl": inlineOnly("-e", "-E"),
	"du":   withFlags(read, map[string]Entry{"--files0-from": content}),
	"wc":   withFlags(read, map[string]Entry{"--files0-from": content}),
	"date": withFlags(read, map[string]Entry{"-f": content, optFile: content, "-s": root, "--set": root}),
	"file": withFlags(read, map[string]Entry{
		"-f": content, "--files-from": content, "-C": create, "--compile": create, "-m": content, "--magic-file": content,
	}),
	"tree": withFlags(read, map[string]Entry{"-o": create, "-R": create, "--fromfile": content}),
	"sort": withFlags(content, map[string]Entry{"-o": create, "--output": create, "--compress-program": script}),
	"rg":   withFlags(content, map[string]Entry{"--pre": script}),
	"less": withFlags(content, map[string]Entry{"--log-file": create, "-o": create, "-O": create, "--LOG-FILE": create}),
	"xxd":  withFlags(content, map[string]Entry{"-r": create, "-revert": create}),
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
	{script, []string{"xargs"}},
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
