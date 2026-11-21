-- unaccent: the functions, the dictionary, its search for the dictionary
-- in its own schema, rules options, and use in a text search
-- configuration (as a filtering dictionary). Checks raise errors.

CREATE EXTENSION IF NOT EXISTS unaccent;

DO $$
BEGIN
	ASSERT (SELECT prosrc FROM pg_proc WHERE proname = 'unaccent_init') = 'gopgext_tsearch_init';
	ASSERT unaccent('Hôtel crème brûlée') = 'Hotel creme brulee';
	ASSERT unaccent('ЁЖИК') = 'ЕЖИК';
	ASSERT unaccent('Œuvre ½ ℃') = 'OEuvre  1/2 °C', unaccent('Œuvre ½ ℃');
	ASSERT unaccent('foobar') = 'foobar';
	ASSERT unaccent('') = '';
	ASSERT unaccent('unaccent', 'Ångström') = 'Angstrom';
	ASSERT ts_lexize('unaccent', 'Été') = '{Ete}';
	ASSERT ts_lexize('unaccent', 'ete') IS NULL, 'nothing replaced: the next dictionary gets the word';
	-- The longest source string wins (A + combining ring, vs A).
	ASSERT unaccent(E'Å') = 'A';
END $$;

-- unaccent(text) uses the dictionary of its own schema, whatever the
-- search_path; unaccent(regdictionary, text) any dictionary.
CREATE SCHEMA t_other;
SET search_path = t_other;
DO $$
BEGIN
	ASSERT public.unaccent('à') = 'a';
	ASSERT public.unaccent('public.unaccent', 'à') = 'a';
END $$;
RESET search_path;
ALTER TEXT SEARCH DICTIONARY unaccent RENAME TO unaccent2;
DO $$
BEGIN
	PERFORM unaccent('à');
	RAISE EXCEPTION 'no error';
EXCEPTION WHEN undefined_object THEN
	ASSERT SQLERRM = 'text search dictionary "public.unaccent" does not exist', SQLERRM;
END $$;
ALTER TEXT SEARCH DICTIONARY unaccent2 RENAME TO unaccent;
DROP SCHEMA t_other;

-- Errors.
DO $$
DECLARE
	o text;
	msg text;
	state text;
BEGIN
	FOR o, msg, state IN VALUES
			('', 'missing Rules parameter', '22023'),
			(', rules = unaccent, rules = unaccent', 'multiple Rules parameters', '22023'),
			(', rules = nosuch', 'could not open unaccent file "%/tsearch_data/nosuch.rules": No such file or directory', 'F0000'),
			(', bogus = 1', 'unrecognized Unaccent parameter: "bogus"', '22023') LOOP
		BEGIN
			EXECUTE format('CREATE TEXT SEARCH DICTIONARY t_bad (TEMPLATE = unaccent%s)', o);
			RAISE EXCEPTION 'no error for %', o;
		EXCEPTION WHEN OTHERS THEN
			ASSERT SQLERRM LIKE msg AND SQLSTATE = state, format('%s: %s %s', o, SQLSTATE, SQLERRM);
		END;
	END LOOP;
END $$;

-- A filtering dictionary: unaccented words go on to simple.
CREATE TEXT SEARCH CONFIGURATION t_cfg (COPY = simple);
ALTER TEXT SEARCH CONFIGURATION t_cfg ALTER MAPPING FOR hword, hword_part, word WITH unaccent, simple;
DO $$
BEGIN
	ASSERT to_tsvector('t_cfg', 'Les Misérables à Noël') = '''a'':3 ''les'':1 ''miserables'':2 ''noel'':4', to_tsvector('t_cfg', 'Les Misérables à Noël');
	ASSERT to_tsvector('t_cfg', 'Ça été') @@ to_tsquery('t_cfg', 'ete & ca');
	ASSERT (SELECT count(*) FROM generate_series(1, 20000) i WHERE unaccent('é' || i) = 'e' || i) = 20000;
END $$;
DROP TEXT SEARCH CONFIGURATION t_cfg;
