-- Operators and the SP-GiST operator class of the pt type (the types and
-- their functions are generated from main.go).

CREATE OPERATOR ~= (
	LEFTARG = pt, RIGHTARG = pt, PROCEDURE = pt_same,
	COMMUTATOR = ~=, RESTRICT = eqsel, JOIN = eqjoinsel
);

CREATE OPERATOR <@ (
	LEFTARG = pt, RIGHTARG = rect, PROCEDURE = pt_within,
	RESTRICT = contsel, JOIN = contjoinsel
);

CREATE OPERATOR <-> (
	LEFTARG = pt, RIGHTARG = pt, PROCEDURE = pt_distance,
	COMMUTATOR = <->
);

-- SP-GiST support functions: all implemented by gopgext/spgist, which calls
-- the spgist.OpClass registered as "pt_quad_ops" in main.go.

CREATE FUNCTION pt_spg_config(internal, internal)
	RETURNS void AS 'MODULE_PATHNAME', 'gopgext_spgist_config'
	LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION pt_spg_choose(internal, internal)
	RETURNS void AS 'MODULE_PATHNAME', 'gopgext_spgist_choose'
	LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION pt_spg_picksplit(internal, internal)
	RETURNS void AS 'MODULE_PATHNAME', 'gopgext_spgist_picksplit'
	LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION pt_spg_inner_consistent(internal, internal)
	RETURNS void AS 'MODULE_PATHNAME', 'gopgext_spgist_inner_consistent'
	LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION pt_spg_leaf_consistent(internal, internal)
	RETURNS boolean AS 'MODULE_PATHNAME', 'gopgext_spgist_leaf_consistent'
	LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE OPERATOR CLASS pt_quad_ops
	DEFAULT FOR TYPE pt USING spgist AS
	OPERATOR 6 ~= ,
	OPERATOR 8 <@ (pt, rect),
	OPERATOR 15 <-> (pt, pt) FOR ORDER BY pg_catalog.float_ops,
	FUNCTION 1 pt_spg_config (internal, internal),
	FUNCTION 2 pt_spg_choose (internal, internal),
	FUNCTION 3 pt_spg_picksplit (internal, internal),
	FUNCTION 4 pt_spg_inner_consistent (internal, internal),
	FUNCTION 5 pt_spg_leaf_consistent (internal, internal);
