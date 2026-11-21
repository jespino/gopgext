/*
 * C side of gopgext/subscript: subscripting (value[subscript]) for data
 * types, implemented by the subscript.Type the extension registers in Go
 * under the type's name.
 *
 * The type's SUBSCRIPT handler is gopgext_subscript_handler. Parse
 * analysis (transform) asks Go for the subscript and element types and
 * coerces the subscripts; execution calls Go's Fetch and Assign with plain
 * values (Go never sees executor structures).
 */
#include "postgres.h"

#include "catalog/pg_type.h"
#include "executor/execExpr.h"
#include "nodes/nodeFuncs.h"
#include "nodes/subscripting.h"
#include "parser/parse_coerce.h"
#include "parser/parse_expr.h"
#include "utils/builtins.h"
#include "utils/lsyscache.h"
#include "utils/regproc.h"
#include "utils/syscache.h"

#include "gopgext.h"

/* Go (gopgext/subscript), called through gopgext_invoke. */
void		gopgext_go_subscript_config(struct gopgext_call *call);
void		gopgext_go_subscript_fetch(struct gopgext_call *call);
void		gopgext_go_subscript_assign(struct gopgext_call *call);

/* What a type's subscripts are, filled by Go. */
struct subscript_config
{
	Oid			subtype;		/* type subscripts are coerced to */
	Oid			restype;		/* type of an element */
	int64		max;			/* number of subscripts allowed */
};

/* Called by Go: resolves the type names of a configuration. */
ErrorData  *gopgext_subscript_set_config(struct subscript_config *cfg, const char *subtype,
										 const char *restype, int64 max);

ErrorData *
gopgext_subscript_set_config(struct subscript_config *cfg, const char *subtype,
							 const char *restype, int64 max)
{
	GOPGEXT_GUARD({
		cfg->subtype = DatumGetObjectId(DirectFunctionCall1(regtypein, CStringGetDatum(subtype)));
		cfg->restype = DatumGetObjectId(DirectFunctionCall1(regtypein, CStringGetDatum(restype)));
		cfg->max = max;
	});
}

/* The name of a type (what its subscript.Type is registered under). */
static char *
type_name(Oid typ)
{
	HeapTuple	tup = SearchSysCache1(TYPEOID, ObjectIdGetDatum(typ));
	char	   *name;

	if (!HeapTupleIsValid(tup))
		elog(ERROR, "cache lookup failed for type %u", typ);
	name = pstrdup(NameStr(((Form_pg_type) GETSTRUCT(tup))->typname));
	ReleaseSysCache(tup);
	return name;
}

/* Calls Go function fn with nargs Datum arguments (none NULL). */
static Datum
call_go(gopgext_gofn fn, bool *isnull, int nargs, const Datum *args)
{
	LOCAL_FCINFO(fc, 8);
	Datum		result;

	Assert(nargs <= 8);
	InitFunctionCallInfoData(*fc, NULL, nargs, InvalidOid, NULL, NULL);
	for (int i = 0; i < nargs; i++)
	{
		fc->args[i].value = args[i];
		fc->args[i].isnull = false;
	}
	result = gopgext_invoke(fc, fn);
	if (isnull)
		*isnull = fc->isnull;
	return result;
}

/*
 * Parse analysis of container[subscripts]: checks the number of subscripts,
 * coerces them to the subscript type and sets the result type.
 */
static void
subscript_transform(SubscriptingRef *sbsref, List *indirection, ParseState *pstate,
					bool isSlice, bool isAssignment)
{
	struct subscript_config cfg;
	char	   *name = type_name(sbsref->refcontainertype);
	Datum		args[2];
	List	   *upper = NIL;
	ListCell   *lc;

	args[0] = CStringGetDatum(name);
	args[1] = PointerGetDatum(&cfg);
	call_go(gopgext_go_subscript_config, NULL, 2, args);

	if (isSlice || list_length(indirection) > cfg.max)
	{
		if (cfg.max == 1)
			ereport(ERROR,
					(errcode(ERRCODE_FEATURE_NOT_SUPPORTED),
					 errmsg("%s allows only one subscript", name),
					 parser_errposition(pstate, exprLocation((Node *) indirection))));
		else if (isSlice)
			ereport(ERROR,
					(errcode(ERRCODE_FEATURE_NOT_SUPPORTED),
					 errmsg("%s does not support slices", name),
					 parser_errposition(pstate, exprLocation((Node *) indirection))));
		else
			ereport(ERROR,
					(errcode(ERRCODE_FEATURE_NOT_SUPPORTED),
					 errmsg("%s allows at most %d subscripts", name, (int) cfg.max),
					 parser_errposition(pstate, exprLocation((Node *) indirection))));
	}

	foreach(lc, indirection)
	{
		A_Indices  *ai = lfirst_node(A_Indices, lc);
		Node	   *subexpr = transformExpr(pstate, ai->uidx, pstate->p_expr_kind);

		subexpr = coerce_to_target_type(pstate, subexpr, exprType(subexpr),
										cfg.subtype, -1, COERCION_ASSIGNMENT,
										COERCE_IMPLICIT_CAST, -1);
		if (subexpr == NULL)
			ereport(ERROR,
					(errcode(ERRCODE_DATATYPE_MISMATCH),
					 errmsg("%s subscript must have type %s", name, format_type_be(cfg.subtype)),
					 parser_errposition(pstate, exprLocation(ai->uidx))));
		upper = lappend(upper, subexpr);
	}
	sbsref->refupperindexpr = upper;
	sbsref->reflowerindexpr = NIL;
	sbsref->refrestype = cfg.restype;
	sbsref->reftypmod = -1;
}

/* Fetches container[subscripts] into *resvalue (NULL subscripts: NULL). */
static void
subscript_fetch_into(SubscriptingRefState *state, Datum container, Datum *value, bool *isnull)
{
	Datum		args[4];

	for (int i = 0; i < state->numupper; i++)
		if (state->upperindexnull[i])
		{
			*isnull = true;
			return;
		}
	args[0] = CStringGetDatum((char *) state->workspace);
	args[1] = container;
	args[2] = Int64GetDatum(state->numupper);
	args[3] = PointerGetDatum(state->upperindex);
	*value = call_go(gopgext_go_subscript_fetch, isnull, 4, args);
}

static void
subscript_fetch(ExprState *state, ExprEvalStep *op, ExprContext *econtext)
{
	subscript_fetch_into(op->d.sbsref.state, *op->resvalue, op->resvalue, op->resnull);
}

/* The old element value, for nested assignments. */
static void
subscript_fetch_old(ExprState *state, ExprEvalStep *op, ExprContext *econtext)
{
	SubscriptingRefState *s = op->d.sbsref.state;

	if (*op->resnull)
	{
		s->prevvalue = (Datum) 0;
		s->prevnull = true;
	}
	else
		subscript_fetch_into(s, *op->resvalue, &s->prevvalue, &s->prevnull);
}

/* container[subscripts] = replacement: the new container into *resvalue. */
static void
subscript_assign(ExprState *state, ExprEvalStep *op, ExprContext *econtext)
{
	SubscriptingRefState *s = op->d.sbsref.state;
	Datum		args[8];
	bool		isnull;
	Datum		result;

	args[0] = CStringGetDatum((char *) s->workspace);
	args[1] = *op->resnull ? (Datum) 0 : *op->resvalue;
	args[2] = BoolGetDatum(*op->resnull);
	args[3] = Int64GetDatum(s->numupper);
	args[4] = PointerGetDatum(s->upperindex);
	args[5] = PointerGetDatum(s->upperindexnull);
	args[6] = s->replacenull ? (Datum) 0 : s->replacevalue;
	args[7] = BoolGetDatum(s->replacenull);
	result = call_go(gopgext_go_subscript_assign, &isnull, 8, args);
	*op->resvalue = result;
	*op->resnull = isnull;
}

static void
subscript_exec_setup(const SubscriptingRef *sbsref, SubscriptingRefState *sbsrefstate,
					 SubscriptExecSteps *methods)
{
	sbsrefstate->workspace = type_name(sbsref->refcontainertype);
	methods->sbs_check_subscripts = NULL;
	methods->sbs_fetch = subscript_fetch;
	methods->sbs_assign = subscript_assign;
	methods->sbs_fetch_old = subscript_fetch_old;
}

PG_FUNCTION_INFO_V1(gopgext_subscript_handler);

/* The SUBSCRIPT handler of types whose subscripting is in Go. */
Datum
gopgext_subscript_handler(PG_FUNCTION_ARGS)
{
	static const SubscriptRoutines routines = {
		.transform = subscript_transform,
		.exec_setup = subscript_exec_setup,
		.fetch_strict = true,		/* NULL container or subscript: NULL */
		.fetch_leakproof = false,
		.store_leakproof = false,
	};

	PG_RETURN_POINTER(&routines);
}
