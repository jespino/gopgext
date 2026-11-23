-- The trigger carrying a table's write-behind columns: FOR EACH STATEMENT
-- WHEN (false), it never runs (intercept.Policy.Trigger).
CREATE FUNCTION writebehind_trigger() RETURNS trigger LANGUAGE plpgsql
	AS $$BEGIN RETURN NULL; END$$;

-- Keeps the UPDATEs of tbl that only set columns of cols until
-- writebehind_flush(). keyed: WHERE pk = value doesn't read the table.
CREATE FUNCTION writebehind_enable(tbl regclass, cols text[], keyed boolean DEFAULT false)
RETURNS void LANGUAGE plpgsql AS $$
BEGIN
	EXECUTE format('DROP TRIGGER IF EXISTS writebehind ON %s', tbl);
	EXECUTE format('CREATE TRIGGER writebehind BEFORE UPDATE ON %s FOR EACH STATEMENT WHEN (false) EXECUTE FUNCTION %I.writebehind_trigger(%s)',
		tbl, (SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace WHERE e.extname = 'writebehind'),
		(SELECT string_agg(quote_literal(a), ', ')
			FROM unnest(ARRAY[CASE WHEN keyed THEN 'keyed' ELSE '' END] || cols) a));
END
$$;
