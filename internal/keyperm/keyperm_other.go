//go:build !windows

package keyperm

import (
	"fmt"
	"os"
)

func check(path string) Result {
	info, err := os.Stat(path)
	if err != nil {
		return Result{Err: err}
	}
	mode := info.Mode().Perm()
	var readers []string
	if mode&0o070 != 0 {
		readers = append(readers, fmt.Sprintf("group权限%03o", mode&0o070))
	}
	if mode&0o007 != 0 {
		readers = append(readers, fmt.Sprintf("other权限%03o", mode&0o007))
	}
	return Result{OK: len(readers) == 0, Readers: readers}
}

func fix(path string) error { return os.Chmod(path, 0o600) }
