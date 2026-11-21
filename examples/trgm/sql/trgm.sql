-- Operators and the GIN operator classes (the functions are generated from
-- main.go).

CREATE OPERATOR % (
	LEFTARG = text, RIGHTARG = text, PROCEDURE = similarity_op,
	COMMUTATOR = '%', RESTRICT = contsel, JOIN = contjoinsel
);

CREATE OPERATOR ^@@ (
	LEFTARG = text, RIGHTARG = text, PROCEDURE = has_word_prefix,
	RESTRICT = contsel, JOIN = contjoinsel
);

-- GIN support functions: implemented by gopgext/gin, which calls the
-- gin.OpClass registered under the operator family's name in main.go.
-- Keys are trigrams (or words), stored as bytea.

CREATE FUNCTION trgm_gin_extract_value(text, internal, internal)
	RETURNS internal AS 'MODULE_PATHNAME', 'gopgext_gin_extract_value'
	LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION trgm_gin_extract_query(text, internal, smallint, internal, internal, internal, internal)
	RETURNS internal AS 'MODULE_PATHNAME', 'gopgext_gin_extract_query'
	LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION trgm_gin_consistent(internal, smallint, text, integer, internal, internal, internal, internal)
	RETURNS boolean AS 'MODULE_PATHNAME', 'gopgext_gin_consistent'
	LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION trgm_gin_tri_consistent(internal, smallint, text, integer, internal, internal, internal)
	RETURNS "char" AS 'MODULE_PATHNAME', 'gopgext_gin_tri_consistent'
	LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE OPERATOR CLASS trgm_gin_ops
	FOR TYPE text USING gin AS
	OPERATOR 1 % (text, text),
	OPERATOR 3 ~~ (text, text),
	OPERATOR 4 ~~* (text, text),
	FUNCTION 2 trgm_gin_extract_value (text, internal, internal),
	FUNCTION 3 trgm_gin_extract_query (text, internal, smallint, internal, internal, internal, internal),
	FUNCTION 4 trgm_gin_consistent (internal, smallint, text, integer, internal, internal, internal, internal),
	FUNCTION 6 trgm_gin_tri_consistent (internal, smallint, text, integer, internal, internal, internal),
	STORAGE bytea;

-- Words, with prefix search through partial-match keys (and an explicit
-- compare function).

CREATE FUNCTION word_gin_compare(bytea, bytea)
	RETURNS integer AS 'MODULE_PATHNAME', 'gopgext_gin_compare'
	LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION word_gin_extract_value(text, internal, internal)
	RETURNS internal AS 'MODULE_PATHNAME', 'gopgext_gin_extract_value'
	LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION word_gin_extract_query(text, internal, smallint, internal, internal, internal, internal)
	RETURNS internal AS 'MODULE_PATHNAME', 'gopgext_gin_extract_query'
	LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION word_gin_consistent(internal, smallint, text, integer, internal, internal, internal, internal)
	RETURNS boolean AS 'MODULE_PATHNAME', 'gopgext_gin_consistent'
	LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION word_gin_compare_partial(bytea, bytea, smallint, internal)
	RETURNS integer AS 'MODULE_PATHNAME', 'gopgext_gin_compare_partial'
	LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE OPERATOR CLASS word_prefix_gin_ops
	FOR TYPE text USING gin AS
	OPERATOR 1 ^@@ (text, text),
	FUNCTION 1 word_gin_compare (bytea, bytea),
	FUNCTION 2 word_gin_extract_value (text, internal, internal),
	FUNCTION 3 word_gin_extract_query (text, internal, smallint, internal, internal, internal, internal),
	FUNCTION 4 word_gin_consistent (internal, smallint, text, integer, internal, internal, internal, internal),
	FUNCTION 5 word_gin_compare_partial (bytea, bytea, smallint, internal),
	STORAGE bytea;
