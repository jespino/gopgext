-- Checks from PostgreSQL's own regression tests for contrib/fuzzystrmatch
-- (expected/fuzzystrmatch.out, fuzzystrmatch_utf8.out). The full upstream
-- suite runs with pg_regress, see README.md.
CREATE EXTENSION fuzzystrmatch;

DO $$
BEGIN
	ASSERT soundex('hello world!') = 'H464';
	ASSERT (soundex('Anne'), soundex('Ann'), difference('Anne', 'Ann')) = ('A500', 'A500', 4);
	ASSERT (soundex('Andrew'), difference('Anne', 'Andrew')) = ('A536', 2);
	ASSERT (soundex('Margaret'), difference('Anne', 'Margaret')) = ('M626', 0);
	ASSERT (soundex(''), difference('', '')) = ('', 4);
	ASSERT levenshtein('GUMBO', 'GAMBOL') = 2;
	ASSERT levenshtein('GUMBO', 'GAMBOL', 2, 1, 1) = 3;
	ASSERT levenshtein('GUMBO', 'GAMBOL', 1, 1, 2000000000) = 3;
	ASSERT levenshtein_less_equal('extensive', 'exhaustive', 2) = 3;
	ASSERT levenshtein_less_equal('extensive', 'exhaustive', 4) = 4;
	ASSERT levenshtein_less_equal('aaa', 'aaaaa', 1073741824, 0, 1073741824, 10) = 11;
	ASSERT metaphone('GUMBO', 4) = 'KM';
	ASSERT (dmetaphone('gumbo'), dmetaphone_alt('gumbo')) = ('KMP', 'KMP');
	ASSERT daitch_mokotoff('Halberstadt') = '{587943,587433}';
	ASSERT daitch_mokotoff('Rosochowaciec') = '{945755,945754,945745,945744,944755,944754,944745,944744}';
	ASSERT daitch_mokotoff('''OBrien') = '{079600}';
	ASSERT daitch_mokotoff('MCCOYJR') = '{651900,654900,654190,654490,645190,645490,641900,644900}';
	ASSERT daitch_mokotoff('BIERSCHBACH') = '{794575,794574,794750,794740,745750,745740,747500,747400}';
	ASSERT daitch_mokotoff('Müller') = '{689000}';
	ASSERT daitch_mokotoff('Straßburg') = '{294795}';
	ASSERT daitch_mokotoff('țamas') = '{364000,464000}';
	ASSERT daitch_mokotoff('12 !?') IS NULL;
	BEGIN
		PERFORM levenshtein('GUMBO', 'GAMBOL', 2000000000, 2000000000, 2000000000);
		RAISE 'expected an error';
	EXCEPTION WHEN numeric_value_out_of_range THEN
	END;
	BEGIN
		PERFORM metaphone('abc', 0);
		RAISE 'expected an error';
	EXCEPTION WHEN zero_length_character_string THEN
	END;
END $$;
