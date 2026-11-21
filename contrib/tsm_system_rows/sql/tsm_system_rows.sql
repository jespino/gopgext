CREATE FUNCTION system_rows(internal)
RETURNS tsm_handler
AS 'MODULE_PATHNAME', 'gopgext_tablesample_handler'
LANGUAGE C STRICT;
