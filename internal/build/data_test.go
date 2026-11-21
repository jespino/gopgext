package build

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDataFiles(t *testing.T) {
	dir := t.TempDir()
	if files, err := DataFiles(filepath.Join(dir, TSearchDataDir)); err != nil || files != nil {
		t.Fatalf("missing dir: %v, %v", files, err)
	}
	data := filepath.Join(dir, TSearchDataDir)
	for _, p := range []string{"b.rules", "a.syn", ".hidden", "sub/x.stop"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(data, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(data, p), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files, err := DataFiles(data)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(data, "a.syn"), filepath.Join(data, "b.rules")}
	if !reflect.DeepEqual(files, want) {
		t.Errorf("got %v, want %v", files, want)
	}
}
