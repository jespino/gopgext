-- Checks of the uuid-ossp port (upstream's regression test runs with
-- pg_regress, see README.md).
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

DO $$
DECLARE
	u uuid;
	a uuid;
	b uuid;
BEGIN
	ASSERT uuid_nil() = '00000000-0000-0000-0000-000000000000';
	ASSERT uuid_ns_dns() = '6ba7b810-9dad-11d1-80b4-00c04fd430c8';
	ASSERT uuid_ns_url() = '6ba7b811-9dad-11d1-80b4-00c04fd430c8';
	ASSERT uuid_ns_oid() = '6ba7b812-9dad-11d1-80b4-00c04fd430c8';
	ASSERT uuid_ns_x500() = '6ba7b814-9dad-11d1-80b4-00c04fd430c8';
	ASSERT uuid_generate_v3(uuid_ns_dns(), 'www.widgets.com') = '3d813cbb-47fb-32ba-91df-831e1593ac29';
	ASSERT uuid_generate_v5(uuid_ns_dns(), 'www.widgets.com') = '21f7f8de-8051-5b89-8680-0195ef798b6a';
	ASSERT uuid_generate_v5(uuid_ns_url(), '') IS NOT NULL;
	ASSERT uuid_generate_v3(NULL, 'x') IS NULL;
	FOR i IN 1..100 LOOP
		u := uuid_generate_v4();
		ASSERT substr(u::text, 15, 1) = '4' AND substr(u::text, 20, 1) IN ('8', '9', 'a', 'b'), u::text;
		u := uuid_generate_v1();
		ASSERT substr(u::text, 15, 1) = '1' AND substr(u::text, 20, 1) IN ('8', '9', 'a', 'b'), u::text;
		u := uuid_generate_v1mc();
		ASSERT substr(u::text, 15, 1) = '1', u::text;
		ASSERT (('x' || substr(u::text, 25, 2))::bit(8) & '00000011') = '00000011', u::text;
	END LOOP;
	-- v1: the same node, the same clock sequence, increasing timestamps
	a := uuid_generate_v1();
	b := uuid_generate_v1();
	ASSERT substr(a::text, 20) = substr(b::text, 20);
	ASSERT (substr(a::text, 16, 3) || substr(a::text, 10, 4) || substr(a::text, 1, 8)) <
		(substr(b::text, 16, 3) || substr(b::text, 10, 4) || substr(b::text, 1, 8));
	ASSERT uuid_generate_v4() <> uuid_generate_v4();
	ASSERT (SELECT count(DISTINCT uuid_generate_v1()) FROM generate_series(1, 1000)) = 1000;
	ASSERT (SELECT count(DISTINCT uuid_generate_v4()) FROM generate_series(1, 1000)) = 1000;
	-- version 1 timestamps are the current time
	u := uuid_generate_v1();
	ASSERT abs(extract(epoch FROM now()) -
		(('x' || lpad(substr(u::text, 16, 3) || substr(u::text, 10, 4) || substr(u::text, 1, 8), 16, '0'))::bit(64)::bigint
		 - 122192928000000000) / 1e7) < 60;
END $$;
