-- Operators and operator classes of epoch and code (the types and their
-- functions are generated from main.go).

CREATE OPERATOR < (LEFTARG = epoch, RIGHTARG = epoch, PROCEDURE = epoch_lt,
	COMMUTATOR = > , NEGATOR = >= , RESTRICT = scalarltsel, JOIN = scalarltjoinsel);
CREATE OPERATOR <= (LEFTARG = epoch, RIGHTARG = epoch, PROCEDURE = epoch_le,
	COMMUTATOR = >= , NEGATOR = > , RESTRICT = scalarlesel, JOIN = scalarlejoinsel);
CREATE OPERATOR = (LEFTARG = epoch, RIGHTARG = epoch, PROCEDURE = epoch_eq,
	COMMUTATOR = = , NEGATOR = <> , RESTRICT = eqsel, JOIN = eqjoinsel);
CREATE OPERATOR >= (LEFTARG = epoch, RIGHTARG = epoch, PROCEDURE = epoch_ge,
	COMMUTATOR = <= , NEGATOR = < , RESTRICT = scalargesel, JOIN = scalargejoinsel);
CREATE OPERATOR > (LEFTARG = epoch, RIGHTARG = epoch, PROCEDURE = epoch_gt,
	COMMUTATOR = < , NEGATOR = <= , RESTRICT = scalargtsel, JOIN = scalargtjoinsel);

CREATE OPERATOR CLASS epoch_btree_ops DEFAULT FOR TYPE epoch USING btree AS
	OPERATOR 1 < , OPERATOR 2 <= , OPERATOR 3 = , OPERATOR 4 >= , OPERATOR 5 > ,
	FUNCTION 1 epoch_cmp(epoch, epoch);

CREATE OPERATOR = (LEFTARG = code, RIGHTARG = code, PROCEDURE = code_eq,
	COMMUTATOR = = , RESTRICT = eqsel, JOIN = eqjoinsel);

-- BRIN support functions: all implemented by gopgext/brin, which calls the
-- brin.OpClass registered under the operator family's name in main.go.

CREATE FUNCTION eventlog_brin_opcinfo(internal) RETURNS internal
	AS 'MODULE_PATHNAME', 'gopgext_brin_opcinfo' LANGUAGE C IMMUTABLE STRICT;
CREATE FUNCTION eventlog_brin_add_value(internal, internal, internal, internal) RETURNS boolean
	AS 'MODULE_PATHNAME', 'gopgext_brin_add_value' LANGUAGE C IMMUTABLE STRICT;
CREATE FUNCTION eventlog_brin_consistent(internal, internal, internal, integer) RETURNS boolean
	AS 'MODULE_PATHNAME', 'gopgext_brin_consistent' LANGUAGE C IMMUTABLE STRICT;
CREATE FUNCTION eventlog_brin_union(internal, internal, internal) RETURNS boolean
	AS 'MODULE_PATHNAME', 'gopgext_brin_union' LANGUAGE C IMMUTABLE STRICT;

CREATE OPERATOR CLASS epoch_minmax_ops DEFAULT FOR TYPE epoch USING brin AS
	OPERATOR 1 < , OPERATOR 2 <= , OPERATOR 3 = , OPERATOR 4 >= , OPERATOR 5 > ,
	FUNCTION 1 eventlog_brin_opcinfo(internal),
	FUNCTION 2 eventlog_brin_add_value(internal, internal, internal, internal),
	FUNCTION 3 eventlog_brin_consistent(internal, internal, internal, integer),
	FUNCTION 4 eventlog_brin_union(internal, internal, internal);

CREATE OPERATOR CLASS code_bloom_ops DEFAULT FOR TYPE code USING brin AS
	OPERATOR 1 = ,
	FUNCTION 1 eventlog_brin_opcinfo(internal),
	FUNCTION 2 eventlog_brin_add_value(internal, internal, internal, internal),
	FUNCTION 3 eventlog_brin_consistent(internal, internal, internal, integer),
	FUNCTION 4 eventlog_brin_union(internal, internal, internal);
