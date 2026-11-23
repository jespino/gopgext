# Developer targets for gopgext itself. Users can install with
# `go install github.com/jespino/gopgext@latest`, then run `gopgext init` / `gopgext build`.
#
#   make                          build ./gopgext
#   make test                     vet + unit tests
#   make pgtest [PG_CONFIG=...]   build the examples and test fixtures, install
#                                 them into that PostgreSQL and run their SQL
#                                 tests in a throwaway cluster (_pgtest/)
#   make site                     build the website (site/) into site/public/
#   make site-serve               preview the website at http://localhost:1313/gopgext/

PG_CONFIG ?= pg_config
HUGO ?= hugo

# Projects tested a second time loaded through shared_preload_libraries.
PRELOADED = $(addprefix examples/,hello types inventory textutil numbers_fdw vector geo \
	trgm quadtree eventlog stats textsearch) $(addprefix contrib/,fuzzystrmatch isn pg_stat_statements pgcrypto \
	auto_explain passwordcheck \
	dict_int dict_xsyn unaccent hstore citext \
	tablefunc uuid-ossp tsm_system_rows tsm_system_time \
	cube earthdistance seg) \
	examples/triggers examples/ticker examples/writebehind $(addprefix contrib/,tcn lo autoinc insert_username moddatetime refint)
comma := ,
space := $(subst ,, )

.PHONY: all gopgext test pgtest site site-api site-serve clean
all: gopgext

gopgext:
	go build -o gopgext .

test:
	go vet ./...
	go test ./...

pgtest: gopgext
	sh test/run.sh $(PG_CONFIG)
	PRELOAD=$(subst $(space),$(comma),$(notdir $(PRELOADED))) PGTEST_PORT=5498 sh test/run.sh $(PG_CONFIG) $(PRELOADED)

# The API reference pages are generated from the packages' doc comments.
site-api:
	cd site/apidoc && go run . -src ../../internal/assets/_files/lib -import gopgext -skip internal,fmgr -out ../content/api

site: site-api
	$(HUGO) --source site --minify --gc

site-serve: site-api
	$(HUGO) server --source site

clean:
	rm -rf gopgext _pgtest site/public site/resources site/content/api examples/*/*.so examples/*/*.control examples/*/*--*.sql \
		contrib/*/*.so contrib/*/*.control contrib/*/*--*.sql \
		test/fixtures/*/*.so test/fixtures/*/*.control test/fixtures/*/*--*.sql
