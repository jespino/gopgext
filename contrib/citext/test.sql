-- citext: quick checks (upstream's regression suite is run with pg_regress, see README).
CREATE EXTENSION IF NOT EXISTS citext;
DO $$
BEGIN
	IF NOT ('ABC'::citext = 'abc'::citext) THEN RAISE 'eq'; END IF;
	IF 'a'::citext <> 'A'::citext THEN RAISE 'ne'; END IF;
	IF NOT ('a'::citext < 'B'::citext) THEN RAISE 'lt'; END IF;
	IF citext_cmp('B', 'a') <= 0 THEN RAISE 'cmp'; END IF;
	IF citext_hash('AbC') <> citext_hash('abc') THEN RAISE 'hash'; END IF;
	IF citext_hash('abc') <> hashtext('abc') THEN RAISE 'hash value'; END IF;
	IF citext_hash_extended('abc', 7) <> hashtextextended('abc', 7) THEN RAISE 'hash ext'; END IF;
	IF min(x)::text <> 'a' OR max(x)::text <> 'C' FROM unnest(ARRAY['b','a','C']::citext[]) x THEN RAISE 'min/max'; END IF;
	IF NOT ('a'::citext ~<~ 'B'::citext) THEN RAISE 'pattern'; END IF;
	IF NOT ('Hello'::citext ~ 'h') THEN RAISE 'regex'; END IF;
	IF NOT ('Hello'::citext LIKE 'h%') THEN RAISE 'like'; END IF;
	IF strpos('ABC'::citext, 'b') <> 2 THEN RAISE 'strpos'; END IF;
END $$;
CREATE TEMP TABLE ci (t citext PRIMARY KEY);
INSERT INTO ci VALUES ('One'), ('two');
DO $$
BEGIN
	BEGIN
		INSERT INTO ci VALUES ('ONE');
		RAISE 'unique violation expected';
	EXCEPTION WHEN unique_violation THEN NULL;
	END;
	IF (SELECT count(*) FROM ci WHERE t = 'TWO') <> 1 THEN RAISE 'lookup'; END IF;
END $$;
