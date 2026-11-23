/*
 * C side of gopgext/intercept: UPDATEs handed to Go instead of executed.
 *
 * Planner hook: an UPDATE of a table whose policy (asked to Go once, then
 * cached until the table's relcache entry is invalidated) has a column
 * group with every column the UPDATE sets is planned as a SELECT of the
 * rows it would update: their table (tableoid), primary key and new
 * values. It doesn't lock or write rows. The PlannedStmt keeps
 * commandType CMD_UPDATE (the command tag is "UPDATE n"); the top target
 * list carries what the executor needs in its column names
 * ("ix:<extension>:<root>:<dedupe>:<tag>", "ixk", "ixv:<attnum>"), so cached
 * plans work. With a keyed policy, WHERE pk = value (constants,
 * parameters...) isn't even read: the SELECT computes the values only.
 *
 * ExecutorRun hook: runs such a plan, passes its rows to Go in batches,
 * and sets es_processed to the number of rows Go counted: the command tag,
 * PQcmdTuples, GET DIAGNOSTICS ROW_COUNT and FOUND report them as if they
 * had been written.
 *
 * Writes of watched columns by statements that aren't intercepted are
 * reported too: plain UPDATEs get a hidden RETURNING tableoid, pk... (the
 * client gets no rows) whose rows go to Go; MERGE, INSERT ... ON CONFLICT
 * DO UPDATE, UPDATEs with RETURNING and data-modifying CTEs are reported
 * per table (the columns, not the rows).
 */
#include "postgres.h"

#include "access/genam.h"
#include "access/htup_details.h"
#include "access/stratnum.h"
#include "access/sysattr.h"
#include "access/table.h"
#include "catalog/pg_class.h"
#include "catalog/pg_index.h"
#include "catalog/pg_trigger.h"
#include "catalog/pg_type.h"
#include "commands/trigger.h"
#include "executor/executor.h"
#include "executor/instrument.h"
#include "miscadmin.h"
#include "nodes/makefuncs.h"
#include "nodes/nodeFuncs.h"
#include "nodes/plannodes.h"
#include "optimizer/clauses.h"
#include "optimizer/optimizer.h"
#include "optimizer/planner.h"
#include "parser/parse_relation.h"
#include "parser/parsetree.h"
#include "utils/builtins.h"
#include "utils/guc.h"
#include "utils/hsearch.h"
#include "utils/inval.h"
#include "utils/lsyscache.h"
#include "utils/memutils.h"
#include "utils/rel.h"
#include "utils/relcache.h"
#include "utils/syscache.h"

#include "gopgext.h"

void		gopgext_go_intercept_table(struct gopgext_call *call);
void		gopgext_go_intercept_rows(struct gopgext_call *call);
void		gopgext_go_intercept_table_written(struct gopgext_call *call);

void		gopgext_intercept_install(const char *setting, int64 max_image);
bool		gopgext_intercept_suspend(bool on);

#define MARK_KEY "ixk"
#define MARK_VAL "ixv:"

/* Rows passed to Go at a time. */
#define BATCH_ROWS 1000

static planner_hook_type prev_planner;
static ExecutorRun_hook_type prev_executor_run;

static char *setting;			/* a boolean GUC: intercept while on (NULL: always) */
static int64 max_image;			/* longer values are passed as text */
static char *mark_rows;			/* "ix:<extension>:" */
static char *mark_write;		/* "ixw:<extension>:" */
static bool suspended;			/* Go's own writes: plan UPDATEs normally */

bool
gopgext_intercept_suspend(bool on)
{
	bool		was = suspended;

	suspended = on;
	return was;
}

/* ---- Table policies ------------------------------------------------------- */

/* What Go said about a table, and what the planner needs from the catalog. */
typedef struct TablePolicy
{
	Oid			relid;			/* hash key */
	bool		valid;
	bool		watched;		/* Go gave a policy */
	bool		interceptable;	/* it has groups, and nothing forbids it */
	bool		keyed;
	bool		has_trigger;	/* intercept only while this trigger fires */
	char		tgenabled;
	bool		has_children;	/* inheritance parent (not partitioned) */
	List	   *groups;			/* Bitmapsets of attnums */
	Bitmapset  *grouped;		/* all of them */
	Bitmapset  *watch;
	char	   *tag;
	int			npk;
	AttrNumber	pk[INDEX_MAX_KEYS];
	Oid			pktype[INDEX_MAX_KEYS];
	int32		pktypmod[INDEX_MAX_KEYS];
	Oid			pkcoll[INDEX_MAX_KEYS];
	Oid			pkeq[INDEX_MAX_KEYS];	/* equality operators (InvalidOid: none usable) */
} TablePolicy;

/* A policy from Go. Mirrored by intercept.cpolicy. */
struct intercept_policy
{
	int64		ok;				/* a policy */
	int64		ncols;
	int16	   *cols;			/* groups, each ended by 0 */
	int64		nwatch;
	int16	   *watch;
	int64		keyed;
	const char *trigger;		/* or NULL */
	const char *tag;
	int64		tag_len;
};

static HTAB *policies;

static void
invalidate_policy(Datum arg, Oid relid)
{
	HASH_SEQ_STATUS seq;
	TablePolicy *p;

	if (policies == NULL)
		return;
	if (OidIsValid(relid))
	{
		p = hash_search(policies, &relid, HASH_FIND, NULL);
		if (p)
			p->valid = false;
		return;
	}
	hash_seq_init(&seq, policies);
	while ((p = hash_seq_search(&seq)) != NULL)
		p->valid = false;
}

static void
load_policy(TablePolicy *p, Relation rel)
{
	MemoryContext old;
	struct intercept_policy gp;
	bool		blocked;
	Oid			pkindex;
	HeapTuple	tup;
	Trigger    *ours = NULL;

	list_free_deep(p->groups);
	bms_free(p->grouped);
	bms_free(p->watch);
	if (p->tag)
		pfree(p->tag);
	p->groups = NIL;
	p->grouped = NULL;
	p->watch = NULL;
	p->tag = NULL;
	p->watched = false;
	p->interceptable = false;
	p->has_trigger = false;
	p->npk = 0;

	if (rel->rd_rel->relkind != RELKIND_RELATION && rel->rd_rel->relkind != RELKIND_PARTITIONED_TABLE)
		return;
	/* Rows are identified by their primary key, in index order. */
	pkindex = RelationGetPrimaryKeyIndex(rel, false);
	if (!OidIsValid(pkindex))
		return;
	tup = SearchSysCache1(INDEXRELID, ObjectIdGetDatum(pkindex));
	if (!HeapTupleIsValid(tup))
		return;
	{
		Form_pg_index idx = (Form_pg_index) GETSTRUCT(tup);

		for (int i = 0; i < idx->indnkeyatts; i++)
		{
			p->pk[i] = idx->indkey.values[i];
			get_atttypetypmodcoll(RelationGetRelid(rel), p->pk[i],
								  &p->pktype[i], &p->pktypmod[i], &p->pkcoll[i]);
		}
		p->npk = idx->indnkeyatts;
	}
	ReleaseSysCache(tup);
	/*
	 * The key's equality operators, for UPDATEs that give the key
	 * (WHERE pk = value): the index's own, so the value means the same
	 * row. Not with nondeterministic collations (equal values, different
	 * text).
	 */
	{
		Relation	idxrel = index_open(pkindex, AccessShareLock);

		for (int i = 0; i < p->npk; i++)
		{
			p->pkeq[i] = InvalidOid;
			if (idxrel->rd_opcintype[i] == p->pktype[i] &&
				(!OidIsValid(p->pkcoll[i]) || get_collation_isdeterministic(p->pkcoll[i])))
				p->pkeq[i] = get_opfamily_member(idxrel->rd_opfamily[i], p->pktype[i], p->pktype[i],
												 BTEqualStrategyNumber);
		}
		index_close(idxrel, AccessShareLock);
	}

	/* Ask Go. */
	memset(&gp, 0, sizeof(gp));
	{
		LOCAL_FCINFO(fc, 2);

		InitFunctionCallInfoData(*fc, NULL, 2, InvalidOid, NULL, NULL);
		fc->args[0].value = ObjectIdGetDatum(RelationGetRelid(rel));
		fc->args[0].isnull = false;
		fc->args[1].value = PointerGetDatum(&gp);
		fc->args[1].isnull = false;
		gopgext_invoke(fc, gopgext_go_intercept_table);
	}
	if (!gp.ok)
		return;

	/* Copied before Go runs again (its memory may be collected then). */
	old = MemoryContextSwitchTo(CacheMemoryContext);
	p->watched = true;
	p->keyed = gp.keyed != 0;
	if (gp.tag_len > 0)
		p->tag = pnstrdup(gp.tag, gp.tag_len);
	for (int i = 0; i < gp.nwatch; i++)
		if (gp.watch[i] > 0)
			p->watch = bms_add_member(p->watch, gp.watch[i]);
	{
		Bitmapset  *group = NULL;

		for (int i = 0; i < gp.ncols; i++)
		{
			if (gp.cols[i] > 0)
			{
				group = bms_add_member(group, gp.cols[i]);
				continue;
			}
			if (group != NULL)
			{
				p->groups = lappend(p->groups, group);
				p->grouped = bms_union(p->grouped, group);
			}
			group = NULL;
		}
		if (group != NULL)
		{
			p->groups = lappend(p->groups, group);
			p->grouped = bms_union(p->grouped, group);
		}
	}

	/*
	 * Intercepted UPDATEs don't run row-level security policies or
	 * triggers: tables with either aren't intercepted (the policy's own
	 * trigger excepted, which decides when they are), nor inheritance
	 * parents with children (their rows).
	 */
	blocked = rel->rd_rel->relrowsecurity;
	p->has_children = rel->rd_rel->relkind == RELKIND_RELATION && rel->rd_rel->relhassubclass;
	if (rel->trigdesc != NULL)
	{
		for (int i = 0; i < rel->trigdesc->numtriggers; i++)
		{
			Trigger    *t = &rel->trigdesc->triggers[i];

			if (gp.trigger != NULL && strcmp(t->tgname, gp.trigger) == 0)
				ours = t;
			else if (TRIGGER_FOR_UPDATE(t->tgtype) && t->tgenabled != TRIGGER_DISABLED && !t->tgisinternal)
				blocked = true;
		}
	}
	if (gp.trigger != NULL)
	{
		if (ours == NULL)
			blocked = true;
		else
		{
			p->has_trigger = true;
			p->tgenabled = ours->tgenabled;
		}
	}
	p->interceptable = p->groups != NIL && !blocked;
	MemoryContextSwitchTo(old);
}

static TablePolicy *
policy_for(Oid relid)
{
	TablePolicy *p;
	bool		found;

	if (policies == NULL)
	{
		HASHCTL		ctl;

		memset(&ctl, 0, sizeof(ctl));
		ctl.keysize = sizeof(Oid);
		ctl.entrysize = sizeof(TablePolicy);
		ctl.hcxt = CacheMemoryContext;
		policies = hash_create("gopgext intercept tables", 64, &ctl, HASH_ELEM | HASH_BLOBS | HASH_CONTEXT);
		CacheRegisterRelcacheCallback(invalidate_policy, (Datum) 0);
	}
	p = hash_search(policies, &relid, HASH_ENTER, &found);
	if (!found)
	{
		p->valid = false;
		p->groups = NIL;
		p->grouped = NULL;
		p->watch = NULL;
		p->tag = NULL;
	}
	if (!p->valid)
	{
		/* Locked by the parser. */
		Relation	rel = table_open(relid, NoLock);

		/*
		 * Valid from the start: an invalidation while loading (Go looks
		 * at the catalog) makes it load again next time.
		 */
		p->valid = true;
		PG_TRY();
		{
			load_policy(p, rel);
		}
		PG_CATCH();
		{
			p->valid = false;
			table_close(rel, NoLock);
			PG_RE_THROW();
		}
		PG_END_TRY();
		table_close(rel, NoLock);
	}
	return p;
}

/*
 * The attribute number in leaf (a partition of root) of column num of
 * root: partitions can have other attribute numbers (dropped columns).
 */
static AttrNumber
leaf_attnum(Oid root, Oid leaf, AttrNumber num)
{
	if (root == leaf)
		return num;
	return get_attnum(leaf, get_attname(root, num, false));
}

/* ---- Planning --------------------------------------------------------- */

static bool
has_current_of(Node *node, void *context)
{
	if (node == NULL)
		return false;
	if (IsA(node, CurrentOfExpr))
		return true;
	return expression_tree_walker(node, has_current_of, context);
}

static bool
setting_on(void)
{
	const char *v;

	if (setting == NULL)
		return true;
	v = GetConfigOption(setting, true, false);
	return v == NULL || strcmp(v, "on") == 0;
}

static bool
trigger_fires(char tgenabled)
{
	if (tgenabled == TRIGGER_DISABLED)
		return false;
	if (SessionReplicationRole == SESSION_REPLICATION_ROLE_REPLICA)
		return tgenabled == TRIGGER_FIRES_ON_REPLICA || tgenabled == TRIGGER_FIRES_ALWAYS;
	return tgenabled != TRIGGER_FIRES_ON_REPLICA;
}

/*
 * For a keyed policy: if the UPDATE's WHERE is exactly "pk = value" for
 * each primary key column (values without columns, volatile functions or
 * subqueries: constants, parameters...), with the key's equality operator,
 * sets keys to the values and returns true.
 */
static bool
key_values(Query *parse, TablePolicy *p, Expr **keys)
{
	FromExpr   *jt = parse->jointree;
	List	   *conds;
	ListCell   *lc;
	Node	   *from;

	if (!p->keyed || parse->hasSubLinks || jt == NULL || jt->quals == NULL ||
		list_length(jt->fromlist) != 1)
		return false;
	from = linitial(jt->fromlist);
	if (!IsA(from, RangeTblRef) || ((RangeTblRef *) from)->rtindex != parse->resultRelation)
		return false;
	conds = make_ands_implicit((Expr *) jt->quals);
	if (list_length(conds) != p->npk)
		return false;
	memset(keys, 0, sizeof(Expr *) * p->npk);
	foreach(lc, conds)
	{
		OpExpr	   *op = (OpExpr *) lfirst(lc);
		Node	   *l,
				   *r,
				   *other;
		Var		   *var;
		int			i;

		if (!IsA(op, OpExpr) || list_length(op->args) != 2)
			return false;
		l = linitial(op->args);
		r = lsecond(op->args);
		if (IsA(l, Var))
			var = (Var *) l, other = r;
		else if (IsA(r, Var))
			var = (Var *) r, other = l;
		else
			return false;
		if (var->varno != parse->resultRelation || var->varlevelsup != 0)
			return false;
		for (i = 0; i < p->npk && p->pk[i] != var->varattno; i++)
			;
		if (i == p->npk || keys[i] != NULL || !OidIsValid(p->pkeq[i]) || op->opno != p->pkeq[i] ||
			exprType(other) != p->pktype[i] ||
			contain_var_clause(other) || contain_volatile_functions(other))
			return false;
		keys[i] = (Expr *) other;
	}
	return true;
}

static char *
rows_mark(TablePolicy *p, Oid root, bool dedupe)
{
	return psprintf("%s%u:%d:%s", mark_rows, root, dedupe ? 1 : 0, p->tag ? p->tag : "");
}

/*
 * If parse is an UPDATE to intercept, turns it into the SELECT to run
 * instead (in place: the planner may scribble on its input) and returns
 * true.
 */
static bool
intercept_query(Query *parse)
{
	RangeTblEntry *rte;
	TablePolicy *p;
	Bitmapset  *set = NULL;
	Bitmapset  *refs = NULL;
	bool		matched = false;
	ListCell   *lc;
	List	   *tlist = NIL;
	AttrNumber	resno = 1;

	if (parse->commandType != CMD_UPDATE || parse->resultRelation <= 0 ||
		parse->returningList != NIL || parse->hasModifyingCTE ||
		parse->hasTargetSRFs || parse->utilityStmt != NULL ||
		parse->targetList == NIL)
		return false;
	rte = rt_fetch(parse->resultRelation, parse->rtable);
	if (rte->rtekind != RTE_RELATION ||
		(rte->relkind != RELKIND_RELATION && rte->relkind != RELKIND_PARTITIONED_TABLE))
		return false;
	p = policy_for(rte->relid);
	if (!p->interceptable || (p->has_trigger && !trigger_fires(p->tgenabled)) ||
		(rte->inh && p->has_children))
		return false;

	/* The columns the UPDATE sets: one of the groups must have all of them. */
	foreach(lc, parse->targetList)
	{
		TargetEntry *tle = lfirst_node(TargetEntry, lc);

		if (tle->resjunk)
			return false;		/* multiple-column assignments */
		set = bms_add_member(set, tle->resno);
	}
	foreach(lc, p->groups)
		if (bms_is_subset(set, (Bitmapset *) lfirst(lc)))
			matched = true;
	if (!matched)
		return false;
	/* SET col = col + 1 reads a column Go may hold a newer value of. */
	foreach(lc, parse->targetList)
		pull_varattnos((Node *) lfirst_node(TargetEntry, lc)->expr, parse->resultRelation, &refs);
	for (int a = -1; (a = bms_next_member(refs, a)) >= 0;)
		if (bms_is_member(a + FirstLowInvalidHeapAttributeNumber, p->grouped))
			return false;
	if (has_current_of((Node *) parse->jointree, NULL))
		return false;

	/*
	 * WHERE pk = value, new values that don't depend on the row, and a
	 * keyed policy: SELECT relid, the key values and the new values,
	 * without reading the table (still locked, and UPDATE permission is
	 * still checked). The row is counted whether it exists or not.
	 */
	if (rte->relkind == RELKIND_RELATION && bms_is_empty(refs) &&
		!contain_var_clause((Node *) parse->targetList))
	{
		Expr	   *keys[INDEX_MAX_KEYS];

		if (key_values(parse, p, keys))
		{
			tlist = lappend(tlist,
							makeTargetEntry((Expr *) makeConst(OIDOID, -1, InvalidOid, sizeof(Oid),
															   ObjectIdGetDatum(rte->relid), false, true),
											resno++, rows_mark(p, rte->relid, false), false));
			for (int i = 0; i < p->npk; i++)
				tlist = lappend(tlist, makeTargetEntry(keys[i], resno++, pstrdup(MARK_KEY), false));
			foreach(lc, parse->targetList)
			{
				TargetEntry *tle = lfirst_node(TargetEntry, lc);

				tlist = lappend(tlist,
								makeTargetEntry(tle->expr, resno++, psprintf(MARK_VAL "%d", tle->resno), false));
			}
			parse->commandType = CMD_SELECT;
			parse->resultRelation = 0;
			parse->rowMarks = NIL;
			parse->jointree = makeFromExpr(NIL, NULL);
			parse->targetList = tlist;
			return true;
		}
	}

	/*
	 * SELECT tableoid, pk..., new values... with the UPDATE's FROM and
	 * WHERE. With other tables in FROM, a row can be matched several times
	 * (an UPDATE updates it once): Go counts distinct rows then.
	 */
	tlist = lappend(tlist,
					makeTargetEntry((Expr *) makeVar(parse->resultRelation, TableOidAttributeNumber,
													 OIDOID, -1, InvalidOid, 0),
									resno++, rows_mark(p, rte->relid, list_length(parse->rtable) > 1),
									false));
	for (int i = 0; i < p->npk; i++)
		tlist = lappend(tlist,
						makeTargetEntry((Expr *) makeVar(parse->resultRelation, p->pk[i], p->pktype[i],
														 p->pktypmod[i], p->pkcoll[i], 0),
										resno++, pstrdup(MARK_KEY), false));
	foreach(lc, parse->targetList)
	{
		TargetEntry *tle = lfirst_node(TargetEntry, lc);

		tlist = lappend(tlist,
						makeTargetEntry(tle->expr, resno++, psprintf(MARK_VAL "%d", tle->resno), false));
	}
	parse->commandType = CMD_SELECT;
	parse->resultRelation = 0;
	parse->rowMarks = NIL;
	parse->targetList = tlist;
	return true;
}

/*
 * If parse is an UPDATE executed normally that sets watched columns, adds
 * RETURNING tableoid, pk... to it (the executor hook reads them, the
 * client doesn't get them): Go is told which rows it wrote. Returns true
 * if it did.
 */
static bool
watch_query(Query *parse)
{
	RangeTblEntry *rte;
	TablePolicy *p;
	StringInfoData cols;
	ListCell   *lc;
	List	   *returning = NIL;
	AttrNumber	resno = 1;

	if (parse->commandType != CMD_UPDATE || parse->resultRelation <= 0 ||
		parse->returningList != NIL || parse->utilityStmt != NULL)
		return false;
	rte = rt_fetch(parse->resultRelation, parse->rtable);
	if (rte->rtekind != RTE_RELATION ||
		(rte->relkind != RELKIND_RELATION && rte->relkind != RELKIND_PARTITIONED_TABLE))
		return false;
	p = policy_for(rte->relid);
	if (!p->watched || p->watch == NULL)
		return false;
	initStringInfo(&cols);
	foreach(lc, parse->targetList)
	{
		TargetEntry *tle = lfirst_node(TargetEntry, lc);

		if (!tle->resjunk && bms_is_member(tle->resno, p->watch))
			appendStringInfo(&cols, "%s%d", cols.len ? "," : "", tle->resno);
	}
	if (cols.len == 0)
		return false;
	returning = lappend(returning,
						makeTargetEntry((Expr *) makeVar(parse->resultRelation, TableOidAttributeNumber,
														 OIDOID, -1, InvalidOid, 0),
										resno++,
										psprintf("%s%u:%s:%s", mark_write, rte->relid, cols.data,
												 p->tag ? p->tag : ""),
										false));
	for (int i = 0; i < p->npk; i++)
		returning = lappend(returning,
							makeTargetEntry((Expr *) makeVar(parse->resultRelation, p->pk[i], p->pktype[i],
															 p->pktypmod[i], p->pkcoll[i], 0),
											resno++, pstrdup(MARK_KEY), false));
	parse->returningList = returning;
	return true;
}

static PlannedStmt *
intercept_planner(Query *parse, const char *query_string, int cursor_options,
				  ParamListInfo bound_params)
{
	bool		can_set_tag = parse->canSetTag;
	bool		intercepted = false;
	bool		watched = false;
	PlannedStmt *stmt;

	if (!suspended && parse->commandType == CMD_UPDATE)
	{
		if (setting_on())
			intercepted = intercept_query(parse);
		if (!intercepted)
			watched = watch_query(parse);
	}
	/* The executor hook runs intercepted plans itself: no parallel workers. */
	if (intercepted)
		cursor_options &= ~CURSOR_OPT_PARALLEL_OK;
	stmt = prev_planner ?
		prev_planner(parse, query_string, cursor_options, bound_params) :
		standard_planner(parse, query_string, cursor_options, bound_params);
	if (intercepted)
	{
		stmt->commandType = CMD_UPDATE;
		stmt->hasReturning = false;
		stmt->canSetTag = can_set_tag;
	}
	if (watched)
		stmt->hasReturning = false; /* the client gets no rows */
	return stmt;
}

/* ---- Execution --------------------------------------------------------- */

/*
 * A value for Go: NULL (len < 0), the binary image of a Datum of type typ
 * (len bytes at ptr, or the Datum itself if by value: ptr NULL), or text
 * (text set: too long, or a type without a fixed image). Mirrored by
 * intercept.ccell.
 */
struct cell
{
	const char *ptr;
	int64		len;
	int64		typ;
	Datum		datum;			/* by-value Datums */
	int64		attnum;			/* value: its column in the row's table (a partition's may differ) */
	int64		text;
};

/*
 * Rows for Go: per row, the table, nkeys keys (text) and nvals values.
 * Mirrored by intercept.cbatch.
 */
struct batch
{
	int64		nrows;
	int64		ncols;
	int64		nkeys;
	int64		root;
	int64		dedupe;			/* count distinct rows */
	const char *tag;
	struct cell *cells;			/* nrows * ncols */
	int64		first;
	int64		last;
	int64		write;			/* rows written (watched columns: NULL values) */
	int64		count;			/* out (last batch): rows matched */
};

/* Attribute numbers of the value columns in a partition (run_rows). */
struct leafmap
{
	Oid			leaf;
	int16	   *attnums;		/* per column */
};

/* A column of the plan: its type and output function. */
struct column
{
	Oid			typ;
	int16		typlen;
	bool		typbyval;
	FmgrInfo	out;
};

static bool
has_mark(Plan *plan, const char *mark)
{
	TargetEntry *tle;

	if (plan == NULL || plan->targetlist == NIL)
		return false;
	tle = linitial_node(TargetEntry, plan->targetlist);
	return tle->resname != NULL && strncmp(tle->resname, mark, strlen(mark)) == 0;
}

static bool
is_rows_plan(QueryDesc *qd)
{
	PlannedStmt *stmt = qd->plannedstmt;

	return stmt->commandType == CMD_UPDATE && stmt->planTree != NULL &&
		!IsA(stmt->planTree, ModifyTable) && has_mark(stmt->planTree, mark_rows);
}

static bool
is_write_plan(QueryDesc *qd)
{
	PlannedStmt *stmt = qd->plannedstmt;

	return stmt->planTree != NULL && IsA(stmt->planTree, ModifyTable) &&
		has_mark(stmt->planTree, mark_write);
}

static void
call_go(struct batch *b)
{
	LOCAL_FCINFO(fc, 1);

	InitFunctionCallInfoData(*fc, NULL, 1, InvalidOid, NULL, NULL);
	fc->args[0].value = PointerGetDatum(b);
	fc->args[0].isnull = false;
	gopgext_invoke(fc, gopgext_go_intercept_rows);
	b->first = 0;
	b->nrows = 0;
}

static void
text_cell(struct cell *cell, struct column *col, Datum d)
{
	cell->ptr = OutputFunctionCall(&col->out, d);
	cell->len = strlen(cell->ptr);
	cell->typ = col->typ;
	cell->text = 1;
}

static void
value_cell(struct cell *cell, struct column *col, Datum d)
{
	cell->typ = col->typ;
	cell->text = 0;
	if (col->typbyval)
	{
		cell->datum = d;
		cell->ptr = NULL;		/* the array may move */
		cell->len = sizeof(Datum);
		return;
	}
	if (col->typlen > 0)
	{
		cell->ptr = DatumGetPointer(d);
		cell->len = col->typlen;
	}
	else if (col->typlen == -1)
	{
		struct varlena *v = pg_detoast_datum_packed((struct varlena *) DatumGetPointer(d));

		cell->ptr = (const char *) v;
		cell->len = VARSIZE_ANY(v);
	}
	else
	{
		cell->ptr = DatumGetCString(d);
		cell->len = strlen(cell->ptr) + 1;
	}
	if (max_image > 0 && cell->len > max_image)
		text_cell(cell, col, d);
}

/*
 * Runs an intercepted plan (its rows are the rows the UPDATE matches), or
 * a watched one (an UPDATE whose RETURNING rows are the rows it wrote),
 * and passes the rows to Go in batches.
 */
static void
run_rows(QueryDesc *qd, bool write)
{
	EState	   *estate = qd->estate;
	PlanState  *ps = qd->planstate;
	List	   *tlist = qd->plannedstmt->planTree->targetlist;
	MemoryContext old = MemoryContextSwitchTo(estate->es_query_cxt);
	TupleDesc	desc = ExecGetResultType(ps);
	struct batch b;
	struct column *cols;
	int16	   *kinds;			/* per column: -1 table, 0 key, else the attnum */
	MemoryContext rowctx = NULL;
	int			capacity = 4;	/* rows; grows up to BATCH_ROWS */
	ListCell   *lc;
	int			i = 0;
	int			nvals = 0;
	int16	   *write_cols = NULL;
	int			nwrite = 0;
	Oid			root = InvalidOid;
	List	   *leafmaps = NIL;
	int			ncells;

	if (qd->totaltime)
		InstrStartNode(qd->totaltime);
	memset(&b, 0, sizeof(b));
	b.write = write;
	b.tag = "";
	/* The columns written become value columns (NULL values) for Go. */
	if (write)
	{
		TargetEntry *tle = linitial_node(TargetEntry, tlist);
		char	   *p = pstrdup(tle->resname + strlen(mark_write));
		char	   *colstr;
		char	   *tag;

		root = (Oid) strtoul(p, &colstr, 10);
		colstr++;
		tag = strchr(colstr, ':');
		*tag++ = '\0';
		b.tag = tag;
		write_cols = palloc(sizeof(int16) * (strlen(colstr) / 2 + 2));
		for (char *tok = strtok(colstr, ","); tok; tok = strtok(NULL, ","))
			write_cols[nwrite++] = (int16) atoi(tok);
	}
	b.ncols = list_length(tlist) + nwrite;
	kinds = palloc(sizeof(int16) * b.ncols);
	cols = palloc0(sizeof(struct column) * b.ncols);
	foreach(lc, tlist)
	{
		TargetEntry *tle = lfirst_node(TargetEntry, lc);
		Oid			fn;
		bool		varlena;

		if (i == 0)
		{
			kinds[i] = -1;
			if (!write)
			{
				char	   *rest;

				root = (Oid) strtoul(tle->resname + strlen(mark_rows), &rest, 10);
				b.dedupe = rest[1] == '1';
				b.tag = rest + 3;
			}
		}
		else if (strcmp(tle->resname, MARK_KEY) == 0)
		{
			kinds[i] = 0;
			b.nkeys++;
		}
		else
		{
			kinds[i] = (int16) atoi(tle->resname + strlen(MARK_VAL));
			nvals++;
		}
		cols[i].typ = TupleDescAttr(desc, i)->atttypid;
		get_typlenbyval(cols[i].typ, &cols[i].typlen, &cols[i].typbyval);
		getTypeOutputInfo(cols[i].typ, &fn, &varlena);
		fmgr_info(fn, &cols[i].out);
		i++;
	}
	for (int j = 0; j < nwrite; j++)
		kinds[i + j] = write_cols[j];
	ncells = i;					/* columns of the plan's rows */
	b.root = root;
	b.first = 1;
	b.cells = palloc(sizeof(struct cell) * b.ncols * capacity);
	if (write)
		estate->es_processed = 0;	/* counted by ModifyTable */
	for (;;)
	{
		TupleTableSlot *slot;
		struct cell *row;
		int16	   *map = NULL;
		Oid			leaf;

		ResetPerTupleExprContext(estate);
		MemoryContextSwitchTo(estate->es_query_cxt);
		slot = ExecProcNode(ps);
		if (TupIsNull(slot))
			break;
		slot_getallattrs(slot);
		if (b.nrows == capacity)
		{
			capacity = Min(capacity * 4, BATCH_ROWS);
			b.cells = repalloc(b.cells, sizeof(struct cell) * b.ncols * capacity);
		}
		/* A partition's attribute numbers (cached for the statement). */
		leaf = DatumGetObjectId(slot->tts_values[0]);
		if (OidIsValid(root) && leaf != root && (nvals > 0 || nwrite > 0))
		{
			foreach(lc, leafmaps)
				if (((struct leafmap *) lfirst(lc))->leaf == leaf)
					map = ((struct leafmap *) lfirst(lc))->attnums;
			if (map == NULL)
			{
				struct leafmap *m = palloc(sizeof(*m));

				m->leaf = leaf;
				m->attnums = palloc(sizeof(int16) * b.ncols);
				for (int c = 0; c < b.ncols; c++)
					m->attnums[c] = kinds[c] > 0 ? leaf_attnum(root, leaf, kinds[c]) : kinds[c];
				leafmaps = lappend(leafmaps, m);
				map = m->attnums;
			}
		}
		/* Text values and detoasted copies: freed after each batch of a big UPDATE. */
		if (rowctx)
			MemoryContextSwitchTo(rowctx);
		row = &b.cells[b.nrows * b.ncols];
		for (int c = 0; c < b.ncols; c++)
		{
			row[c].attnum = map ? map[c] : kinds[c];
			if (c >= ncells || slot->tts_isnull[c])
			{
				row[c].ptr = NULL;
				row[c].len = -1;
				row[c].typ = c < ncells ? cols[c].typ : 0;
				row[c].text = 0;
			}
			else if (kinds[c] > 0)
				value_cell(&row[c], &cols[c], slot->tts_values[c]);
			else if (kinds[c] < 0)
			{
				/* tableoid: the Datum */
				row[c].datum = slot->tts_values[c];
				row[c].ptr = NULL;
				row[c].len = sizeof(Datum);
				row[c].typ = OIDOID;
				row[c].text = 0;
			}
			else
				text_cell(&row[c], &cols[c], slot->tts_values[c]);
		}
		if (++b.nrows == BATCH_ROWS)
		{
			call_go(&b);
			if (rowctx)
				MemoryContextReset(rowctx);
			else
				rowctx = AllocSetContextCreate(estate->es_query_cxt, "gopgext intercept rows",
											   ALLOCSET_DEFAULT_SIZES);
		}
	}
	b.last = 1;
	call_go(&b);
	if (!write)
		estate->es_processed = (uint64) b.count;
	MemoryContextSwitchTo(old);
	if (qd->totaltime)
		InstrStopNode(qd->totaltime, estate->es_processed);
}

/* A table whose watched columns a statement wrote. Mirrored by intercept.ctablewrite. */
struct table_write
{
	int64		rel;
	int64		root;
	int64		ncols;
	int16	   *cols;
	const char *tag;
};

/*
 * Statements that write watched columns but can't tell which rows
 * (RETURNING, data-modifying CTEs, MERGE, INSERT ... ON CONFLICT DO
 * UPDATE): tell Go the tables and columns.
 */
static void
report_tables(QueryDesc *qd)
{
	PlannedStmt *stmt = qd->plannedstmt;
	List	   *plans = list_make1(stmt->planTree);
	ListCell   *lc;

	plans = list_concat(plans, stmt->subplans);
	foreach(lc, plans)
	{
		ModifyTable *mt = (ModifyTable *) lfirst(lc);
		RangeTblEntry *rte;
		Bitmapset  *updated;
		TablePolicy *p;
		Bitmapset  *cols = NULL;
		ListCell   *rl;

		if (mt == NULL || !IsA(mt, ModifyTable) || ((Plan *) mt == stmt->planTree && is_write_plan(qd)))
			continue;
		if (!(mt->operation == CMD_UPDATE ||
#if PG_VERSION_NUM >= 150000
			  mt->operation == CMD_MERGE ||
#endif
			  (mt->operation == CMD_INSERT && mt->onConflictAction == ONCONFLICT_UPDATE)))
			continue;
		rte = rt_fetch(mt->rootRelation ? mt->rootRelation : linitial_int(mt->resultRelations),
					   stmt->rtable);
		if (rte->rtekind != RTE_RELATION)
			continue;
#if PG_VERSION_NUM >= 160000
		if (rte->perminfoindex == 0)
			continue;
		updated = getRTEPermissionInfo(stmt->permInfos, rte)->updatedCols;
#else
		updated = rte->updatedCols;
#endif
		p = policy_for(rte->relid);
		if (!p->watched || p->watch == NULL)
			continue;
		for (int a = -1; (a = bms_next_member(updated, a)) >= 0;)
			if (bms_is_member(a + FirstLowInvalidHeapAttributeNumber, p->watch))
				cols = bms_add_member(cols, a + FirstLowInvalidHeapAttributeNumber);
		if (cols == NULL)
			continue;
		foreach(rl, mt->resultRelations)
		{
			Oid			leaf = rt_fetch(lfirst_int(rl), stmt->rtable)->relid;
			struct table_write tw;
			int16		nums[MaxHeapAttributeNumber];

			tw.rel = leaf;
			tw.root = rte->relid;
			tw.tag = p->tag ? p->tag : "";
			tw.ncols = 0;
			for (int a = -1; (a = bms_next_member(cols, a)) >= 0;)
				nums[tw.ncols++] = leaf_attnum(rte->relid, leaf, a);
			tw.cols = nums;
			{
				LOCAL_FCINFO(fc, 1);

				InitFunctionCallInfoData(*fc, NULL, 1, InvalidOid, NULL, NULL);
				fc->args[0].value = PointerGetDatum(&tw);
				fc->args[0].isnull = false;
				gopgext_invoke(fc, gopgext_go_intercept_table_written);
			}
		}
	}
}

static void
#if PG_VERSION_NUM >= 180000
intercept_executor_run(QueryDesc *qd, ScanDirection dir, uint64 count)
#else
intercept_executor_run(QueryDesc *qd, ScanDirection dir, uint64 count, bool once)
#endif
{
	if (is_rows_plan(qd))
	{
		run_rows(qd, false);
		return;
	}
	if (is_write_plan(qd))
	{
		run_rows(qd, true);
		return;
	}
	if (!suspended && policies != NULL &&
		(qd->plannedstmt->commandType != CMD_SELECT || qd->plannedstmt->hasModifyingCTE))
		report_tables(qd);
#if PG_VERSION_NUM >= 180000
	if (prev_executor_run)
		prev_executor_run(qd, dir, count);
	else
		standard_ExecutorRun(qd, dir, count);
#else
	if (prev_executor_run)
		prev_executor_run(qd, dir, count, once);
	else
		standard_ExecutorRun(qd, dir, count, once);
#endif
}

void
gopgext_intercept_install(const char *setting_name, int64 max_image_len)
{
	static bool installed;

	if (installed)
		return;
	installed = true;
	if (setting_name != NULL && setting_name[0] != '\0')
		setting = MemoryContextStrdup(TopMemoryContext, setting_name);
	max_image = max_image_len;
	mark_rows = MemoryContextStrdup(TopMemoryContext, psprintf("ix:%s:", gopgext_name()));
	mark_write = MemoryContextStrdup(TopMemoryContext, psprintf("ixw:%s:", gopgext_name()));
	prev_planner = planner_hook;
	planner_hook = intercept_planner;
	prev_executor_run = ExecutorRun_hook;
	ExecutorRun_hook = intercept_executor_run;
}
