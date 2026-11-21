-- Checks raise errors (see test/run.sh).
CREATE EXTENSION IF NOT EXISTS seg;

CREATE FUNCTION pg_temp.check(ok boolean, what text) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
	IF ok IS NOT TRUE THEN
		RAISE EXCEPTION 'check failed: %', what;
	END IF;
END $$;

SELECT pg_temp.check('5.0 .. 7.50'::seg::text = '5.0 .. 7.50', 'significant digits');
SELECT pg_temp.check('6(+-)0.5'::seg::text = '5.5 .. 6.5', 'plus-minus');
SELECT pg_temp.check('<1 .. >2e7'::seg::text = '<1 .. >2e+07', 'extensions');
SELECT pg_temp.check(seg_size('1 .. 3') = 2, 'size');
SELECT pg_temp.check('1 .. 3'::seg @> '2'::seg, 'contains');
SELECT pg_temp.check(pg_column_size('1 .. 3'::seg) = 12, 'fixed size');

DO $$
BEGIN
	PERFORM '2 .. 1'::seg;
	RAISE EXCEPTION 'no error';
EXCEPTION WHEN invalid_parameter_value THEN
	PERFORM pg_temp.check(SQLERRM = 'swapped boundaries: 2 is greater than 1', SQLERRM);
END $$;

-- GiST: index scans match sequential scans.
CREATE TABLE segs AS
	SELECT i, (((i * 7919) % 1000)::text || ' .. ' || ((i * 7919) % 1000 + i % 13)::text)::seg AS s
	FROM generate_series(1, 5000) i;
CREATE INDEX ON segs USING gist (s);
ANALYZE segs;
CREATE TEMP TABLE seq AS SELECT
	(SELECT array_agg(i ORDER BY i) FROM segs WHERE s && '100 .. 120') AS o,
	(SELECT array_agg(i ORDER BY i) FROM segs WHERE s <@ '100 .. 300') AS w,
	(SELECT array_agg(i ORDER BY i) FROM segs WHERE s << '20') AS l;
SET enable_seqscan = off;
SELECT pg_temp.check((SELECT array_agg(i ORDER BY i) FROM segs WHERE s && '100 .. 120') = o, '&& index') FROM seq;
SELECT pg_temp.check((SELECT array_agg(i ORDER BY i) FROM segs WHERE s <@ '100 .. 300') = w, '<@ index') FROM seq;
SELECT pg_temp.check((SELECT array_agg(i ORDER BY i) FROM segs WHERE s << '20') = l, '<< index') FROM seq;
RESET enable_seqscan;
DROP TABLE segs;
