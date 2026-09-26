//go:build windows

package localapi

import (
	"fmt"
	"os"

	"tunnelx/internal/keyperm"
)

func secureEndpointFile(f *os.File) error {
	result := keyperm.Check(f.Name())
	if result.Err != nil {
		return result.Err
	}
	if !result.OK {
		return fmt.Errorf("控制端点权限过宽")
	}
	return nil
}
