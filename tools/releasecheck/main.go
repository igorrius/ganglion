// Command releasecheck verifies a core release's files the way a node will
// (R-UPD-1, ADR-0081): SHA256SUMS signed by one of the keys embedded from
// update/keys, every file it lists present and matching, the binaries of
// both platforms and release.json among them, and release.json naming the
// tag. The release workflow runs it before it publishes; anyone can run it
// on a downloaded release.
//
//	releasecheck -dir dist -tag v0.2.0
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/igorrius/ganglion/update"
)

// platforms are the binaries every core release carries.
var platforms = [][2]string{{"linux", "amd64"}, {"linux", "arm64"}}

func main() {
	dir := flag.String("dir", ".", "the folder with the release's files")
	tag := flag.String("tag", "", "the tag the release must name")
	flag.Parse()
	m, k, err := check(*dir, *tag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "releasecheck:", err)
		os.Exit(1)
	}
	fmt.Printf("%s: signed by %s, commit %s, env %d\n", m.Version, k.Name, m.Commit, m.Env)
}

// check verifies the release in dir against the embedded keys.
func check(dir, tag string) (update.Manifest, update.Key, error) {
	keys, err := update.Keys()
	if err != nil {
		return update.Manifest{}, update.Key{}, err
	}
	return checkWith(dir, tag, keys)
}

func checkWith(dir, tag string, keys []update.Key) (update.Manifest, update.Key, error) {
	read := func(name string, limit int) ([]byte, error) {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err == nil && len(data) > limit {
			err = fmt.Errorf("%s: %d bytes", name, len(data))
		}
		return data, err
	}
	sumsData, err := read(update.SumsFile, update.MaxSumsBytes)
	if err != nil {
		return update.Manifest{}, update.Key{}, err
	}
	sig, err := read(update.SignatureFile, 1<<10)
	if err != nil {
		return update.Manifest{}, update.Key{}, err
	}
	k, err := update.VerifySums(sumsData, sig, keys)
	if err != nil {
		return update.Manifest{}, update.Key{}, err
	}
	sums, err := update.ParseSums(sumsData)
	if err != nil {
		return update.Manifest{}, update.Key{}, err
	}
	need := []string{update.ManifestFile}
	for _, p := range platforms {
		need = append(need, update.BinaryName(p[0], p[1]))
	}
	for _, n := range need {
		if _, ok := sums[n]; !ok {
			return update.Manifest{}, update.Key{}, fmt.Errorf("%s does not list %s", update.SumsFile, n)
		}
	}
	names := make([]string, 0, len(sums))
	for n := range sums {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		f, err := os.Open(filepath.Join(dir, n))
		if err != nil {
			return update.Manifest{}, update.Key{}, err
		}
		err = sums.Check(n, f)
		_ = f.Close()
		if err != nil {
			return update.Manifest{}, update.Key{}, err
		}
	}
	data, err := read(update.ManifestFile, update.MaxManifestBytes)
	if err != nil {
		return update.Manifest{}, update.Key{}, err
	}
	m, err := update.ParseManifest(data)
	if err != nil {
		return update.Manifest{}, update.Key{}, err
	}
	if tag != "" && m.Version != tag {
		return update.Manifest{}, update.Key{}, errors.New("release.json names " + m.Version + ", not " + tag)
	}
	return m, k, nil
}
