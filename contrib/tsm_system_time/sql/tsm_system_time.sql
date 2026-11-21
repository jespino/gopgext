CREATE FUNCTION system_time(internal)
RETURNS tsm_handler
AS 'MODULE_PATHNAME', 'gopgext_tablesample_handler'
LANGUAGE C STRICT;
