-- Aggregates over the Go functions (STYPE internal: an agg.State), and the
-- ~~= operator with its selectivity estimators.

CREATE AGGREGATE median(double precision) (
	SFUNC = median_add,
	STYPE = internal,
	FINALFUNC = median_final,
	COMBINEFUNC = median_combine,
	SERIALFUNC = median_serialize,
	DESERIALFUNC = median_deserialize,
	PARALLEL = SAFE
);

CREATE AGGREGATE percentile(double precision, double precision) (
	SFUNC = percentile_add,
	STYPE = internal,
	FINALFUNC = percentile_final
);

CREATE AGGREGATE mode_of(text) (
	SFUNC = mode_add,
	STYPE = internal,
	FINALFUNC = mode_final
);

CREATE FUNCTION near_sel(internal, oid, internal, integer) RETURNS double precision
	AS 'MODULE_PATHNAME', 'gopgext_restriction_selectivity' LANGUAGE C STABLE STRICT;

CREATE FUNCTION near_joinsel(internal, oid, internal, smallint, internal) RETURNS double precision
	AS 'MODULE_PATHNAME', 'gopgext_join_selectivity' LANGUAGE C STABLE STRICT;

CREATE OPERATOR ~~= (
	LEFTARG = bigint, RIGHTARG = bigint, PROCEDURE = nearly_equal,
	COMMUTATOR = ~~=, RESTRICT = near_sel, JOIN = near_joinsel
);

-- An estimator that isn't registered in Go (an error when planning).
CREATE FUNCTION unregistered_sel(internal, oid, internal, integer) RETURNS double precision
	AS 'MODULE_PATHNAME', 'gopgext_restriction_selectivity' LANGUAGE C STABLE STRICT;

CREATE OPERATOR ~~~ (
	LEFTARG = bigint, RIGHTARG = bigint, PROCEDURE = nearly_equal,
	RESTRICT = unregistered_sel
);
