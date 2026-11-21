-- From lo--1.1.sql and lo--1.1--1.2.sql (lo_manage is generated from Go).

--
--	Create the data type ... now just a domain over OID
--

CREATE DOMAIN lo AS pg_catalog.oid;

--
-- For backwards compatibility, define a function named lo_oid.
--
-- The other functions that formerly existed are not needed because
-- the implicit casts between a domain and its underlying type handle them.
--
CREATE FUNCTION lo_oid(lo) RETURNS pg_catalog.oid
LANGUAGE SQL STRICT IMMUTABLE PARALLEL SAFE
RETURN (SELECT $1::pg_catalog.oid);
