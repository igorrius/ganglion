// Command sandboxprobe reports what a host offers the command sandbox of
// ADR-0039: Landlock, no_new_privs, a not-dumpable process, the container it
// runs in. It is the spike of M3d task 3.20 and changes nothing on the host.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func main() {
	hold := flag.Bool("hold", false, "wait for a signal after the report, for a platform that restarts a container that exits")
	flag.Parse()
	fmt.Println("| Check | Value |")
	fmt.Println("|---|---|")
	for _, c := range checks() {
		fmt.Printf("| %s | %s |\n", c[0], c[1])
	}
	if *hold {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		<-ctx.Done()
	}
}

func checks() [][2]string {
	return [][2]string{
		{"kernel", kernel()},
		{"arch", runtime.GOARCH},
		{"euid", strconv.Itoa(os.Geteuid())},
		{"Landlock ABI", landlock()},
		{"no_new_privs", nnp()},
		{"not dumpable closes environ", dumpable()},
		{"seccomp mode", statusField("Seccomp")},
		{"Yama ptrace_scope", read("/proc/sys/kernel/yama/ptrace_scope")},
		{"unprivileged user namespaces", read("/proc/sys/kernel/unprivileged_userns_clone") + ", max " + read("/proc/sys/user/max_user_namespaces")},
		{"container", container()},
		{".env files under $HOME, depth 4", envScan()},
	}
}

func kernel() string {
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return "error: " + err.Error()
	}
	return unix.ByteSliceToString(u.Release[:]) // gVisor reports 4.4.0
}

func landlock() string {
	v, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	switch {
	case errno == unix.ENOSYS:
		return "none: no syscall (an old kernel, or seccomp blocks it)"
	case errno == unix.EOPNOTSUPP:
		return "built but off (not in the lsm= list)"
	case errno != 0:
		return "error: " + errno.Error()
	}
	return strconv.Itoa(int(v))
}

func nnp() string {
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return "cannot set: " + err.Error()
	}
	v, err := unix.PrctlRetInt(unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0)
	if err != nil {
		return "cannot read: " + err.Error()
	}
	return "set, reads " + strconv.Itoa(v)
}

func dumpable() string {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return "cannot set: " + err.Error()
	}
	var st unix.Stat_t
	if err := unix.Stat("/proc/self/environ", &st); err != nil {
		return "error: " + err.Error()
	}
	if os.Geteuid() == 0 {
		return "meaningless as root (owner uid " + strconv.Itoa(int(st.Uid)) + ")"
	}
	if st.Uid == 0 {
		return "yes, owned by root"
	}
	return "no, owned by uid " + strconv.Itoa(int(st.Uid))
}

func statusField(name string) string {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return "error: " + err.Error()
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, name+":"); ok {
			return strings.TrimSpace(v) // 0 off, 1 strict, 2 filter
		}
	}
	return "absent"
}

func read(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "absent"
	}
	return strings.TrimSpace(string(b))
}

func container() string {
	var marks []string
	for _, f := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(f); err == nil {
			marks = append(marks, f)
		}
	}
	if b, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		marks = append(marks, "pid 1 cgroup "+strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0]))
	}
	if len(marks) == 0 {
		return "no marks"
	}
	return strings.Join(marks, "; ")
}

// envScan measures what finding .env files would cost: R-CMD-3 denies them
// in any folder, and Landlock cannot deny by name.
func envScan() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "no home"
	}
	start, found, dirs := time.Now(), 0, 0
	skip := map[string]bool{".git": true, "node_modules": true, ".cache": true, "go": true, ".cargo": true, ".rustup": true}
	err = filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable folder is skipped, not fatal
		}
		depth := strings.Count(strings.TrimPrefix(p, home), string(filepath.Separator))
		if d.IsDir() {
			dirs++
			if skip[d.Name()] || depth >= 4 {
				return filepath.SkipDir
			}
			return nil
		}
		if n := d.Name(); n == ".env" || strings.HasPrefix(n, ".env.") {
			found++
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return "no home"
	}
	return fmt.Sprintf("%d found in %d folders, %s", found, dirs, time.Since(start).Round(time.Millisecond))
}
