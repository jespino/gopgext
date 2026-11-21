/*
 * C side of gopgext/selectivity: restriction and join selectivity
 * estimators (the RESTRICT and JOIN functions of operators) implemented in
 * Go.
 *
 * The planner's structures stay here: this code finds the column and the
 * constant of the clause (get_restriction_variable, get_join_variables),
 * reads the column's statistics (pg_statistic: null fraction, distinct
 * values, most common values, histogram) and passes plain values to the Go
 * estimator registered under the name of the SQL function being called.
 */
#include "postgres.h"

#include "access/htup_details.h"
#include "catalog/pg_statistic.h"
#include "nodes/nodeFuncs.h"
#include "utils/lsyscache.h"
#include "utils/selfuncs.h"
#include "utils/syscache.h"

#include "gopgext.h"

void		gopgext_go_selectivity_restriction(struct gopgext_call *call);
void		gopgext_go_selectivity_join(struct gopgext_call *call);

/* Statistics of one side of a clause. Mirrored by selectivity.cstats. */
struct sel_stats
{
	int64		present;		/* the side is a column (or an indexed expression) */
	int64		type;
	double		rows;			/* estimated rows of its table */
	int64		has_stats;
	double		nullfrac;
	double		distinct;		/* stadistinct */
	int64		width;
	int64		n_mcv;
	Datum	   *mcv;
	double	   *mcv_freq;
	int64		mcv_type;
	int64		n_hist;
	Datum	   *hist;
	int64		hist_type;
};

/* What Go gets. Mirrored by selectivity.cinput. */
struct sel_input
{
	const char *name;			/* the estimator: the SQL function's name */
	int64		op;
	int64		has_const;
	int64		const_type;
	Datum		const_value;
	int64		var_on_left;
	int64		jointype;
	struct sel_stats left;		/* restriction: the column */
	struct sel_stats right;
};

/* The slots of the statistics read for one side. */
struct sel_slots
{
	AttStatsSlot mcv;
	AttStatsSlot hist;
	bool		has_mcv;
	bool		has_hist;
};

static const char *
estimator_name(FunctionCallInfo fcinfo)
{
	if (fcinfo->flinfo->fn_extra == NULL)
	{
		char	   *name = get_func_name(fcinfo->flinfo->fn_oid);

		if (name == NULL)
			elog(ERROR, "cache lookup failed for function %u", fcinfo->flinfo->fn_oid);
		fcinfo->flinfo->fn_extra = MemoryContextStrdup(fcinfo->flinfo->fn_mcxt, name);
	}
	return fcinfo->flinfo->fn_extra;
}

static void
fill_stats(VariableStatData *vardata, struct sel_stats *s, struct sel_slots *slots)
{
	Form_pg_statistic st;

	memset(slots, 0, sizeof(*slots));
	s->present = vardata->var != NULL;
	s->type = vardata->vartype;
	s->rows = vardata->rel ? vardata->rel->tuples : 0;
	if (!HeapTupleIsValid(vardata->statsTuple))
		return;
	st = (Form_pg_statistic) GETSTRUCT(vardata->statsTuple);
	s->has_stats = 1;
	s->nullfrac = st->stanullfrac;
	s->distinct = st->stadistinct;
	s->width = st->stawidth;
	/* Values only if the user may read the column (as the built-in estimators). */
	if (!vardata->acl_ok)
		return;
	if (get_attstatsslot(&slots->mcv, vardata->statsTuple, STATISTIC_KIND_MCV, InvalidOid,
						 ATTSTATSSLOT_VALUES | ATTSTATSSLOT_NUMBERS))
	{
		slots->has_mcv = true;
		s->n_mcv = slots->mcv.nvalues;
		s->mcv = slots->mcv.values;
		s->mcv_type = slots->mcv.valuetype;
		s->mcv_freq = palloc(sizeof(double) * Max(slots->mcv.nnumbers, 1));
		for (int i = 0; i < slots->mcv.nnumbers; i++)
			s->mcv_freq[i] = slots->mcv.numbers[i];
	}
	if (get_attstatsslot(&slots->hist, vardata->statsTuple, STATISTIC_KIND_HISTOGRAM, InvalidOid,
						 ATTSTATSSLOT_VALUES))
	{
		slots->has_hist = true;
		s->n_hist = slots->hist.nvalues;
		s->hist = slots->hist.values;
		s->hist_type = slots->hist.valuetype;
	}
}

static void
free_stats(struct sel_slots *slots)
{
	if (slots->has_mcv)
		free_attstatsslot(&slots->mcv);
	if (slots->has_hist)
		free_attstatsslot(&slots->hist);
}

static double
call_go(gopgext_gofn fn, struct sel_input *in)
{
	LOCAL_FCINFO(fc, 1);
	Datum		result;

	InitFunctionCallInfoData(*fc, NULL, 1, InvalidOid, NULL, NULL);
	fc->args[0].value = PointerGetDatum(in);
	fc->args[0].isnull = false;
	result = gopgext_invoke(fc, fn);
	return DatumGetFloat8(result);
}

PG_FUNCTION_INFO_V1(gopgext_restriction_selectivity);
PG_FUNCTION_INFO_V1(gopgext_join_selectivity);

/*
 * restrict(internal, oid, internal, integer) RETURNS float8: the fraction of
 * a table's rows matching "column op value".
 */
Datum
gopgext_restriction_selectivity(PG_FUNCTION_ARGS)
{
	PlannerInfo *root = (PlannerInfo *) PG_GETARG_POINTER(0);
	List	   *args = (List *) PG_GETARG_POINTER(2);
	int			varRelid = PG_GETARG_INT32(3);
	VariableStatData vardata;
	Node	   *other;
	bool		varonleft;
	struct sel_input in;
	struct sel_slots slots;
	double		sel;

	memset(&in, 0, sizeof(in));
	in.name = estimator_name(fcinfo);
	in.op = PG_GETARG_OID(1);
	if (!get_restriction_variable(root, args, varRelid, &vardata, &other, &varonleft))
		PG_RETURN_FLOAT8(call_go(gopgext_go_selectivity_restriction, &in));

	in.var_on_left = varonleft;
	if (IsA(other, Const))
	{
		Const	   *c = (Const *) other;

		if (c->constisnull)
		{
			/* Strict operators never match NULL. */
			ReleaseVariableStats(vardata);
			PG_RETURN_FLOAT8(0.0);
		}
		in.has_const = 1;
		in.const_type = c->consttype;
		in.const_value = c->constvalue;
	}
	fill_stats(&vardata, &in.left, &slots);
	sel = call_go(gopgext_go_selectivity_restriction, &in);
	free_stats(&slots);
	ReleaseVariableStats(vardata);
	CLAMP_PROBABILITY(sel);
	PG_RETURN_FLOAT8(sel);
}

/*
 * join(internal, oid, internal, smallint, internal) RETURNS float8: the
 * fraction of the cross product of two tables matching "a op b".
 */
Datum
gopgext_join_selectivity(PG_FUNCTION_ARGS)
{
	PlannerInfo *root = (PlannerInfo *) PG_GETARG_POINTER(0);
	List	   *args = (List *) PG_GETARG_POINTER(2);
	SpecialJoinInfo *sjinfo = (SpecialJoinInfo *) PG_GETARG_POINTER(4);
	VariableStatData vardata1;
	VariableStatData vardata2;
	bool		reversed;
	struct sel_input in;
	struct sel_slots slots1;
	struct sel_slots slots2;
	double		sel;

	memset(&in, 0, sizeof(in));
	in.name = estimator_name(fcinfo);
	in.op = PG_GETARG_OID(1);
	in.jointype = PG_GETARG_INT16(3);
	if (list_length(args) != 2)
		PG_RETURN_FLOAT8(call_go(gopgext_go_selectivity_join, &in));
	get_join_variables(root, args, sjinfo, &vardata1, &vardata2, &reversed);
	fill_stats(&vardata1, &in.left, &slots1);
	fill_stats(&vardata2, &in.right, &slots2);
	sel = call_go(gopgext_go_selectivity_join, &in);
	free_stats(&slots1);
	free_stats(&slots2);
	ReleaseVariableStats(vardata1);
	ReleaseVariableStats(vardata2);
	CLAMP_PROBABILITY(sel);
	PG_RETURN_FLOAT8(sel);
}
