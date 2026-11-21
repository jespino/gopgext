-- Operators and the GiST operator class of the shape type (the type and
-- its functions are generated from main.go).

CREATE OPERATOR && (
	LEFTARG = shape, RIGHTARG = shape, PROCEDURE = shape_overlaps,
	COMMUTATOR = &&, RESTRICT = areasel, JOIN = areajoinsel
);

CREATE OPERATOR ~ (
	LEFTARG = shape, RIGHTARG = shape, PROCEDURE = shape_contains,
	COMMUTATOR = @, RESTRICT = contsel, JOIN = contjoinsel
);

CREATE OPERATOR @ (
	LEFTARG = shape, RIGHTARG = shape, PROCEDURE = shape_within,
	COMMUTATOR = ~, RESTRICT = contsel, JOIN = contjoinsel
);

CREATE OPERATOR <-> (
	LEFTARG = shape, RIGHTARG = shape, PROCEDURE = st_distance,
	COMMUTATOR = <->
);

-- GiST support functions: all implemented by gopgext/gist, which calls the
-- gist.OpClass registered as "shape_gist_ops" in main.go. Keys are bounding
-- boxes, stored as bytea.

CREATE FUNCTION shape_gist_consistent(internal, shape, smallint, oid, internal)
	RETURNS boolean AS 'MODULE_PATHNAME', 'gopgext_gist_consistent'
	LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION shape_gist_union(internal, internal)
	RETURNS bytea AS 'MODULE_PATHNAME', 'gopgext_gist_union'
	LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION shape_gist_compress(internal)
	RETURNS internal AS 'MODULE_PATHNAME', 'gopgext_gist_compress'
	LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION shape_gist_penalty(internal, internal, internal)
	RETURNS internal AS 'MODULE_PATHNAME', 'gopgext_gist_penalty'
	LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION shape_gist_picksplit(internal, internal)
	RETURNS internal AS 'MODULE_PATHNAME', 'gopgext_gist_picksplit'
	LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION shape_gist_same(bytea, bytea, internal)
	RETURNS internal AS 'MODULE_PATHNAME', 'gopgext_gist_same'
	LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION shape_gist_distance(internal, shape, smallint, oid, internal)
	RETURNS double precision AS 'MODULE_PATHNAME', 'gopgext_gist_distance'
	LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE OPERATOR CLASS shape_gist_ops
	DEFAULT FOR TYPE shape USING gist AS
	OPERATOR 3 && ,
	OPERATOR 7 ~ ,
	OPERATOR 8 @ ,
	OPERATOR 15 <-> FOR ORDER BY pg_catalog.float_ops,
	FUNCTION 1 shape_gist_consistent (internal, shape, smallint, oid, internal),
	FUNCTION 2 shape_gist_union (internal, internal),
	FUNCTION 3 shape_gist_compress (internal),
	FUNCTION 5 shape_gist_penalty (internal, internal, internal),
	FUNCTION 6 shape_gist_picksplit (internal, internal),
	FUNCTION 7 shape_gist_same (bytea, bytea, internal),
	FUNCTION 8 shape_gist_distance (internal, shape, smallint, oid, internal),
	STORAGE bytea;
