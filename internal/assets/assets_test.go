package assets

import "testing"

func TestAssets(t *testing.T) {
	for _, f := range []string{"target.json", "csrc/gopgext.h", "csrc/runtime.c", "csrc/spi.c",
		"overlay/src/runtime/runtime_pgext.go", "overlay/src/crypto/rand/rand_pgext.go", "lib/pg/pg.go", "lib/fmgr/fmgr.go",
		"lib/internal/core/types.go", "lib/gist/gist.go", "frameworks/gist/gist.c",
		"lib/gin/gin.go", "frameworks/gin/gin.c", "lib/spgist/spgist.go", "frameworks/spgist/spgist.c",
		"lib/brin/brin.go", "frameworks/brin/brin.c", "lib/agg/agg.go", "frameworks/agg/agg.c",
		"lib/selectivity/selectivity.go", "frameworks/selectivity/selectivity.c",
		"lib/am/am.go", "frameworks/am/am.c", "lib/hooks/hooks.go", "frameworks/hooks/hooks.c",
		"lib/shmem/shmem.go", "frameworks/shmem/shmem.c", "lib/pg/server.go",
		"lib/auth/auth.go", "frameworks/auth/auth.c", "lib/pg/message.go",
		"lib/trigger/trigger.go", "frameworks/trigger/trigger.c", "lib/pg/notify.go",
		"lib/tsearch/tsearch.go", "frameworks/tsearch/tsearch.c",
		"csrc/values.c", "lib/pg/values.go", "lib/internal/core/values.go",
		"lib/subscript/subscript.go", "frameworks/subscript/subscript.c",
		"csrc/record.c", "lib/fmgr/record.go", "lib/pg/record.go", "lib/internal/core/record.go",
		"lib/tablesample/tablesample.go", "frameworks/tablesample/tablesample.c",
		"lib/bgworker/bgworker.go", "frameworks/bgworker/bgworker.c",
		"lib/xact/xact.go", "frameworks/xact/xact.c",
		"lib/intercept/intercept.go", "frameworks/intercept/intercept.c",
		"csrc/relation.c", "lib/pg/relation.go"} {
		if _, err := File(f); err != nil {
			t.Error(err)
		}
	}
}
