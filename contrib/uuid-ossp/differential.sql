-- Differential test: compares this Go port with the original C uuid-ossp
-- (needs the original library installed as $libdir/uuid-ossp_c.so:
--   cp $(pg_config --pkglibdir)/uuid-ossp.so $(pg_config --pkglibdir)/uuid-ossp_c.so
-- and this port installed as uuid-ossp). Prints the number of mismatches
-- per check (all must be 0).
--
--   psql -v ON_ERROR_STOP=1 -f differential.sql

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
DROP SCHEMA IF EXISTS uuid_c CASCADE;
CREATE SCHEMA uuid_c;
CREATE FUNCTION uuid_c.v3(uuid, text) RETURNS uuid AS '$libdir/uuid-ossp_c', 'uuid_generate_v3' LANGUAGE C STRICT;
CREATE FUNCTION uuid_c.v5(uuid, text) RETURNS uuid AS '$libdir/uuid-ossp_c', 'uuid_generate_v5' LANGUAGE C STRICT;
CREATE FUNCTION uuid_c.v1() RETURNS uuid AS '$libdir/uuid-ossp_c', 'uuid_generate_v1' LANGUAGE C;
CREATE FUNCTION uuid_c.v1mc() RETURNS uuid AS '$libdir/uuid-ossp_c', 'uuid_generate_v1mc' LANGUAGE C;
CREATE FUNCTION uuid_c.v4() RETURNS uuid AS '$libdir/uuid-ossp_c', 'uuid_generate_v4' LANGUAGE C;
CREATE FUNCTION uuid_c.uuid_nil() RETURNS uuid AS '$libdir/uuid-ossp_c' LANGUAGE C;
CREATE FUNCTION uuid_c.uuid_ns_dns() RETURNS uuid AS '$libdir/uuid-ossp_c' LANGUAGE C;
CREATE FUNCTION uuid_c.uuid_ns_url() RETURNS uuid AS '$libdir/uuid-ossp_c' LANGUAGE C;
CREATE FUNCTION uuid_c.uuid_ns_oid() RETURNS uuid AS '$libdir/uuid-ossp_c' LANGUAGE C;
CREATE FUNCTION uuid_c.uuid_ns_x500() RETURNS uuid AS '$libdir/uuid-ossp_c' LANGUAGE C;

CREATE TEMP TABLE inputs AS
SELECT CASE i % 5 WHEN 0 THEN uuid_ns_dns() WHEN 1 THEN uuid_ns_url() WHEN 2 THEN uuid_ns_oid()
	WHEN 3 THEN uuid_ns_x500() ELSE gen_random_uuid() END AS ns,
	CASE WHEN i % 7 = 0 THEN '' ELSE s END AS name
FROM generate_series(1, 20000) i,
	LATERAL (SELECT CASE WHEN i % 3 = 0 THEN 1000 ELSE 94 END AS range, (random() * 300)::int + i % 2 AS len) p,
	LATERAL (SELECT string_agg(c, '') AS s FROM
		(SELECT chr(32 + (random() * p.range)::int) AS c FROM generate_series(0, p.len)) z) n;

SELECT 'constants' AS check, count(*) FILTER (WHERE NOT ok) AS mismatches, count(*) AS checks FROM (VALUES
	(uuid_nil() = uuid_c.uuid_nil()), (uuid_ns_dns() = uuid_c.uuid_ns_dns()),
	(uuid_ns_url() = uuid_c.uuid_ns_url()), (uuid_ns_oid() = uuid_c.uuid_ns_oid()),
	(uuid_ns_x500() = uuid_c.uuid_ns_x500())) v(ok)
UNION ALL
SELECT 'v3', count(*) FILTER (WHERE uuid_generate_v3(ns, name) <> uuid_c.v3(ns, name)), count(*) FROM inputs
UNION ALL
SELECT 'v5', count(*) FILTER (WHERE uuid_generate_v5(ns, name) <> uuid_c.v5(ns, name)), count(*) FROM inputs
UNION ALL
-- generated UUIDs: the same version and variant, v1 timestamps close to each other's
SELECT 'v1/v1mc/v4 shape', count(*) FILTER (WHERE NOT (
		substr(uuid_generate_v1()::text, 15, 1) = substr(uuid_c.v1()::text, 15, 1) AND
		substr(uuid_generate_v1mc()::text, 15, 1) = substr(uuid_c.v1mc()::text, 15, 1) AND
		substr(uuid_generate_v4()::text, 15, 1) = substr(uuid_c.v4()::text, 15, 1) AND
		substr(uuid_generate_v4()::text, 20, 1) ~ '[89ab]' AND substr(uuid_c.v4()::text, 20, 1) ~ '[89ab]' AND
		abs(('x' || lpad(substr(uuid_generate_v1()::text, 16, 3) || substr(uuid_generate_v1()::text, 10, 4), 16, '0'))::bit(64)::bigint -
			('x' || lpad(substr(uuid_c.v1()::text, 16, 3) || substr(uuid_c.v1()::text, 10, 4), 16, '0'))::bit(64)::bigint) <= 1)),
	count(*)
FROM generate_series(1, 1000);
