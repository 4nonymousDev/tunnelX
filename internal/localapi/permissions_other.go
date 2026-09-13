//go:build !windows

package localapi

import "os"

func secureEndpointFile(f *os.File) error { return f.Chmod(0o600) }
