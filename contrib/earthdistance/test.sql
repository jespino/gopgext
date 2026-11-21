-- Checks raise errors (see test/run.sh).
CREATE EXTENSION IF NOT EXISTS earthdistance CASCADE;

CREATE FUNCTION pg_temp.check(ok boolean, what text) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
	IF ok IS NOT TRUE THEN
		RAISE EXCEPTION 'check failed: %', what;
	END IF;
END $$;

SELECT pg_temp.check((SELECT requires FROM pg_available_extension_versions
	WHERE name = 'earthdistance' AND installed) = '{cube}', 'requires cube');
-- One degree of longitude on the equator, in miles.
SELECT pg_temp.check(round(('(0,0)'::point <@> '(1,0)'::point)::numeric, 5) = 69.09318, '<@>');
SELECT pg_temp.check('(0,0)'::point <@> '(180,0)'::point = '(0,0)'::point <@> '(-180,0)'::point, 'antimeridian');
SELECT pg_temp.check(round(earth_distance(ll_to_earth(0, 0), ll_to_earth(0, 1))::numeric) = 111320, 'earth_distance');
SELECT pg_temp.check(earth_box(ll_to_earth(0, 0), 1000) @> ll_to_earth(0, 0.001), 'earth_box');
SELECT pg_temp.check(round(latitude(ll_to_earth(45, 30))::numeric, 6) = 45 AND
	round(longitude(ll_to_earth(45, 30))::numeric, 6) = 30, 'latitude, longitude');
