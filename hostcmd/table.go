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
)

// Subcommands named in several tables.
const (
	subKill    = "kill"
	subInspect = "inspect"
	subPrune   = "prune"
	subStop    = "stop"
	subLogs    = "logs"
)

func fixed(e Entry) rule { return func([]string) Entry { return e } }

func every(e Entry, names ...string) map[string]rule {
	m := make(map[string]rule, len(names))
	for _, n := range names {
		m[n] = fixed(e)
	}
	return m
}

// withFlags is base moved by any of flags, matched whole or as "--name=value",
// up to a lone "--".
func withFlags(base Entry, flags map[string]Entry) rule {
	return func(args []string) Entry {
		e := base
		for _, a := range args {
			if a == "--" {
				break
			}
			name, _, _ := strings.Cut(a, "=")
			if f, ok := flags[name]; ok {
				e = over(e, f)
			}
		}
		return e
	}
}

// subcommands classes by the first argument that is not a flag; other is a
// subcommand not listed, or none. A flag's value read as a subcommand lands
// in other, which is never below change.
func subcommands(subs map[string]rule, other Entry) rule {
	return func(args []string) Entry {
		for i, a := range args {
			if strings.HasPrefix(a, "-") {
				continue
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

// program is awk or sed: the program is an argument, unless -f names a file.
func program(args []string) Entry {
	for _, a := range args {
		if a == "-f" || strings.HasPrefix(a, "--file") {
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

// journalctl prints entries unless asked only for its size; vacuuming
// deletes them.
func journalctl(args []string) Entry {
	e := read
	for _, a := range args {
		name, _, _ := strings.Cut(a, "=")
		switch name {
		case "--disk-usage":
		case "--vacuum-size", "--vacuum-time", "--vacuum-files":
			e = over(e, vacuum)
		default:
			e = over(e, content)
		}
	}
	if len(args) == 0 {
		return content
	}
	return e
}

// systemctl acts on the owner's own services only with --user; without it
// a change needs root.
func systemctl(args []string) Entry {
	if slices.Contains(args, "--user") {
		return subcommands(map[string]rule{
			subStop: fixed(serviceStop), subKill: fixed(serviceStop), "status": fixed(content),
			"list-units": fixed(read), "list-timers": fixed(read), "is-active": fixed(read), "is-failed": fixed(read),
		}, outside)(args)
	}
	return subcommands(map[string]rule{
		"status": fixed(content), "list-units": fixed(read), "is-active": fixed(read), "is-failed": fixed(read),
	}, root)(args)
}

// docker runs only the subcommands listed (ruling 4).
var docker = subcommands(map[string]rule{
	"ps": fixed(read), "images": fixed(read), "version": fixed(read),
	subLogs: fixed(content), subInspect: fixed(content),
	"rm": fixed(containerRm), "rmi": fixed(rmi), subStop: fixed(dockerStop), subKill: fixed(dockerStop),
	"run": fixed(outside), "exec": fixed(outside), "create": fixed(outside), "start": fixed(outside), "restart": fixed(outside),
	"system": subcommands(map[string]rule{
		"df": fixed(read), subPrune: withFlags(systemPrune, map[string]Entry{"--volumes": volumeRm}),
	}, dockerOther),
	"image": subcommands(map[string]rule{
		"ls": fixed(read), "history": fixed(read), subInspect: fixed(content), subPrune: fixed(imagePrune), "rm": fixed(rmi),
	}, dockerOther),
	"builder": subcommands(map[string]rule{subPrune: fixed(buildPrune), "du": fixed(read)}, dockerOther),
	"buildx":  subcommands(map[string]rule{subPrune: fixed(buildPrune), "du": fixed(read)}, dockerOther),
	"container": subcommands(map[string]rule{
		"ls": fixed(read), subInspect: fixed(content), subLogs: fixed(content), "rm": fixed(containerRm),
		subPrune: fixed(containerRm), subStop: fixed(dockerStop),
	}, dockerOther),
	"volume": subcommands(map[string]rule{
		"ls": fixed(read), subInspect: fixed(content), "rm": fixed(volumeRm), subPrune: fixed(volumeRm),
	}, dockerOther),
	"compose": subcommands(map[string]rule{
		"ps": fixed(read), subLogs: fixed(content), subStop: fixed(dockerStop), "down": fixed(containerRm),
	}, outside),
}, dockerOther)

// table is every program the node knows, by name.
var table = func() map[string]rule {
	t := map[string]rule{
		"find": withFlags(read, map[string]Entry{
			"-delete": remove, "-exec": script, "-execdir": script, "-ok": script, "-okdir": script,
			"-fprint": create, "-fprint0": create, "-fprintf": create, "-fls": create,
		}),
		"ps": ps, "top": top, "journalctl": journalctl, "systemctl": systemctl, "docker": docker,
		"env": env, "awk": program, "gawk": program, "mawk": program, "sed": program,
		"go":    subcommands(map[string]rule{"version": fixed(read), "env": fixed(content), "clean": fixed(goClean)}, unknown),
		"cargo": subcommands(map[string]rule{"clean": fixed(cargoClean)}, unknown),
		"bash":  inline("-c"), "sh": inline("-c"), "dash": inline("-c"), "zsh": inline("-c"), "ksh": inline("-c"),
		"fish": inline("-c"), "python": inline("-c"), "python3": inline("-c"),
		"node": inline("-e", "--eval", "-p", "--print"), "perl": inline("-e", "-E"),
	}
	groups := []struct {
		e     Entry
		names []string
	}{
		{read, []string{"df", "du", "ls", "tree", "free", "uptime", "uname", "whoami", "id", "nproc", "lsblk", "findmnt",
			"stat", "wc", "date", "hostname", "which", "file", "realpath", "pwd", "sleep", "true", "false", "echo"}},
		{content, []string{"cat", "head", "tail", "less", "more", "grep", "egrep", "fgrep", "rg", "zcat", "bzcat", "xzcat",
			"zgrep", "strings", "od", "xxd", "hexdump", "base64", "diff", "cmp", "sort", "uniq", "cut", "tr", "jq",
			"column", "nl", "tac", "rev"}},
		{remove, []string{"rm", "rmdir", "shred", "unlink"}},
		{move, []string{"mv", "cp", "install", "rsync"}},
		{create, []string{"mkdir", "touch", "ln", "mkfifo"}},
		{perms, []string{"chmod", "chown", "chgrp"}},
		{kill, []string{"kill", "pkill", "killall"}},
		{archive, []string{"tar", "gzip", "gunzip", "zip", "unzip", "xz", "unxz", "zstd", "bzip2"}},
		{truncate, []string{"truncate"}},
		{network, []string{"curl", "wget"}},
		{script, []string{"xargs"}},
		{root, []string{"sudo", "su", "doas", "pkexec", "run0", "sg", "newgrp", "chroot"}},
		{allEnv, []string{"printenv"}},
		{outside, []string{"systemd-run", "crontab", "at", "batch", "nohup", "setsid", "screen", "tmux", "ssh", "scp",
			"sftp", "nc", "ncat", "socat", "nsenter", "unshare", "podman"}},
	}
	for _, g := range groups {
		maps.Copy(t, every(g.e, g.names...))
	}
	return t
}()
