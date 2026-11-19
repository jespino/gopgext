# Developer targets for gopgext itself. Users just need `go install .` (or
# `make`) and then `gopgext init` / `gopgext build`.
#
#   make                          build ./gopgext
#   make test                     vet + unit tests
#   make pgtest [PG_CONFIG=...]   build the examples and test fixtures, install
#                                 them into that PostgreSQL and run their SQL
#                                 tests in a throwaway cluster (_pgtest/)

PG_CONFIG ?= pg_config

.PHONY: all gopgext test pgtest clean
all: gopgext

gopgext:
	go build -o gopgext .

test:
	go vet ./...
	go test ./...

pgtest: gopgext
	sh test/run.sh $(PG_CONFIG)
	PRELOAD=hello,types,inventory,textutil,fuzzystrmatch,isn,numbers_fdw PGTEST_PORT=5498 sh test/run.sh $(PG_CONFIG) examples/hello examples/types examples/inventory examples/textutil examples/fuzzystrmatch examples/isn examples/numbers_fdw

clean:
	rm -rf gopgext _pgtest examples/*/*.so examples/*/*.control examples/*/*--*.sql \
		test/fixtures/*/*.so test/fixtures/*/*.control test/fixtures/*/*--*.sql
