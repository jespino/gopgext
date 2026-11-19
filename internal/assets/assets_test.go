package assets

import "testing"

func TestAssets(t *testing.T) {
	for _, f := range []string{"target.json", "csrc/gopgext.h", "csrc/runtime.c", "csrc/spi.c",
		"overlay/src/runtime/runtime_pgext.go", "lib/pg/pg.go", "lib/fmgr/fmgr.go"} {
		if _, err := File(f); err != nil {
			t.Error(err)
		}
	}
}
