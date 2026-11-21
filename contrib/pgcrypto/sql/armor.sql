-- armor() with headers: the Go function takes the arrays as pg.AnyArray
-- (to check their dimensions), so its CREATE FUNCTION is here.
CREATE FUNCTION armor(bytea, text[], text[])
RETURNS text
AS 'MODULE_PATHNAME', 'gopgext_fn_ArmorHeaders'
LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;
