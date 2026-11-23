// Package templates holds the module templates the node ships: the crates
// module.import copies into a workspace as the start of a module the agent
// writes (spec R-RDR-1, R-RUN-15..17). They are embedded in the binary, so a
// node's templates are those of its own version, as its built-in skills are.
package templates

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Each template is listed file by file, so a local build folder (target/) is
// never embedded; src is taken whole. A file added beside them must be added
// here: TestEveryFileIsEmbedded says which.
//
//go:embed reader-template/.gitignore reader-template/Cargo.lock reader-template/Cargo.toml
//go:embed reader-template/README.md reader-template/manifest.yaml reader-template/examples.yaml reader-template/src
//go:embed reflex-template/.gitignore reflex-template/Cargo.lock reflex-template/Cargo.toml
//go:embed reflex-template/README.md reflex-template/manifest.yaml reflex-template/examples.yaml reflex-template/src
//go:embed web-template/.gitignore web-template/Cargo.lock web-template/Cargo.toml
//go:embed web-template/README.md web-template/manifest.yaml web-template/examples.yaml web-template/src web-template/assets
var files embed.FS

// FS is the templates, one folder each.
func FS() fs.FS { return files }

// Write puts the templates into dir as folders of plain files, replacing
// whatever dir held, so the folder is always what this binary ships.
func Write(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("templates: %w", err)
	}
	err := fs.WalkDir(files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		to := filepath.Join(dir, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(to, 0o700)
		}
		b, err := files.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(to, b, 0o600)
	})
	if err != nil {
		return fmt.Errorf("templates: %w", err)
	}
	return nil
}
