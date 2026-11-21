/*
 * seg 1.4: upstream's seg--1.1.sql with seg--1.1--1.2.sql, seg--1.2--1.3.sql
 * and seg--1.3--1.4.sql applied. gopgext creates the type and its input and
 * output functions (//gopgext:type in main.go) before this. The GiST
 * support functions are gopgext/gist's, exported under upstream's symbols
 * (//gopgext:alias in gist.go).
 */

COMMENT ON TYPE seg IS
'floating point interval ''FLOAT .. FLOAT'', ''.. FLOAT'', ''FLOAT ..'' or ''FLOAT''';

--
-- External C-functions for R-tree methods
--

-- Left/Right methods

CREATE FUNCTION seg_over_left(seg, seg)
RETURNS bool
AS 'MODULE_PATHNAME'
LANGUAGE C STRICT IMMUTABLE PARALLEL SAFE;

COMMENT ON FUNCTION seg_over_left(seg, seg) IS
'overlaps or is left of';

CREATE FUNCTION seg_over_right(seg, seg)
RETURNS bool
AS 'MODULE_PATHNAME'
LANGUAGE C STRICT IMMUTABLE PARALLEL SAFE;

COMMENT ON FUNCTION seg_over_right(seg, seg) IS
'overlaps or is right of';

CREATE FUNCTION seg_left(seg, seg)
RETURNS bool
AS 'MODULE_PATHNAME'
LANGUAGE C STRICT IMMUTABLE PARALLEL SAFE;

COMMENT ON FUNCTION seg_left(seg, seg) IS
'is left of';

CREATE FUNCTION seg_right(seg, seg)
RETURNS bool
AS 'MODULE_PATHNAME'
LANGUAGE C STRICT IMMUTABLE PARALLEL SAFE;

COMMENT ON FUNCTION seg_right(seg, seg) IS
'is right of';


-- Scalar comparison methods

CREATE FUNCTION seg_lt(seg, seg)
RETURNS bool
AS 'MODULE_PATHNAME'
LANGUAGE C STRICT IMMUTABLE PARALLEL SAFE;

COMMENT ON FUNCTION seg_lt(seg, seg) IS
'less than';

CREATE FUNCTION seg_le(seg, seg)
RETURNS bool
AS 'MODULE_PATHNAME'
LANGUAGE C STRICT IMMUTABLE PARALLEL SAFE;

COMMENT ON FUNCTION seg_le(seg, seg) IS
'less than or equal';

CREATE FUNCTION seg_gt(seg, seg)
RETURNS bool
AS 'MODULE_PATHNAME'
LANGUAGE C STRICT IMMUTABLE PARALLEL SAFE;

COMMENT ON FUNCTION seg_gt(seg, seg) IS
'greater than';

CREATE FUNCTION seg_ge(seg, seg)
RETURNS bool
AS 'MODULE_PATHNAME'
LANGUAGE C STRICT IMMUTABLE PARALLEL SAFE;

COMMENT ON FUNCTION seg_ge(seg, seg) IS
'greater than or equal';

CREATE FUNCTION seg_contains(seg, seg)
RETURNS bool
AS 'MODULE_PATHNAME'
LANGUAGE C STRICT IMMUTABLE PARALLEL SAFE;

COMMENT ON FUNCTION seg_contains(seg, seg) IS
'contains';

CREATE FUNCTION seg_contained(seg, seg)
RETURNS bool
AS 'MODULE_PATHNAME'
LANGUAGE C STRICT IMMUTABLE PARALLEL SAFE;

COMMENT ON FUNCTION seg_contained(seg, seg) IS
'contained in';

CREATE FUNCTION seg_overlap(seg, seg)
RETURNS bool
AS 'MODULE_PATHNAME'
LANGUAGE C STRICT IMMUTABLE PARALLEL SAFE;

COMMENT ON FUNCTION seg_overlap(seg, seg) IS
'overlaps';

CREATE FUNCTION seg_same(seg, seg)
RETURNS bool
AS 'MODULE_PATHNAME'
LANGUAGE C STRICT IMMUTABLE PARALLEL SAFE;

COMMENT ON FUNCTION seg_same(seg, seg) IS
'same as';

CREATE FUNCTION seg_different(seg, seg)
RETURNS bool
AS 'MODULE_PATHNAME'
LANGUAGE C STRICT IMMUTABLE PARALLEL SAFE;

COMMENT ON FUNCTION seg_different(seg, seg) IS
'different';

-- support routines for indexing

CREATE FUNCTION seg_cmp(seg, seg)
RETURNS int4
AS 'MODULE_PATHNAME'
LANGUAGE C STRICT IMMUTABLE PARALLEL SAFE;

COMMENT ON FUNCTION seg_cmp(seg, seg) IS 'btree comparison function';

CREATE FUNCTION seg_union(seg, seg)
RETURNS seg
AS 'MODULE_PATHNAME'
LANGUAGE C STRICT IMMUTABLE PARALLEL SAFE;

CREATE FUNCTION seg_inter(seg, seg)
RETURNS seg
AS 'MODULE_PATHNAME'
LANGUAGE C STRICT IMMUTABLE PARALLEL SAFE;

CREATE FUNCTION seg_size(seg)
RETURNS float4
AS 'MODULE_PATHNAME'
LANGUAGE C STRICT IMMUTABLE PARALLEL SAFE;

-- miscellaneous

CREATE FUNCTION seg_center(seg)
RETURNS float4
AS 'MODULE_PATHNAME'
LANGUAGE C STRICT IMMUTABLE PARALLEL SAFE;

CREATE FUNCTION seg_upper(seg)
RETURNS float4
AS 'MODULE_PATHNAME'
LANGUAGE C STRICT IMMUTABLE PARALLEL SAFE;

CREATE FUNCTION seg_lower(seg)
RETURNS float4
AS 'MODULE_PATHNAME'
LANGUAGE C STRICT IMMUTABLE PARALLEL SAFE;


--
-- OPERATORS
--

CREATE OPERATOR < (
	LEFTARG = seg,
	RIGHTARG = seg,
	PROCEDURE = seg_lt,
	COMMUTATOR = '>',
	NEGATOR = '>=',
	RESTRICT = scalarltsel,
	JOIN = scalarltjoinsel
);

CREATE OPERATOR <= (
	LEFTARG = seg,
	RIGHTARG = seg,
	PROCEDURE = seg_le,
	COMMUTATOR = '>=',
	NEGATOR = '>',
	RESTRICT = scalarlesel,
	JOIN = scalarlejoinsel
);

CREATE OPERATOR > (
	LEFTARG = seg,
	RIGHTARG = seg,
	PROCEDURE = seg_gt,
	COMMUTATOR = '<',
	NEGATOR = '<=',
	RESTRICT = scalargtsel,
	JOIN = scalargtjoinsel
);

CREATE OPERATOR >= (
	LEFTARG = seg,
	RIGHTARG = seg,
	PROCEDURE = seg_ge,
	COMMUTATOR = '<=',
	NEGATOR = '<',
	RESTRICT = scalargesel,
	JOIN = scalargejoinsel
);

CREATE OPERATOR << (
	LEFTARG = seg,
	RIGHTARG = seg,
	PROCEDURE = seg_left,
	COMMUTATOR = '>>',
	RESTRICT = positionsel,
	JOIN = positionjoinsel
);

CREATE OPERATOR &< (
	LEFTARG = seg,
	RIGHTARG = seg,
	PROCEDURE = seg_over_left,
	RESTRICT = positionsel,
	JOIN = positionjoinsel
);

CREATE OPERATOR && (
	LEFTARG = seg,
	RIGHTARG = seg,
	PROCEDURE = seg_overlap,
	COMMUTATOR = '&&',
	RESTRICT = areasel,
	JOIN = areajoinsel
);

CREATE OPERATOR &> (
	LEFTARG = seg,
	RIGHTARG = seg,
	PROCEDURE = seg_over_right,
	RESTRICT = positionsel,
	JOIN = positionjoinsel
);

CREATE OPERATOR >> (
	LEFTARG = seg,
	RIGHTARG = seg,
	PROCEDURE = seg_right,
	COMMUTATOR = '<<',
	RESTRICT = positionsel,
	JOIN = positionjoinsel
);

CREATE OPERATOR = (
	LEFTARG = seg,
	RIGHTARG = seg,
	PROCEDURE = seg_same,
	COMMUTATOR = '=',
	NEGATOR = '<>',
	RESTRICT = eqsel,
	JOIN = eqjoinsel,
	MERGES
);

CREATE OPERATOR <> (
	LEFTARG = seg,
	RIGHTARG = seg,
	PROCEDURE = seg_different,
	COMMUTATOR = '<>',
	NEGATOR = '=',
	RESTRICT = neqsel,
	JOIN = neqjoinsel
);

CREATE OPERATOR @> (
	LEFTARG = seg,
	RIGHTARG = seg,
	PROCEDURE = seg_contains,
	COMMUTATOR = '<@',
	RESTRICT = contsel,
	JOIN = contjoinsel
);

CREATE OPERATOR <@ (
	LEFTARG = seg,
	RIGHTARG = seg,
	PROCEDURE = seg_contained,
	COMMUTATOR = '@>',
	RESTRICT = contsel,
	JOIN = contjoinsel
);

-- define the GiST support methods
CREATE FUNCTION gseg_consistent(internal,seg,smallint,oid,internal)
RETURNS bool
AS 'MODULE_PATHNAME'
LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION gseg_penalty(internal,internal,internal)
RETURNS internal
AS 'MODULE_PATHNAME'
LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION gseg_picksplit(internal, internal)
RETURNS internal
AS 'MODULE_PATHNAME'
LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION gseg_union(internal, internal)
RETURNS seg
AS 'MODULE_PATHNAME'
LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION gseg_same(seg, seg, internal)
RETURNS internal
AS 'MODULE_PATHNAME'
LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;


-- Create the operator classes for indexing

CREATE OPERATOR CLASS seg_ops
    DEFAULT FOR TYPE seg USING btree AS
        OPERATOR        1       < ,
        OPERATOR        2       <= ,
        OPERATOR        3       = ,
        OPERATOR        4       >= ,
        OPERATOR        5       > ,
        FUNCTION        1       seg_cmp(seg, seg);

CREATE OPERATOR CLASS gist_seg_ops
DEFAULT FOR TYPE seg USING gist
AS
	OPERATOR	1	<< ,
	OPERATOR	2	&< ,
	OPERATOR	3	&& ,
	OPERATOR	4	&> ,
	OPERATOR	5	>> ,
	OPERATOR	6	= ,
	OPERATOR	7	@> ,
	OPERATOR	8	<@ ,
	FUNCTION	1	gseg_consistent (internal, seg, smallint, oid, internal),
	FUNCTION	2	gseg_union (internal, internal),
	FUNCTION	5	gseg_penalty (internal, internal, internal),
	FUNCTION	6	gseg_picksplit (internal, internal),
	FUNCTION	7	gseg_same (seg, seg, internal);
