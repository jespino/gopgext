-- The ivfflat access method (gopgext/am, implemented in ivfflat.go) and its
-- operator classes, as in pgvector.

CREATE FUNCTION ivfflat_handler(internal) RETURNS index_am_handler
	AS 'MODULE_PATHNAME', 'gopgext_am_handler' LANGUAGE C;

CREATE ACCESS METHOD ivfflat TYPE INDEX HANDLER ivfflat_handler;

COMMENT ON ACCESS METHOD ivfflat IS 'ivfflat index access method';

CREATE OPERATOR CLASS vector_l2_ops
	DEFAULT FOR TYPE vector USING ivfflat AS
	OPERATOR 1 <-> (vector, vector) FOR ORDER BY float_ops;

CREATE OPERATOR CLASS vector_ip_ops
	FOR TYPE vector USING ivfflat AS
	OPERATOR 1 <#> (vector, vector) FOR ORDER BY float_ops;

CREATE OPERATOR CLASS vector_cosine_ops
	FOR TYPE vector USING ivfflat AS
	OPERATOR 1 <=> (vector, vector) FOR ORDER BY float_ops;
