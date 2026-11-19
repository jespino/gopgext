/*
 * C side of gopgext/fdw: the foreign data wrapper callbacks PostgreSQL calls
 * (planning, execution, EXPLAIN, IMPORT FOREIGN SCHEMA, option validation).
 * The wrapper itself is Go: what's specific to it (options, which conditions
 * it evaluates remotely, producing rows, imported tables) is decided by the
 * fdw.Wrapper the extension registers, called through gopgext_invoke.
 *
 * Pushdown: conditions "column op value", where value doesn't depend on the
 * foreign table and isn't volatile (a constant, now() - interval '1 hour', a
 * parameter, or a column of another table in a parameterized nested loop),
 * are offered to the wrapper's Plan; those it accepts are evaluated when the
 * scan starts (or restarts) and given to its Scan. All conditions are also
 * checked locally.
 *
 * Data exchanged with Go is text: descriptions as "key=value" items, values
 * as text (rows are converted with each column's input function).
 */
#include "postgres.h"

#include "access/htup_details.h"
#include "access/reloptions.h"
#include "access/table.h"
#include "access/xact.h"
#include "catalog/pg_foreign_data_wrapper.h"
#include "catalog/pg_foreign_server.h"
#include "catalog/pg_foreign_table.h"
#include "catalog/pg_type.h"
#include "catalog/pg_user_mapping.h"
#include "commands/defrem.h"
#include "commands/explain.h"
#if PG_VERSION_NUM >= 180000
#include "commands/explain_format.h"
#include "commands/explain_state.h"
#endif
#include "executor/executor.h"
#include "foreign/fdwapi.h"
#include "foreign/foreign.h"
#include "miscadmin.h"
#include "nodes/makefuncs.h"
#include "nodes/nodeFuncs.h"
#include "optimizer/cost.h"
#include "optimizer/optimizer.h"
#include "optimizer/pathnode.h"
#include "optimizer/paths.h"
#include "optimizer/planmain.h"
#include "optimizer/restrictinfo.h"
#include "utils/array.h"
#include "utils/builtins.h"
#include "utils/datetime.h"
#include "utils/lsyscache.h"
#include "utils/memutils.h"
#include "utils/rel.h"
#include "utils/ruleutils.h"
#include "utils/syscache.h"
#include "utils/timestamp.h"

#include "gopgext.h"

/* Go (gopgext/fdw), called through gopgext_invoke. */
void		gopgext_go_fdw_validate(struct gopgext_call *call);
void		gopgext_go_fdw_plan(struct gopgext_call *call);
void		gopgext_go_fdw_begin(struct gopgext_call *call);
void		gopgext_go_fdw_fetch(struct gopgext_call *call);
void		gopgext_go_fdw_end(struct gopgext_call *call);
void		gopgext_go_fdw_describe(struct gopgext_call *call);
void		gopgext_go_fdw_import(struct gopgext_call *call);

#define GO_FUNCTION(name, gofn)				\
	static Datum name(PG_FUNCTION_ARGS);	\
	static Datum name(PG_FUNCTION_ARGS) { return gopgext_invoke(fcinfo, gofn); }

GO_FUNCTION(go_validate, gopgext_go_fdw_validate)
GO_FUNCTION(go_plan, gopgext_go_fdw_plan)
GO_FUNCTION(go_begin, gopgext_go_fdw_begin)
GO_FUNCTION(go_fetch, gopgext_go_fdw_fetch)
GO_FUNCTION(go_end, gopgext_go_fdw_end)
GO_FUNCTION(go_describe, gopgext_go_fdw_describe)
GO_FUNCTION(go_import, gopgext_go_fdw_import)

#define BATCH_ROWS 1000			/* rows per call into Go */

/* ---- Descriptions for Go ------------------------------------------- */

static void
add_item(List **items, const char *fmt,...) pg_attribute_printf(2, 3);

static void
add_item(List **items, const char *fmt,...)
{
	StringInfoData s;
	va_list		args;

	initStringInfo(&s);
	for (;;)
	{
		int			needed;

		va_start(args, fmt);
		needed = appendStringInfoVA(&s, fmt, args);
		va_end(args);
		if (needed == 0)
			break;
		enlargeStringInfo(&s, needed);
	}
	*items = lappend(*items, s.data);
}

static void
add_options(List **items, const char *prefix, List *options)
{
	ListCell   *lc;

	foreach(lc, options)
	{
		DefElem    *def = lfirst_node(DefElem, lc);

		add_item(items, "%s%s=%s", prefix, def->defname, defGetString(def));
	}
}

static ArrayType *
items_array(List *items)
{
	Datum	   *datums = palloc(sizeof(Datum) * (list_length(items) + 1));
	ListCell   *lc;
	int			i = 0;

	foreach(lc, items)
		datums[i++] = CStringGetTextDatum((char *) lfirst(lc));
	return construct_array(datums, i, TEXTOID, -1, false, TYPALIGN_INT);
}

/* Options of the user mapping for the current user (or PUBLIC), if any. */
static List *
user_mapping_options(Oid serverid)
{
	Oid			users[2] = {GetUserId(), InvalidOid};

	for (int i = 0; i < 2; i++)
	{
		HeapTuple	tp = SearchSysCache2(USERMAPPINGUSERSERVER,
										 ObjectIdGetDatum(users[i]), ObjectIdGetDatum(serverid));

		if (HeapTupleIsValid(tp))
		{
			bool		isnull;
			Datum		d = SysCacheGetAttr(USERMAPPINGUSERSERVER, tp,
											Anum_pg_user_mapping_umoptions, &isnull);
			List	   *options = isnull ? NIL : untransformRelOptions(d);

			ReleaseSysCache(tp);
			return options;
		}
	}
	return NIL;
}

/* The description of a foreign table (fdw.Table). */
static List *
table_items(Oid relid)
{
	ForeignTable *table = GetForeignTable(relid);
	ForeignServer *server = GetForeignServer(table->serverid);
	Relation	rel = table_open(relid, AccessShareLock);
	TupleDesc	desc = RelationGetDescr(rel);
	List	   *items = NIL;

	add_item(&items, "table=%s", RelationGetRelationName(rel));
	add_item(&items, "server=%s", server->servername);
	add_options(&items, "sopt.", server->options);
	add_options(&items, "topt.", table->options);
	add_options(&items, "uopt.", user_mapping_options(server->serverid));
	for (int i = 0; i < desc->natts; i++)
	{
		Form_pg_attribute att = TupleDescAttr(desc, i);
		char		prefix[32];

		if (att->attisdropped)
		{
			add_item(&items, "col=\x1f\x1f" "1");
			continue;
		}
		add_item(&items, "col=%s\x1f%s\x1f" "0", NameStr(att->attname),
				 format_type_with_typemod(att->atttypid, att->atttypmod));
		snprintf(prefix, sizeof(prefix), "copt.%d.", i);
		add_options(&items, prefix, GetForeignColumnOptions(relid, att->attnum));
	}
	table_close(rel, AccessShareLock);
	return items;
}

/* Statement start, in microseconds since the Unix epoch (fdw.Now). */
int64		gopgext_fdw_statement_start(void);

int64
gopgext_fdw_statement_start(void)
{
	return GetCurrentStatementStartTimestamp() +
		(int64) (POSTGRES_EPOCH_JDATE - UNIX_EPOCH_JDATE) * SECS_PER_DAY * USECS_PER_SEC;
}

/* ---- Options ------------------------------------------------------- */

PG_FUNCTION_INFO_V1(gopgext_fdw_validator);
Datum
gopgext_fdw_validator(PG_FUNCTION_ARGS)
{
	List	   *options = untransformRelOptions(PG_GETARG_DATUM(0));
	Oid			catalog = PG_GETARG_OID(1);
	List	   *items = NIL;
	const char *where;

	switch (catalog)
	{
		case ForeignServerRelationId:
			where = "server";
			break;
		case ForeignTableRelationId:
			where = "table";
			break;
		case AttributeRelationId:
			where = "column";
			break;
		case UserMappingRelationId:
			where = "user_mapping";
			break;
		default:
			where = "wrapper";
			break;
	}
	add_item(&items, "catalog=%s", where);
	add_options(&items, "opt.", options);
	DirectFunctionCall1(go_validate, PointerGetDatum(items_array(items)));
	PG_RETURN_VOID();
}

/* ---- Planning ------------------------------------------------------ */

/* A condition that could be pushed down. */
typedef struct Candidate
{
	RestrictInfo *rinfo;
	int			attno;
	const char *op;
	const char *kind;
	Expr	   *value;
} Candidate;

/* What the planner knows about a foreign table (RelOptInfo.fdw_private). */
typedef struct FdwRel
{
	List	   *items;			/* table description for Go */
	int			natts;
	List	   *base;			/* Candidates among the table's own conditions */
	bool		base_ok;		/* Plan accepted a scan with them */
	double		rows;
} FdwRel;

static Node *
strip(Node *n)
{
	while (n && IsA(n, RelabelType))
		n = (Node *) ((RelabelType *) n)->arg;
	return n;
}

static bool
is_our_column(RelOptInfo *rel, Node *n)
{
	return n && IsA(n, Var) && ((Var *) n)->varno == rel->relid &&
		((Var *) n)->varlevelsup == 0 && ((Var *) n)->varattno > 0;
}

static const char *
value_kind(Oid type)
{
	switch (type)
	{
		case TIMESTAMPTZOID:
		case TIMESTAMPOID:
			return "time";
		case INTERVALOID:
			return "interval";
		case INT2OID:
		case INT4OID:
		case INT8OID:
			return "int";
		case FLOAT4OID:
		case FLOAT8OID:
		case NUMERICOID:
			return "float";
		case BOOLOID:
			return "bool";
		case TEXTOID:
		case VARCHAROID:
		case BPCHAROID:
		case NAMEOID:
			return "text";
		default:
			return NULL;
	}
}

/* Whether clause has the form "column op value" (in either order). */
static bool
classify(PlannerInfo *root, RelOptInfo *rel, RestrictInfo *rinfo, Candidate *c)
{
	Expr	   *clause = rinfo->clause;
	OpExpr	   *opexpr;
	Node	   *l,
			   *r,
			   *val;
	Var		   *var;
	char	   *opname;
	bool		swapped = false;

	if (rinfo->pseudoconstant || !IsA(clause, OpExpr) ||
		list_length(((OpExpr *) clause)->args) != 2)
		return false;
	opexpr = (OpExpr *) clause;
	l = strip(linitial(opexpr->args));
	r = strip(lsecond(opexpr->args));
	if (is_our_column(rel, l) && !bms_is_member(rel->relid, pull_varnos(root, r)))
	{
		var = (Var *) l;
		val = lsecond(opexpr->args);
	}
	else if (is_our_column(rel, r) && !bms_is_member(rel->relid, pull_varnos(root, l)))
	{
		var = (Var *) r;
		val = linitial(opexpr->args);
		swapped = true;
	}
	else
		return false;
	if (contain_volatile_functions(val))
		return false;
	opname = get_opname(opexpr->opno);
	if (!opname)
		return false;
	if (strcmp(opname, "=") != 0 && strcmp(opname, "<>") != 0 &&
		strcmp(opname, "<") != 0 && strcmp(opname, "<=") != 0 &&
		strcmp(opname, ">") != 0 && strcmp(opname, ">=") != 0)
		return false;
	if (swapped)
	{
		if (strcmp(opname, "<") == 0)
			opname = ">";
		else if (strcmp(opname, "<=") == 0)
			opname = ">=";
		else if (strcmp(opname, ">") == 0)
			opname = "<";
		else if (strcmp(opname, ">=") == 0)
			opname = "<=";
	}
	c->kind = value_kind(exprType(val));
	if (!c->kind)
		return false;
	c->rinfo = rinfo;
	c->attno = var->varattno;
	c->op = opname;
	c->value = (Expr *) val;
	return true;
}

static List *
candidates(PlannerInfo *root, RelOptInfo *rel, List *rinfos)
{
	List	   *out = NIL;
	ListCell   *lc;

	foreach(lc, rinfos)
	{
		Candidate  *c = palloc(sizeof(Candidate));

		if (classify(root, rel, lfirst_node(RestrictInfo, lc), c))
			out = lappend(out, c);
		else
			pfree(c);
	}
	return out;
}

/*
 * Asks the wrapper's Plan which of cands it evaluates remotely. Returns
 * whether it can scan with them; sets the accepted ones in pushed (if not
 * NULL) and the row estimate.
 */
static bool
ask_plan(FdwRel *fr, List *cands, bool *pushed, double *rows)
{
	List	   *items = list_copy(fr->items);
	ArrayType  *result;
	Datum	   *elems;
	bool	   *nulls;
	int			n;
	bool		ok = true;
	ListCell   *lc;

	foreach(lc, cands)
	{
		Candidate  *c = lfirst(lc);

		add_item(&items, "cond=%d\x1f%s\x1f%s", c->attno - 1, c->op, c->kind);
	}
	result = DatumGetArrayTypeP(DirectFunctionCall1(go_plan, PointerGetDatum(items_array(items))));
	deconstruct_array(result, TEXTOID, -1, false, TYPALIGN_INT, &elems, &nulls, &n);
	if (pushed)
		memset(pushed, 0, sizeof(bool) * list_length(cands));
	for (int i = 0; i < n; i++)
	{
		char	   *item = TextDatumGetCString(elems[i]);

		if (strncmp(item, "rows=", 5) == 0)
			*rows = strtod(item + 5, NULL);
		else if (strncmp(item, "error=", 6) == 0)
			ok = false;
		else if (strncmp(item, "push=", 5) == 0 && pushed)
		{
			int			k = atoi(item + 5);

			if (k >= 0 && k < list_length(cands))
				pushed[k] = true;
		}
	}
	return ok;
}

static void
fdwGetForeignRelSize(PlannerInfo *root, RelOptInfo *baserel, Oid foreigntableid)
{
	FdwRel	   *fr = palloc0(sizeof(FdwRel));

	fr->items = table_items(foreigntableid);
	{
		Relation	rel = table_open(foreigntableid, NoLock);

		fr->natts = RelationGetNumberOfAttributes(rel);
		table_close(rel, NoLock);
	}
	fr->base = candidates(root, baserel, baserel->baserestrictinfo);
	fr->rows = 1000;
	fr->base_ok = ask_plan(fr, fr->base, NULL, &fr->rows);
	baserel->fdw_private = fr;
	baserel->rows = Max(fr->rows, 1);
}

static void
add_scan_path(PlannerInfo *root, RelOptInfo *baserel, double rows, bool disabled,
			  Relids required_outer)
{
	Cost		startup = 1000.0;	/* a remote request */
	Cost		total = startup + rows * cpu_tuple_cost * 10;

#if PG_VERSION_NUM >= 180000
	add_path(baserel, (Path *) create_foreignscan_path(root, baserel, NULL, rows,
													   disabled ? 1 : 0, startup, total,
													   NIL, required_outer, NULL, NIL, NIL));
#else
	if (disabled)
	{
		startup += disable_cost;
		total += disable_cost;
	}
	add_path(baserel, (Path *) create_foreignscan_path(root, baserel, NULL, rows,
													   startup, total, NIL, required_outer,
													   NULL,
#if PG_VERSION_NUM >= 170000
													   NIL,
#endif
													   NIL));
#endif
}

static bool
ec_member_is_column(PlannerInfo *root, RelOptInfo *rel, EquivalenceClass *ec,
					EquivalenceMember *em, void *arg)
{
	Node	   *expr = strip((Node *) em->em_expr);

	return is_our_column(rel, expr) && ((Var *) expr)->varattno == *(int *) arg;
}

static void
fdwGetForeignPaths(PlannerInfo *root, RelOptInfo *baserel, Oid foreigntableid)
{
	FdwRel	   *fr = baserel->fdw_private;
	List	   *joins = NIL;
	List	   *done = NIL;
	ListCell   *lc;

	/*
	 * A plain scan; disabled if the wrapper can't scan with the table's own
	 * conditions, so that a parameterized path providing more is used.
	 */
	add_scan_path(root, baserel, baserel->rows, !fr->base_ok, NULL);

	/*
	 * Parameterized paths: join conditions that can be pushed down for each
	 * row of the other side of a nested loop.
	 */
	foreach(lc, baserel->joininfo)
	{
		RestrictInfo *ri = lfirst_node(RestrictInfo, lc);

		if (join_clause_is_movable_to(ri, baserel))
			joins = lappend(joins, ri);
	}
	for (int attno = 1; attno <= fr->natts; attno++)
		joins = list_concat(joins,
							generate_implied_equalities_for_column(root, baserel, ec_member_is_column,
																   &attno, baserel->lateral_referencers));
	foreach(lc, candidates(root, baserel, joins))
	{
		Candidate  *c = lfirst(lc);
		Relids		required_outer = bms_difference(c->rinfo->clause_relids, baserel->relids);
		ParamPathInfo *ppi;
		List	   *rinfos = NIL;
		ListCell   *lc2;
		bool		seen = false;
		double		rows = 0;

		if (bms_is_empty(required_outer) || bms_overlap(required_outer, baserel->lateral_referencers))
			continue;
		foreach(lc2, done)
			seen = seen || bms_equal(lfirst(lc2), required_outer);
		if (seen)
			continue;
		done = lappend(done, required_outer);

		ppi = get_baserel_parampathinfo(root, baserel, required_outer);
		rinfos = list_concat(list_copy(baserel->baserestrictinfo), ppi->ppi_clauses);
		add_scan_path(root, baserel, Max(ppi->ppi_rows, 1),
					  !ask_plan(fr, candidates(root, baserel, rinfos), NULL, &rows),
					  required_outer);
	}
}

static ForeignScan *
fdwGetForeignPlan(PlannerInfo *root, RelOptInfo *baserel, Oid foreigntableid,
				  ForeignPath *best_path, List *tlist, List *scan_clauses, Plan *outer_plan)
{
	FdwRel	   *fr = baserel->fdw_private;
	List	   *cands = candidates(root, baserel, scan_clauses);
	bool	   *pushed = palloc0(sizeof(bool) * (list_length(cands) + 1));
	List	   *exprs = NIL,
			   *filters = NIL;
	double		rows;
	ListCell   *lc;
	int			i = 0;

	(void) ask_plan(fr, cands, pushed, &rows);
	foreach(lc, cands)
	{
		Candidate  *c = lfirst(lc);

		if (pushed[i++])
		{
			exprs = lappend(exprs, c->value);
			filters = lappend(filters, list_make3(makeInteger(c->attno), makeString(pstrdup(c->op)),
												  makeString(pstrdup(c->kind))));
		}
	}
	/* Everything is also checked locally. */
	return make_foreignscan(tlist, extract_actual_clauses(scan_clauses, false), baserel->relid,
							exprs, list_make1(filters), NIL, NIL, outer_plan);
}

/* ---- Execution ----------------------------------------------------- */

typedef struct FdwScan
{
	List	   *items;			/* table description */
	List	   *filters;		/* (attno, op, kind) per fdw_expr */
	List	   *exprstates;
	int			natts;
	FmgrInfo   *infuncs;
	Oid		   *ioparams;
	int32	   *typmods;
	bool	   *dropped;

	int64		handle;			/* Go scan, 0 if none */
	bool		started;
	bool		finished;
	MemoryContext batchcxt;
	Datum	   *elems;
	bool	   *nulls;
	int			nelems;
	int			pos;

	char	   *last_request;
	int			nrequests;
	MemoryContextCallback cleanup;
	bool		cleanup_registered;
} FdwScan;

/* Go scans abandoned by an error: closed at the next scan start. */
static int64 orphans[64];
static int	norphans;

static void
scan_cleanup(void *arg)
{
	FdwScan    *scan = arg;

	if (scan->handle != 0 && norphans < lengthof(orphans))
		orphans[norphans++] = scan->handle;
	scan->handle = 0;
}

static void
fdwBeginForeignScan(ForeignScanState *node, int eflags)
{
	ForeignScan *plan = (ForeignScan *) node->ss.ps.plan;
	Relation	rel = node->ss.ss_currentRelation;
	TupleDesc	desc = RelationGetDescr(rel);
	FdwScan    *scan = palloc0(sizeof(FdwScan));

	node->fdw_state = scan;
	scan->filters = linitial(plan->fdw_private);
	if (eflags & EXEC_FLAG_EXPLAIN_ONLY)
		return;
	scan->items = table_items(RelationGetRelid(rel));
	scan->natts = desc->natts;
	scan->infuncs = palloc0(sizeof(FmgrInfo) * scan->natts);
	scan->ioparams = palloc0(sizeof(Oid) * scan->natts);
	scan->typmods = palloc0(sizeof(int32) * scan->natts);
	scan->dropped = palloc0(sizeof(bool) * scan->natts);
	for (int i = 0; i < scan->natts; i++)
	{
		Form_pg_attribute att = TupleDescAttr(desc, i);
		Oid			infunc;

		scan->dropped[i] = att->attisdropped;
		if (att->attisdropped)
			continue;
		getTypeInputInfo(att->atttypid, &infunc, &scan->ioparams[i]);
		fmgr_info(infunc, &scan->infuncs[i]);
		scan->typmods[i] = att->atttypmod;
	}
	scan->exprstates = ExecInitExprList(plan->fdw_exprs, (PlanState *) node);
	scan->batchcxt = AllocSetContextCreate(node->ss.ps.state->es_query_cxt,
										   "gopgext fdw batch", ALLOCSET_DEFAULT_SIZES);
}

/* Microseconds since the Unix epoch. */
static void
format_time(StringInfo buf, Timestamp ts)
{
	if (TIMESTAMP_IS_NOBEGIN(ts))
		appendStringInfoString(buf, "-inf");
	else if (TIMESTAMP_IS_NOEND(ts))
		appendStringInfoString(buf, "inf");
	else
		appendStringInfo(buf, INT64_FORMAT,
						 ts + (int64) (POSTGRES_EPOCH_JDATE - UNIX_EPOCH_JDATE) * SECS_PER_DAY * USECS_PER_SEC);
}

/*
 * Starts the Go scan: evaluates the pushed-down values (parameters have
 * their current values here).
 */
static void
start_scan(ForeignScanState *node)
{
	FdwScan    *scan = node->fdw_state;
	ExprContext *econtext = node->ss.ps.ps_ExprContext;
	List	   *items;
	ListCell   *lc,
			   *lc2;
	MemoryContext old;
	ArrayType  *arr;

	old = MemoryContextSwitchTo(econtext->ecxt_per_tuple_memory);
	items = list_copy(scan->items);
	forboth(lc, scan->filters, lc2, scan->exprstates)
	{
		List	   *f = lfirst(lc);
		ExprState  *state = lfirst(lc2);
		const char *kind = strVal(lthird(f));
		bool		isnull;
		Datum		d = ExecEvalExpr(state, econtext, &isnull);
		Oid			type = exprType((Node *) state->expr);
		StringInfoData buf;

		initStringInfo(&buf);
		appendStringInfo(&buf, "val=%d\x1f%s\x1f%s\x1f", intVal(linitial(f)) - 1, strVal(lsecond(f)), kind);
		if (isnull)
			appendStringInfoString(&buf, "1\x1f");
		else
		{
			appendStringInfoString(&buf, "0\x1f");
			if (strcmp(kind, "time") == 0)
				format_time(&buf, DatumGetTimestamp(d));
			else if (strcmp(kind, "interval") == 0)
			{
				Interval   *iv = DatumGetIntervalP(d);

				appendStringInfo(&buf, INT64_FORMAT,
								 iv->time + ((int64) iv->month * DAYS_PER_MONTH + iv->day) * USECS_PER_DAY);
			}
			else if (type == FLOAT4OID)
				appendStringInfo(&buf, "%.9g", DatumGetFloat4(d));
			else if (type == FLOAT8OID)
				appendStringInfo(&buf, "%.17g", DatumGetFloat8(d));
			else
			{
				Oid			outfn;
				bool		varlena;

				getTypeOutputInfo(type, &outfn, &varlena);
				appendStringInfoString(&buf, OidOutputFunctionCall(outfn, d));
			}
		}
		items = lappend(items, buf.data);
	}
	for (int i = 0; i < norphans; i++)
		add_item(&items, "orphan=" INT64_FORMAT, orphans[i]);
	norphans = 0;
	arr = items_array(items);
	MemoryContextSwitchTo(old);

	scan->handle = DatumGetInt64(DirectFunctionCall1(go_begin, PointerGetDatum(arr)));
	scan->started = true;
	scan->finished = false;
	scan->nrequests++;
	if (!scan->cleanup_registered)
	{
		scan->cleanup.func = scan_cleanup;
		scan->cleanup.arg = scan;
		MemoryContextRegisterResetCallback(node->ss.ps.state->es_query_cxt, &scan->cleanup);
		scan->cleanup_registered = true;
	}
	old = MemoryContextSwitchTo(node->ss.ps.state->es_query_cxt);
	if (scan->last_request)
		pfree(scan->last_request);
	scan->last_request = TextDatumGetCString(DirectFunctionCall1(go_describe, Int64GetDatum(scan->handle)));
	MemoryContextSwitchTo(old);
	ResetExprContext(econtext);
}

static void
end_scan(FdwScan *scan)
{
	if (scan->handle != 0)
	{
		int64		h = scan->handle;

		scan->handle = 0;
		DirectFunctionCall1(go_end, Int64GetDatum(h));
	}
	scan->started = false;
	scan->nelems = scan->pos = 0;
}

static TupleTableSlot *
fdwIterateForeignScan(ForeignScanState *node)
{
	FdwScan    *scan = node->fdw_state;
	TupleTableSlot *slot = node->ss.ss_ScanTupleSlot;

	ExecClearTuple(slot);
	if (!scan->started)
		start_scan(node);
	if (scan->pos >= scan->nelems)
	{
		MemoryContext old;
		Datum		arr;

		if (scan->finished)
			return slot;
		MemoryContextReset(scan->batchcxt);
		old = MemoryContextSwitchTo(scan->batchcxt);
		arr = DirectFunctionCall2(go_fetch, Int64GetDatum(scan->handle), Int32GetDatum(BATCH_ROWS));
		deconstruct_array(DatumGetArrayTypeP(arr), TEXTOID, -1, false, TYPALIGN_INT,
						  &scan->elems, &scan->nulls, &scan->nelems);
		MemoryContextSwitchTo(old);
		scan->pos = 0;
		if (scan->natts == 0 || scan->nelems % scan->natts != 0)
			elog(ERROR, "foreign data wrapper returned %d values for %d columns", scan->nelems, scan->natts);
		if (scan->nelems == 0)
		{
			scan->finished = true;
			return slot;
		}
	}
	for (int i = 0; i < scan->natts; i++, scan->pos++)
	{
		if (scan->dropped[i] || scan->nulls[scan->pos])
		{
			slot->tts_isnull[i] = true;
			slot->tts_values[i] = (Datum) 0;
		}
		else
		{
			slot->tts_isnull[i] = false;
			slot->tts_values[i] = InputFunctionCall(&scan->infuncs[i],
													TextDatumGetCString(scan->elems[scan->pos]),
													scan->ioparams[i], scan->typmods[i]);
		}
	}
	return ExecStoreVirtualTuple(slot);
}

static void
fdwReScanForeignScan(ForeignScanState *node)
{
	FdwScan    *scan = node->fdw_state;

	/* The next iteration starts a new scan with the current parameters. */
	end_scan(scan);
	scan->finished = false;
}

static void
fdwEndForeignScan(ForeignScanState *node)
{
	FdwScan    *scan = node->fdw_state;

	if (scan && scan->natts > 0)
		end_scan(scan);
}

static void
fdwExplainForeignScan(ForeignScanState *node, ExplainState *es)
{
	ForeignScan *plan = (ForeignScan *) node->ss.ps.plan;
	FdwScan    *scan = node->fdw_state;
	TupleDesc	desc = RelationGetDescr(node->ss.ss_currentRelation);
	List	   *dpcontext = set_deparse_context_plan(es->deparse_cxt, (Plan *) plan, NIL);
	StringInfoData buf;
	ListCell   *lc,
			   *lc2;

	initStringInfo(&buf);
	forboth(lc, linitial(plan->fdw_private), lc2, plan->fdw_exprs)
	{
		List	   *f = lfirst(lc);
		Form_pg_attribute att = TupleDescAttr(desc, intVal(linitial(f)) - 1);

		appendStringInfo(&buf, "%s%s %s %s", buf.len ? ", " : "",
						 quote_identifier(NameStr(att->attname)), strVal(lsecond(f)),
						 deparse_expression(lfirst(lc2), dpcontext, false, false));
	}
	ExplainPropertyText("Remote Filters", buf.len ? buf.data : "(none)", es);
	if (es->analyze && scan && scan->last_request && scan->last_request[0])
	{
		ExplainPropertyText("Remote Request", scan->last_request, es);
		ExplainPropertyInteger("Remote Requests", NULL, scan->nrequests, es);
	}
}

/* ---- IMPORT FOREIGN SCHEMA ----------------------------------------- */

static List *
fdwImportForeignSchema(ImportForeignSchemaStmt *stmt, Oid serverOid)
{
	ForeignServer *server = GetForeignServer(serverOid);
	List	   *items = NIL;
	List	   *result = NIL;
	ArrayType  *arr;
	Datum	   *elems;
	bool	   *nulls;
	int			n;

	add_item(&items, "server=%s", server->servername);
	add_options(&items, "sopt.", server->options);
	add_options(&items, "uopt.", user_mapping_options(serverOid));
	add_item(&items, "remote=%s", stmt->remote_schema);
	add_item(&items, "local=%s", stmt->local_schema);
	arr = DatumGetArrayTypeP(DirectFunctionCall1(go_import, PointerGetDatum(items_array(items))));
	deconstruct_array(arr, TEXTOID, -1, false, TYPALIGN_INT, &elems, &nulls, &n);
	for (int i = 0; i < n; i++)
		result = lappend(result, TextDatumGetCString(elems[i]));
	return result;
}

PG_FUNCTION_INFO_V1(gopgext_fdw_handler);
Datum
gopgext_fdw_handler(PG_FUNCTION_ARGS)
{
	FdwRoutine *r = makeNode(FdwRoutine);

	r->GetForeignRelSize = fdwGetForeignRelSize;
	r->GetForeignPaths = fdwGetForeignPaths;
	r->GetForeignPlan = fdwGetForeignPlan;
	r->BeginForeignScan = fdwBeginForeignScan;
	r->IterateForeignScan = fdwIterateForeignScan;
	r->ReScanForeignScan = fdwReScanForeignScan;
	r->EndForeignScan = fdwEndForeignScan;
	r->ExplainForeignScan = fdwExplainForeignScan;
	r->ImportForeignSchema = fdwImportForeignSchema;
	PG_RETURN_POINTER(r);
}
