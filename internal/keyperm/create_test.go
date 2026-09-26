package keyperm

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateIsPrivateBeforeWritingAndNeverOverwrites(t *testing.T) {
	p := filepath.Join(t.TempDir(), "secret")
	f, err := Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if result := Check(p); result.Err != nil || !result.OK {
		t.Fatalf("new empty file is not private: %#v", result)
	}
	if _, err := f.WriteString("test-only-not-a-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(p); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing file: %v", err)
	}
}
