package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/sqlitedb"
)

var testTime = time.Date(2026, 11, 17, 3, 0, 0, 0, time.UTC)

// node is a data directory like a node's, with a live database.
type node struct {
	dir string
	db  *sql.DB
}

func writeFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func newNode(t *testing.T) *node {
	t.Helper()
	dir := t.TempDir()
	db, err := sqlitedb.Open(context.Background(), filepath.Join(dir, dbName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec("CREATE TABLE memory (id INTEGER PRIMARY KEY, topic TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO memory (topic) VALUES ('birthdays')"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "workspace", ".git", "HEAD"), "ref: refs/heads/main\n", 0o644)
	writeFile(t, filepath.Join(dir, "workspace", ".git", "objects", "ab", "cdef"), "object", 0o444)
	writeFile(t, filepath.Join(dir, "workspace", "notes", "a.md"), "# a", 0o644)
	if err := os.MkdirAll(filepath.Join(dir, "workspace", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "modules", "weather", "module.wasm"), "wasm", 0o755)
	writeFile(t, filepath.Join(dir, "modules", "templates", "rust", "main.rs"), "template", 0o644)
	writeFile(t, filepath.Join(dir, "ganglion.yaml"), "node: x\n", 0o600)
	// Not for the archive.
	writeFile(t, filepath.Join(dir, "master.key"), "SECRET", 0o600)
	writeFile(t, filepath.Join(dir, "logs", "x.log"), "log", 0o644)
	return &node{dir: dir, db: db}
}

func (n *node) sources(t *testing.T) Sources {
	t.Helper()
	snap := filepath.Join(t.TempDir(), dbName)
	if err := Snapshot(context.Background(), n.db, snap); err != nil {
		t.Fatal(err)
	}
	c := DefaultConfig(n.dir)
	return Sources{DB: snap, Workspace: c.Workspace, Modules: c.Modules, Settings: c.Settings,
		Version: "v1.2.3", Schema: 42, Time: testTime}
}

// packSealed is the sealed archive of the node.
func packSealed(t *testing.T, n *node, key []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	sw, err := NewWriter(&buf, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Pack(context.Background(), sw, n.sources(t)); err != nil {
		t.Fatal(err)
	}
	if err := sw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// tree lists the files and folders under dir, with their modes.
func tree(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, _ := d.Info()
		rel, _ := filepath.Rel(dir, p)
		out = append(out, rel+" "+info.Mode().String())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSnapshotIsAConsistentCheckedCopy(t *testing.T) {
	n := newNode(t)
	dst := filepath.Join(t.TempDir(), dbName)
	if err := Snapshot(context.Background(), n.db, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dst + "-wal"); err == nil {
		t.Error("the check left a journal file")
	}
	db, err := sqlitedb.OpenReadOnly(context.Background(), dst)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var topic string
	if err := db.QueryRow("SELECT topic FROM memory").Scan(&topic); err != nil || topic != "birthdays" {
		t.Fatalf("copy: %q %v", topic, err)
	}
	// A destination that exists is an error, not an overwrite.
	if err := Snapshot(context.Background(), n.db, dst); err == nil {
		t.Fatal("snapshot over an existing file")
	}
}

func TestIntegrityCheckRefusesAFileThatIsNotADatabase(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.db")
	writeFile(t, p, strings.Repeat("not a database ", 500), 0o600)
	if err := integrityCheck(context.Background(), p); err == nil {
		t.Fatal("a text file passed")
	}
}

func TestPackWritesTheManifestFirstAndLeavesOutWhatIsNotData(t *testing.T) {
	n := newNode(t)
	if err := os.Symlink("/etc/passwd", filepath.Join(n.dir, "workspace", "link")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	m, err := Pack(context.Background(), &buf, n.sources(t))
	if err != nil {
		t.Fatal(err)
	}
	gz, _ := gzip.NewReader(&buf)
	tr := tar.NewReader(gz)
	var names []string
	var fromTar Manifest
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		names = append(names, h.Name)
		if h.Name == manifestName {
			if err := json.NewDecoder(tr).Decode(&fromTar); err != nil {
				t.Fatal(err)
			}
		}
	}
	if names[0] != manifestName {
		t.Fatalf("first entry %q", names[0])
	}
	var files []string
	for f := range m.Files {
		files = append(files, f)
	}
	sort.Strings(files)
	want := []string{"ganglion.db", "ganglion.yaml", "modules/weather/module.wasm", "workspace/.git/HEAD",
		"workspace/.git/objects/ab/cdef", "workspace/notes/a.md"}
	if strings.Join(files, ",") != strings.Join(want, ",") {
		t.Fatalf("files = %v\nwant    %v", files, want)
	}
	for _, name := range names {
		if strings.Contains(name, "templates") || strings.Contains(name, "master.key") || strings.Contains(name, "logs") {
			t.Errorf("archived %q", name)
		}
	}
	if !contains(names, "workspace/empty/") {
		t.Errorf("an empty folder is lost: %v", names)
	}
	sum := sha256.Sum256([]byte("# a"))
	if m.Files["workspace/notes/a.md"] != (FileInfo{SHA256: hex.EncodeToString(sum[:]), Size: 3}) {
		t.Errorf("manifest entry: %+v", m.Files["workspace/notes/a.md"])
	}
	if m.Format != 1 || m.Version != "v1.2.3" || m.Schema != 42 || !m.Time.Equal(testTime) ||
		len(m.Skipped) != 1 || m.Skipped[0] != "workspace/link" {
		t.Errorf("manifest: %+v", m)
	}
	if fromTar.Version != m.Version || len(fromTar.Files) != len(m.Files) {
		t.Errorf("manifest in the archive differs: %+v", fromTar)
	}
}

func contains(list []string, s string) bool { return slices.Contains(list, s) }

func TestPackWithOptionalPartsMissing(t *testing.T) {
	n := newNode(t)
	src := n.sources(t)
	src.Workspace, src.Modules, src.Settings = filepath.Join(n.dir, "no1"), filepath.Join(n.dir, "no2"), filepath.Join(n.dir, "no3")
	m, err := Pack(context.Background(), &bytes.Buffer{}, src)
	if err != nil || len(m.Files) != 1 || len(m.Skipped) != 0 {
		t.Fatalf("manifest %+v, err %v", m, err)
	}
	src.DB = filepath.Join(n.dir, "missing.db")
	if _, err := Pack(context.Background(), &bytes.Buffer{}, src); err == nil {
		t.Fatal("a missing database snapshot is not optional")
	}
}

func TestPackFailsWhenAFileChangesWhileItIsArchived(t *testing.T) {
	n := newNode(t)
	src := n.sources(t)
	target := filepath.Join(n.dir, "workspace", "notes", "a.md")
	// The file changes between the hash and the copy: the hook is the
	// writer the archive goes to, which is touched after the hashing.
	w := &onFirstWrite{do: func() { writeFile(t, target, "# changed and longer", 0o644) }}
	_, err := Pack(context.Background(), w, src)
	if !errors.Is(err, ErrChanged) {
		t.Fatalf("err = %v, want ErrChanged", err)
	}
}

type onFirstWrite struct {
	once bool
	do   func()
}

func (w *onFirstWrite) Write(p []byte) (int, error) {
	if !w.once {
		w.once = true
		w.do()
	}
	return len(p), nil
}

func TestPackStopsWhenTheContextEnds(t *testing.T) {
	n := newNode(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Pack(ctx, &bytes.Buffer{}, n.sources(t)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestRestoreRoundTrip(t *testing.T) {
	n := newNode(t)
	key := testKey(7)
	sealed := packSealed(t, n, key)
	parent := t.TempDir()
	dst := filepath.Join(parent, "data")
	m, err := Restore(context.Background(), bytes.NewReader(sealed), key, dst)
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != "v1.2.3" || m.Schema != 42 {
		t.Errorf("manifest: %+v", m)
	}
	for rel, want := range map[string]string{
		"ganglion.yaml":                  "node: x\n",
		"workspace/.git/HEAD":            "ref: refs/heads/main\n",
		"workspace/.git/objects/ab/cdef": "object",
		"workspace/notes/a.md":           "# a",
		"modules/weather/module.wasm":    "wasm",
	} {
		if got := readFile(t, filepath.Join(dst, rel)); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	for _, rel := range []string{"modules/templates", "master.key", "logs"} {
		if _, err := os.Stat(filepath.Join(dst, rel)); err == nil {
			t.Errorf("%s was restored", rel)
		}
	}
	if st, err := os.Stat(filepath.Join(dst, "workspace", "empty")); err != nil || !st.IsDir() {
		t.Errorf("empty folder: %v", err)
	}
	for _, line := range tree(t, dst) {
		mode := line[strings.LastIndex(line, " ")+1:]
		switch mode {
		case "drwx------", "-rw-------", "-rwx------":
		default:
			t.Errorf("mode of %s", line)
		}
	}
	if got := tree(t, dst); !contains(got, "modules/weather/module.wasm -rwx------") {
		t.Errorf("the executable bit is lost: %v", got)
	}
	db, err := sqlitedb.Open(context.Background(), filepath.Join(dst, dbName))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var topic string
	if err := db.QueryRow("SELECT topic FROM memory").Scan(&topic); err != nil || topic != "birthdays" {
		t.Fatalf("restored database: %q %v", topic, err)
	}
	assertClean(t, parent, "data")
}

// assertClean fails when parent holds anything but the named entries: no
// staging folder and no temp file.
func assertClean(t *testing.T, parent string, names ...string) {
	t.Helper()
	list, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range list {
		got = append(got, e.Name())
	}
	if strings.Join(got, ",") != strings.Join(names, ",") {
		t.Fatalf("%s holds %v, want %v", parent, got, names)
	}
}

func TestRestoreIntoAnEmptyDirectory(t *testing.T) {
	n := newNode(t)
	dst := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(context.Background(), bytes.NewReader(packSealed(t, n, testKey(7))), testKey(7), dst); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(dst); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("dst: %v %v", st, err)
	}
	if got := readFile(t, filepath.Join(dst, "ganglion.yaml")); got != "node: x\n" {
		t.Fatal(got)
	}
}

func TestRestoreRefusesANonEmptyDestination(t *testing.T) {
	n := newNode(t)
	sealed := packSealed(t, n, testKey(7))
	parent := t.TempDir()
	dst := filepath.Join(parent, "data")
	writeFile(t, filepath.Join(dst, "keep"), "mine", 0o600)
	file := filepath.Join(parent, "afile")
	writeFile(t, file, "x", 0o600)
	for _, d := range []string{dst, file} {
		if _, err := Restore(context.Background(), bytes.NewReader(sealed), testKey(7), d); !errors.Is(err, ErrDestination) {
			t.Errorf("%s: err = %v", d, err)
		}
	}
	if got := readFile(t, filepath.Join(dst, "keep")); got != "mine" {
		t.Fatal("the destination changed")
	}
	assertClean(t, parent, "afile", "data")
}

func TestRestoreRefusesTheWrongKeyAndDamage(t *testing.T) {
	n := newNode(t)
	sealed := packSealed(t, n, testKey(7))
	damaged := bytes.Clone(sealed)
	damaged[len(damaged)-3] ^= 1
	for name, tc := range map[string]struct {
		in  []byte
		key []byte
		err error
	}{
		"wrong key": {sealed, testKey(8), ErrCorrupt},
		"damaged":   {damaged, testKey(7), ErrCorrupt},
		"cut":       {sealed[:len(sealed)/2], testKey(7), ErrCorrupt},
		"not ours":  {[]byte("plain text, not a backup at all......."), testKey(7), ErrFormat},
	} {
		parent := t.TempDir()
		_, err := Restore(context.Background(), bytes.NewReader(tc.in), tc.key, filepath.Join(parent, "data"))
		if !errors.Is(err, tc.err) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.err)
		}
		assertClean(t, parent)
	}
}

// craft builds a sealed archive from entries; the first is the manifest as
// given, so a test can lie in it.
type tarEntry struct {
	hdr  tar.Header
	body string
}

func file(name, body string) tarEntry {
	return tarEntry{hdr: tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: int64(len(body))}, body: body}
}

func okManifest(files ...tarEntry) Manifest {
	m := Manifest{Format: 1, Time: testTime, Files: map[string]FileInfo{}, Skipped: []string{}}
	for _, f := range files {
		if f.hdr.Typeflag == tar.TypeReg {
			sum := sha256.Sum256([]byte(f.body))
			m.Files[f.hdr.Name] = FileInfo{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(f.body))}
		}
	}
	return m
}

func craft(t *testing.T, key []byte, m Manifest, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	sw, _ := NewWriter(&buf, key)
	gz := gzip.NewWriter(sw)
	tw := tar.NewWriter(gz)
	body, _ := json.Marshal(m)
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: manifestName, Mode: 0o600, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write(body)
	for _, e := range entries {
		h := e.hdr
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(e.body))
	}
	_ = tw.Close()
	_ = gz.Close()
	_ = sw.Close()
	return buf.Bytes()
}

// realDB is the bytes of a valid database file.
func realDB(t *testing.T) string {
	t.Helper()
	return readFile(t, newNode(t).sources(t).DB)
}

func TestRestoreRefusesCraftedArchives(t *testing.T) {
	key := testKey(7)
	db := realDB(t)
	good := file(dbName, db)
	link := tarEntry{hdr: tar.Header{Typeflag: tar.TypeSymlink, Name: "workspace/l", Linkname: "/etc/passwd"}}
	hard := tarEntry{hdr: tar.Header{Typeflag: tar.TypeLink, Name: "workspace/h", Linkname: "ganglion.db"}}
	dev := tarEntry{hdr: tar.Header{Typeflag: tar.TypeChar, Name: "workspace/d"}}
	fifo := tarEntry{hdr: tar.Header{Typeflag: tar.TypeFifo, Name: "workspace/f"}}
	mismatch := okManifest(good, file("workspace/a", "one"))
	mismatch.Files["workspace/a"] = FileInfo{SHA256: strings.Repeat("0", 64), Size: 3}
	wrongSize := okManifest(good, file("workspace/a", "one"))
	wrongSize.Files["workspace/a"] = FileInfo{SHA256: wrongSize.Files["workspace/a"].SHA256, Size: 99}
	missing := okManifest(good, file("workspace/gone", "x"))
	badFormat := okManifest(good)
	badFormat.Format = 2
	noDB := okManifest(file("ganglion.yaml", "x"))
	notADB := okManifest(file(dbName, strings.Repeat("junk", 1000)))

	for name, tc := range map[string]struct {
		m       Manifest
		entries []tarEntry
		want    error
	}{
		"parent dir":      {okManifest(good, file("../x", "x")), []tarEntry{good, file("../x", "x")}, ErrUnsafe},
		"nested parent":   {okManifest(good, file("workspace/../../x", "x")), []tarEntry{good, file("workspace/../../x", "x")}, ErrUnsafe},
		"absolute":        {okManifest(good, file("/abs", "x")), []tarEntry{good, file("/abs", "x")}, ErrUnsafe},
		"unknown top":     {okManifest(good, file("other/x", "x")), []tarEntry{good, file("other/x", "x")}, ErrUnsafe},
		"dot component":   {okManifest(good, file("workspace/./x", "x")), []tarEntry{good, file("workspace/./x", "x")}, ErrUnsafe},
		"backslash":       {okManifest(good, file(`workspace\x`, "x")), []tarEntry{good, file(`workspace\x`, "x")}, ErrUnsafe},
		"symlink":         {okManifest(good), []tarEntry{good, link}, ErrUnsafe},
		"hardlink":        {okManifest(good), []tarEntry{good, hard}, ErrUnsafe},
		"device":          {okManifest(good), []tarEntry{good, dev}, ErrUnsafe},
		"fifo":            {okManifest(good), []tarEntry{good, fifo}, ErrUnsafe},
		"duplicate":       {okManifest(good, file("workspace/a", "x")), []tarEntry{good, file("workspace/a", "x"), file("workspace/a", "x")}, ErrUnsafe},
		"not in list":     {okManifest(good), []tarEntry{good, file("workspace/extra", "x")}, ErrManifest},
		"hash mismatch":   {mismatch, []tarEntry{good, file("workspace/a", "one")}, ErrManifest},
		"size mismatch":   {wrongSize, []tarEntry{good, file("workspace/a", "one")}, ErrManifest},
		"listed missing":  {missing, []tarEntry{good}, ErrManifest},
		"format":          {badFormat, []tarEntry{good}, ErrManifest},
		"no database":     {noDB, []tarEntry{file("ganglion.yaml", "x")}, ErrManifest},
		"folder as file":  {okManifest(good), []tarEntry{{hdr: tar.Header{Typeflag: tar.TypeDir, Name: "ganglion.db/"}}}, ErrManifest},
		"broken database": {notADB, []tarEntry{file(dbName, strings.Repeat("junk", 1000))}, nil},
	} {
		parent := t.TempDir()
		dst := filepath.Join(parent, "data")
		_, err := Restore(context.Background(), bytes.NewReader(craft(t, key, tc.m, tc.entries...)), key, dst)
		switch {
		case err == nil:
			t.Errorf("%s: restored", name)
		case tc.want != nil && !errors.Is(err, tc.want):
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
		assertClean(t, parent)
	}
}

func TestRestoreRefusesAnEmptyOrManifestlessArchive(t *testing.T) {
	key := testKey(7)
	for name, plain := range map[string][]byte{"empty": nil, "not gzip": []byte("hello"), "no manifest": gzipTar(t, file("a", "b"))} {
		var buf bytes.Buffer
		sw, _ := NewWriter(&buf, key)
		_, _ = sw.Write(plain)
		_ = sw.Close()
		parent := t.TempDir()
		_, err := Restore(context.Background(), &buf, key, filepath.Join(parent, "data"))
		if !errors.Is(err, ErrManifest) {
			t.Errorf("%s: err = %v", name, err)
		}
		assertClean(t, parent)
	}
}

func gzipTar(t *testing.T, e tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	h := e.hdr
	_ = tw.WriteHeader(&h)
	_, _ = tw.Write([]byte(e.body))
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func TestRestoreRefusesMoreThanTheCap(t *testing.T) {
	x := &extractor{root: t.TempDir(), m: okManifest(file("workspace/big", "x")), seen: map[string]bool{}, total: maxRestore}
	h := file("workspace/big", "x").hdr
	err := x.file("workspace/big", filepath.Join(x.root, "big"), &h, strings.NewReader("x"))
	if !errors.Is(err, ErrUnsafe) {
		t.Fatalf("over the cap: err = %v", err)
	}
}
