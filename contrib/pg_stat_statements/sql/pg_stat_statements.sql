-- The rest of upstream's pg_stat_statements--1.12 script: the reset
-- function (with default arguments), the views and the privileges.

CREATE FUNCTION pg_stat_statements_reset(IN userid Oid DEFAULT 0,
	IN dbid Oid DEFAULT 0,
	IN queryid bigint DEFAULT 0,
	IN minmax_only boolean DEFAULT false
)
RETURNS timestamp with time zone
AS 'MODULE_PATHNAME', 'gopgext_fn_Reset'
LANGUAGE C STRICT PARALLEL SAFE;

CREATE VIEW pg_stat_statements AS
	SELECT * FROM pg_stat_statements(true);

GRANT SELECT ON pg_stat_statements TO PUBLIC;

CREATE VIEW pg_stat_statements_info AS
	SELECT * FROM pg_stat_statements_info();

GRANT SELECT ON pg_stat_statements_info TO PUBLIC;

-- Don't want this to be available to non-superusers.
REVOKE ALL ON FUNCTION pg_stat_statements_reset(Oid, Oid, bigint, boolean) FROM PUBLIC;
