//go:build windows

package localapi

import (
	"os"

	"tunnelx/internal/keyperm"
)

func secureEndpointFile(f *os.File) error {
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	return keyperm.Fix(f.Name())
}
