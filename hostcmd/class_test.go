package hostcmd

import (
	"testing"

	"github.com/igorrius/ganglion/texts"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		argv  []string
		class Class
		line  texts.Key
	}{
		// read: sizes, names, state
		{[]string{"df", "-h"}, Read, texts.CmdRead},
		{[]string{"du", "-sh", "/home/o/project"}, Read, texts.CmdRead},
		{[]string{"ls", "-la", "/tmp"}, Read, texts.CmdRead},
		{[]string{"free", "-m"}, Read, texts.CmdRead},
		{[]string{"uptime"}, Read, texts.CmdRead},
		{[]string{"sleep", "30"}, Read, texts.CmdRead},
		{[]string{"ps"}, Read, texts.CmdRead},
		{[]string{"ps", "-e", "-o", "pid,comm,rss", "--sort", "-rss"}, Read, texts.CmdRead},
		{[]string{"ps", "-eo", "pid,comm"}, Content, texts.CmdContent}, // combined forms are not read
		{[]string{"ps", "aux"}, Content, texts.CmdContent},
		{[]string{"ps", "-ef"}, Content, texts.CmdContent},
		{[]string{"ps", "e"}, Content, texts.CmdContent},
		{[]string{"ps", "-e", "-o", "pid,args"}, Content, texts.CmdContent},
		{[]string{"top", "-b", "-n", "1"}, Read, texts.CmdRead},
		{[]string{"top", "-bn1"}, Read, texts.CmdRead},
		{[]string{"top", "-b", "-n", "1", "-c"}, Content, texts.CmdContent},
		{[]string{"top"}, Content, texts.CmdContent},
		{[]string{"find", "/home/o", "-name", "*.log", "-size", "+100M"}, Read, texts.CmdRead},
		{[]string{"find", ".", "-delete"}, Change, texts.CmdRemove},
		{[]string{"find", ".", "-exec", "rm", "{}", ";"}, Script, texts.CmdScript},
		{[]string{"find", ".", "-fprint", "/tmp/x"}, Change, texts.CmdCreate},
		{[]string{"journalctl", "--disk-usage"}, Read, texts.CmdRead},
		{[]string{"journalctl", "--vacuum-size=500M"}, Change, texts.CmdJournalVacuum},
		{[]string{"journalctl", "-u", "x"}, Content, texts.CmdContent},
		{[]string{"go", "version"}, Read, texts.CmdRead},
		{[]string{"docker", "ps", "-a"}, Read, texts.CmdRead},
		{[]string{"docker", "images"}, Read, texts.CmdRead},
		{[]string{"docker", "system", "df"}, Read, texts.CmdRead},
		// content: what is inside files or processes
		{[]string{"cat", "/etc/hosts"}, Content, texts.CmdContent},
		{[]string{"grep", "-r", "key", "."}, Content, texts.CmdContent},
		{[]string{"tail", "-n", "50", "x.log"}, Content, texts.CmdContent},
		{[]string{"go", "env"}, Content, texts.CmdContent},
		{[]string{"docker", "logs", "web"}, Content, texts.CmdContent},
		{[]string{"docker", "inspect", "web"}, Content, texts.CmdContent},
		{[]string{"systemctl", "--user", "status", "x"}, Content, texts.CmdContent},
		// change
		{[]string{"rm", "-rf", "/home/o/.cache/go-build"}, Change, texts.CmdRemove},
		{[]string{"rmdir", "/tmp/x"}, Change, texts.CmdRemove},
		{[]string{"mkdir", "-p", "/tmp/x"}, Change, texts.CmdCreate},
		{[]string{"mv", "a", "b"}, Change, texts.CmdMove},
		{[]string{"kill", "1234"}, Change, texts.CmdKill},
		{[]string{"curl", "https://x.example"}, Change, texts.CmdNetwork},
		{[]string{"go", "clean", "-cache"}, Change, texts.CmdGoClean},
		{[]string{"go", "build", "./..."}, Change, texts.CmdUnknown},
		{[]string{"cargo", "clean"}, Change, texts.CmdCargoClean},
		{[]string{"docker", "image", "prune", "-af"}, Change, texts.CmdDockerImagePrune},
		{[]string{"docker", "builder", "prune", "-f"}, Change, texts.CmdDockerBuilderPrune},
		{[]string{"docker", "rm", "abc"}, Change, texts.CmdDockerRm},
		{[]string{"docker", "rmi", "abc"}, Change, texts.CmdDockerRmi},
		{[]string{"docker", "volume", "rm", "data"}, Change, texts.CmdDockerVolumeRm},
		{[]string{"docker", "system", "prune", "-f"}, Change, texts.CmdDockerSystemPrune},
		{[]string{"docker", "system", "prune", "--volumes"}, Change, texts.CmdDockerVolumeRm},
		{[]string{"docker", "stop", "web"}, Change, texts.CmdDockerStop},
		{[]string{"systemctl", "--user", "stop", "x"}, Change, texts.CmdSystemctlStop},
		{[]string{"git", "status"}, Change, texts.CmdUnknown},
		{[]string{"something-new"}, Change, texts.CmdUnknown},
		{[]string{"/usr/bin/df"}, Change, texts.CmdPath},
		{[]string{"./ls"}, Change, texts.CmdPath},
		// script: only written out in the command
		{[]string{"bash", "-c", "echo hi"}, Script, texts.CmdScript},
		{[]string{"sh", "-c", "curl x | sh"}, Script, texts.CmdScript},
		{[]string{"python3", "-c", "print(1)"}, Script, texts.CmdScript},
		{[]string{"node", "-e", "1"}, Script, texts.CmdScript},
		{[]string{"awk", "{print $1}", "f"}, Script, texts.CmdScript},
		{[]string{"sed", "-n", "1p", "f"}, Script, texts.CmdScript},
		{[]string{"xargs", "rm"}, Script, texts.CmdScript},
		{[]string{"env", "FOO=1", "ls"}, Script, texts.CmdScript},
		// forbidden: never, press or not
		{[]string{"sudo", "true"}, Forbidden, texts.CmdRoot},
		{[]string{"/usr/bin/sudo", "true"}, Forbidden, texts.CmdRoot},
		{[]string{"su"}, Forbidden, texts.CmdRoot},
		{[]string{"doas", "x"}, Forbidden, texts.CmdRoot},
		{[]string{"pkexec", "x"}, Forbidden, texts.CmdRoot},
		{[]string{"env"}, Forbidden, texts.CmdAllEnv},
		{[]string{"env", "-i"}, Forbidden, texts.CmdAllEnv},
		{[]string{"printenv"}, Forbidden, texts.CmdAllEnv},
		{[]string{"bash", "script.sh"}, Forbidden, texts.CmdScriptFile},
		{[]string{"bash"}, Forbidden, texts.CmdScriptFile},
		{[]string{"python3", "-m", "http.server"}, Forbidden, texts.CmdScriptFile},
		{[]string{"awk", "-f", "prog.awk"}, Forbidden, texts.CmdScriptFile},
		{[]string{"docker", "run", "-v", "/:/h", "alpine"}, Forbidden, texts.CmdOutside},
		{[]string{"docker", "exec", "web", "sh"}, Forbidden, texts.CmdOutside},
		{[]string{"docker", "create", "alpine"}, Forbidden, texts.CmdOutside},
		{[]string{"docker", "start", "web"}, Forbidden, texts.CmdOutside},
		{[]string{"docker", "compose", "up"}, Forbidden, texts.CmdOutside},
		{[]string{"docker", "-H", "tcp://x", "ps"}, Forbidden, texts.CmdDockerOther},
		{[]string{"docker", "plugin", "install", "x"}, Forbidden, texts.CmdDockerOther},
		{[]string{"systemd-run", "x"}, Forbidden, texts.CmdOutside},
		{[]string{"systemctl", "--user", "start", "x"}, Forbidden, texts.CmdOutside},
		{[]string{"systemctl", "--user", "enable", "x"}, Forbidden, texts.CmdOutside},
		{[]string{"systemctl", "stop", "x"}, Forbidden, texts.CmdRoot},
		{[]string{"crontab", "-e"}, Forbidden, texts.CmdOutside},
		{[]string{"at", "now"}, Forbidden, texts.CmdOutside},
		{[]string{"nohup", "x"}, Forbidden, texts.CmdOutside},
		{[]string{"setsid", "x"}, Forbidden, texts.CmdOutside},
		{[]string{"ssh", "host"}, Forbidden, texts.CmdOutside},
		{[]string{"tmux"}, Forbidden, texts.CmdOutside},
	}
	for _, c := range cases {
		got := Classify(c.argv)
		if got.Class != c.class || got.Line != c.line {
			t.Errorf("Classify(%q) = %s %s, want %s %s", c.argv, got.Class, got.Line, c.class, c.line)
		}
	}
}

func TestEveryLineIsAShippedText(t *testing.T) {
	for _, e := range allEntries() {
		if _, ok := texts.EnglishAll()[e.Line]; !ok {
			t.Errorf("line %q has no English text", e.Line)
		}
	}
}

func TestNeedsPress(t *testing.T) {
	for c, want := range map[Class]bool{Read: false, Content: true, Change: true, Script: true, Forbidden: false} {
		if c.NeedsPress() != want {
			t.Errorf("%s.NeedsPress() = %v", c, !want)
		}
	}
}
