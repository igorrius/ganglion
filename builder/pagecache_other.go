//go:build !linux

package builder

// dropFileCache does nothing where the node cannot advise the kernel about
// a file's cached pages.
func dropFileCache(string) {}
