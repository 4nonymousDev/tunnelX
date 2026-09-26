package keyperm

import "os"

// Create creates a new secret file with restrictive permissions before any
// bytes are written. It never follows or overwrites an existing path.
func Create(path string) (*os.File, error) { return create(path) }
