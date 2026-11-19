#!/bin/sh
# Integration tests: builds each test project (examples and test fixtures)
# for the PostgreSQL of PG_CONFIG, installs it, starts a throwaway cluster in
# _pgtest/<version>/ and runs the project's test*.sql files with psql, each
# in its own session (checks raise errors; a *.fatal.sql file must end its
# session). A project can also have:
#   build-flags   extra gopgext build flags
#   expect-log    lines that must appear in the server log
#
#   sh test/run.sh [pg_config] [project ...]
#
# PRELOAD=hello,types also loads those libraries through
# shared_preload_libraries (initialized in the postmaster).
set -eu

PG_CONFIG=${1:-${PG_CONFIG:-pg_config}}
[ $# -gt 0 ] && shift
PROJECTS=${*:-$(ls -d examples/*/ test/fixtures/*/ | sed 's|/$||')}

BIN=$("$PG_CONFIG" --bindir)
VER=$("$PG_CONFIG" --version | awk '{print $2}')
DIR=$(pwd)/_pgtest/$VER
PORT=${PGTEST_PORT:-5499}

rm -rf "$DIR"
mkdir -p "$DIR"
fail=0
ok=""
for p in $PROJECTS; do
	name=$(basename "$p")
	if [ ! -f "$p/go.mod" ]; then
		ok="$ok $p" # SQL-only test of extensions built by other projects
		continue
	fi
	flags=$(cat "$p/build-flags" 2>/dev/null || true)
	# shellcheck disable=SC2086
	if ./gopgext install -pg-config "$PG_CONFIG" $flags "$p" >"$DIR/$name.build.log" 2>&1; then
		ok="$ok $p"
	else
		echo "FAIL: $name: build"
		cat "$DIR/$name.build.log"
		fail=1
	fi
done

"$BIN/initdb" -D "$DIR/data" -A trust -U postgres -E UTF8 --locale=C >"$DIR/initdb.log" 2>&1
"$BIN/pg_ctl" -D "$DIR/data" -l "$DIR/server.log" -w \
	-o "-k $DIR -c listen_addresses= -p $PORT -c shared_preload_libraries=${PRELOAD:-}" start >/dev/null
trap '"$BIN/pg_ctl" -D "$DIR/data" -m fast -w stop >/dev/null' EXIT

for p in $ok; do
	name=$(basename "$p")
	"$BIN/createdb" -h "$DIR" -p "$PORT" -U postgres "test_$name"
	# Each test*.sql runs in its own session; *.fatal.sql must end the
	# session (psql loses the connection: exit code 2).
	for t in $(ls "$p"/test*.sql | sort); do
		tn=$(basename "$t" .sql)
		set +e
		"$BIN/psql" -X -q -h "$DIR" -p "$PORT" -U postgres -v ON_ERROR_STOP=1 \
			-f "$t" "test_$name" >"$DIR/$name.$tn.log" 2>&1
		rc=$?
		set -e
		case "$t:$rc" in
		*.fatal.sql:2 | *[!l].sql:0) echo "PASS: $name/$tn (PostgreSQL $VER)" ;;
		*)
			echo "FAIL: $name/$tn (PostgreSQL $VER, psql exit code $rc)"
			cat "$DIR/$name.$tn.log"
			fail=1
			;;
		esac
	done
	if [ -f "$p/expect-log" ]; then
		while IFS= read -r line; do
			if ! grep -qF "$line" "$DIR/server.log"; then
				echo "FAIL: $name: server log lacks: $line"
				fail=1
			fi
		done <"$p/expect-log"
	fi
done
if grep -E "TRAP:|PANIC:|terminated by signal|was terminated by" "$DIR/server.log"; then
	echo "FAIL: server crashed (see $DIR/server.log)"
	fail=1
fi
exit $fail
