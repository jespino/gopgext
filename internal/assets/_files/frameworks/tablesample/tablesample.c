/*
 * C side of gopgext/tablesample: TABLESAMPLE methods implemented by the
 * tablesample.Method the extension registers in Go.
 *
 * Every method's handler function (FUNCTION name(internal) RETURNS
 * tsm_handler) uses the C symbol gopgext_tablesample_handler; the method is
 * the one registered under the handler function's name. The planner and
 * executor callbacks find it again from the TABLESAMPLE clause (its
 * tsmhandler). A scan's Go state is identified by an id kept in tsm_state;
 * the executor's memory context going away (end of the query, or an error)
 * records the id as freed, and Go drops it on its next call.
 */
#include "postgres.h"

#include <math.h>

#include "access/tsmapi.h"
#include "catalog/pg_type.h"
#include "nodes/execnodes.h"
#include "nodes/nodeFuncs.h"
#include "nodes/pathnodes.h"
#include "nodes/plannodes.h"
#include "optimizer/optimizer.h"
#include "parser/parsetree.h"
#include "utils/lsyscache.h"
#include "utils/memutils.h"
#include "utils/sampling.h"
#include "utils/spccache.h"

#include "gopgext.h"

/* Go (gopgext/tablesample), called through gopgext_invoke. */
void		gopgext_go_tsm_describe(struct gopgext_call *call);
void		gopgext_go_tsm_samplesize(struct gopgext_call *call);
void		gopgext_go_tsm_init(struct gopgext_call *call);
void		gopgext_go_tsm_begin(struct gopgext_call *call);
void		gopgext_go_tsm_nextblock(struct gopgext_call *call);
void		gopgext_go_tsm_nexttuple(struct gopgext_call *call);
void		gopgext_go_tsm_end(struct gopgext_call *call);

/* Called by Go. */
int64		gopgext_tsm_freed(uint64 *buf, int64 max);
void		gopgext_tsm_random_init(void *state, uint32 seed);
double		gopgext_tsm_random_fract(void *state);
double		gopgext_tsm_clamp_row_est(double rows);

#define TSM_MAXPARAMS 16

/* What Go says about a method. Mirrored by tablesample.cmethod. */
struct tsm_method
{
	int64		nparams;
	int64		flags;			/* 1 repeatable across queries, 2 across scans, 4 NextBlock */
	uint32		types[TSM_MAXPARAMS];
};

/* Planner estimate. Mirrored by tablesample.csize. */
struct tsm_size
{
	double		pages;			/* in: the relation's; out: to read */
	double		tuples;			/* in: the relation's; out: returned */
	double		random_page_cost;
	int64		nparams;
	Datum		values[TSM_MAXPARAMS];
	uint32		types[TSM_MAXPARAMS];
	bool		known[TSM_MAXPARAMS];	/* the parameter is a known constant */
};

struct tsm_state
{
	uint64		id;				/* of the Go scan */
	bool		ended;
	MemoryContextCallback cb;
};

/* Ids of scans whose memory was freed, not yet dropped by Go. */
static uint64 *freed;
static int	nfreed;
static int	maxfreed;

static void
tsm_reset(void *arg)
{
	struct tsm_state *st = arg;

	if (st->ended)
		return;
	if (nfreed == maxfreed)
	{
		int			n = maxfreed ? maxfreed * 2 : 64;
		uint64	   *p = MemoryContextAllocExtended(TopMemoryContext, sizeof(uint64) * n,
												   MCXT_ALLOC_NO_OOM);

		if (p == NULL)
			return;				/* the Go state leaks; never fail here */
		if (freed)
		{
			memcpy(p, freed, sizeof(uint64) * nfreed);
			pfree(freed);
		}
		freed = p;
		maxfreed = n;
	}
	freed[nfreed++] = st->id;
}

int64
gopgext_tsm_freed(uint64 *buf, int64 max)
{
	int64		n = Min((int64) nfreed, max);

	nfreed -= n;
	memcpy(buf, freed + nfreed, sizeof(uint64) * n);
	return n;
}

/* Calls Go function fn with nargs Datum arguments (none NULL). */
static Datum
call_go(gopgext_gofn fn, int nargs, const Datum *args)
{
	LOCAL_FCINFO(fc, 8);

	Assert(nargs <= 8);
	InitFunctionCallInfoData(*fc, NULL, nargs, InvalidOid, NULL, NULL);
	for (int i = 0; i < nargs; i++)
	{
		fc->args[i].value = args[i];
		fc->args[i].isnull = false;
	}
	return gopgext_invoke(fc, fn);
}

static void
describe(const char *name, struct tsm_method *m)
{
	Datum		args[2];

	memset(m, 0, sizeof(*m));
	args[0] = CStringGetDatum(name);
	args[1] = PointerGetDatum(m);
	call_go(gopgext_go_tsm_describe, 2, args);
}

static char *
method_name(Oid handler)
{
	char	   *name = get_func_name(handler);

	if (name == NULL)
		elog(ERROR, "cache lookup failed for function %u", handler);
	return name;
}

/* The name of the method of a sample scan. */
static char *
scan_method(SampleScanState *node)
{
	return method_name(((SampleScan *) node->ss.ps.plan)->tablesample->tsmhandler);
}

static void
tsm_samplesize(PlannerInfo *root, RelOptInfo *baserel, List *paramexprs,
			   BlockNumber *pages, double *tuples)
{
	RangeTblEntry *rte = planner_rt_fetch(baserel->relid, root);
	struct tsm_size *s = palloc0(sizeof(*s));
	ListCell   *lc;
	Datum		args[2];
	int			i = 0;

	s->pages = baserel->pages;
	s->tuples = baserel->tuples;
	get_tablespace_page_costs(baserel->reltablespace, &s->random_page_cost, NULL);
	foreach(lc, paramexprs)
	{
		Node	   *expr = estimate_expression_value(root, (Node *) lfirst(lc));

		if (i == TSM_MAXPARAMS)
			break;
		s->types[i] = exprType(expr);
		if (IsA(expr, Const) && !((Const *) expr)->constisnull)
		{
			s->values[i] = ((Const *) expr)->constvalue;
			s->known[i] = true;
		}
		i++;
	}
	s->nparams = i;
	args[0] = CStringGetDatum(method_name(rte->tablesample->tsmhandler));
	args[1] = PointerGetDatum(s);
	call_go(gopgext_go_tsm_samplesize, 2, args);
	*pages = (BlockNumber) s->pages;
	*tuples = s->tuples;
}

static void
tsm_init(SampleScanState *node, int eflags)
{
	struct tsm_state *st = palloc0(sizeof(*st));
	Datum		args[1];

	args[0] = CStringGetDatum(scan_method(node));
	st->id = DatumGetUInt64(call_go(gopgext_go_tsm_init, 1, args));
	st->cb.func = tsm_reset;
	st->cb.arg = st;
	MemoryContextRegisterResetCallback(CurrentMemoryContext, &st->cb);
	node->tsm_state = st;
}

static void
tsm_begin(SampleScanState *node, Datum *params, int nparams, uint32 seed)
{
	struct tsm_state *st = node->tsm_state;
	uint32		types[TSM_MAXPARAMS];
	ListCell   *lc;
	int			i = 0;
	Datum		args[5];

	foreach(lc, node->tsmroutine->parameterTypes)
	{
		if (i < TSM_MAXPARAMS)
			types[i++] = lfirst_oid(lc);
	}
	args[0] = UInt64GetDatum(st->id);
	args[1] = PointerGetDatum(params);
	args[2] = PointerGetDatum(types);
	args[3] = Int64GetDatum(Min(nparams, i));
	args[4] = Int64GetDatum(seed);
	call_go(gopgext_go_tsm_begin, 5, args);
}

static BlockNumber
tsm_nextblock(SampleScanState *node, BlockNumber nblocks)
{
	struct tsm_state *st = node->tsm_state;
	Datum		args[3];
	int64		b;

	args[0] = UInt64GetDatum(st->id);
	args[1] = Int64GetDatum(nblocks);
	args[2] = Int64GetDatum((int64) node->donetuples);
	b = DatumGetInt64(call_go(gopgext_go_tsm_nextblock, 3, args));
	return b < 0 ? InvalidBlockNumber : (BlockNumber) b;
}

static OffsetNumber
tsm_nexttuple(SampleScanState *node, BlockNumber blockno, OffsetNumber maxoffset)
{
	struct tsm_state *st = node->tsm_state;
	Datum		args[4];

	args[0] = UInt64GetDatum(st->id);
	args[1] = Int64GetDatum(blockno);
	args[2] = Int64GetDatum(maxoffset);
	args[3] = Int64GetDatum((int64) node->donetuples);
	return (OffsetNumber) DatumGetInt64(call_go(gopgext_go_tsm_nexttuple, 4, args));
}

static void
tsm_end(SampleScanState *node)
{
	struct tsm_state *st = node->tsm_state;
	Datum		args[1];

	if (st == NULL || st->ended)
		return;
	st->ended = true;
	args[0] = UInt64GetDatum(st->id);
	call_go(gopgext_go_tsm_end, 1, args);
}

PG_FUNCTION_INFO_V1(gopgext_tablesample_handler);

/* name(internal) RETURNS tsm_handler: the method registered as name. */
Datum
gopgext_tablesample_handler(PG_FUNCTION_ARGS)
{
	TsmRoutine *tsm = makeNode(TsmRoutine);
	struct tsm_method m;

	describe(method_name(fcinfo->flinfo->fn_oid), &m);
	tsm->parameterTypes = NIL;
	for (int i = 0; i < m.nparams && i < TSM_MAXPARAMS; i++)
		tsm->parameterTypes = lappend_oid(tsm->parameterTypes, m.types[i]);
	tsm->repeatable_across_queries = (m.flags & 1) != 0;
	tsm->repeatable_across_scans = (m.flags & 2) != 0;
	tsm->SampleScanGetSampleSize = tsm_samplesize;
	tsm->InitSampleScan = tsm_init;
	tsm->BeginSampleScan = tsm_begin;
	tsm->NextSampleBlock = (m.flags & 4) ? tsm_nextblock : NULL;
	tsm->NextSampleTuple = tsm_nexttuple;
	tsm->EndSampleScan = tsm_end;
	PG_RETURN_POINTER(tsm);
}

/* ---- PostgreSQL's sampling random generator (utils/sampling.h) ---------- */

#if PG_VERSION_NUM >= 150000
StaticAssertDecl(sizeof(pg_prng_state) <= 16, "random state size");
#else
StaticAssertDecl(sizeof(SamplerRandomState) <= 16, "random state size");
#endif

void
gopgext_tsm_random_init(void *state, uint32 seed)
{
#if PG_VERSION_NUM >= 150000
	sampler_random_init_state(seed, (pg_prng_state *) state);
#else
	sampler_random_init_state(seed, (unsigned short *) state);
#endif
}

double
gopgext_tsm_random_fract(void *state)
{
#if PG_VERSION_NUM >= 150000
	return sampler_random_fract((pg_prng_state *) state);
#else
	return sampler_random_fract((unsigned short *) state);
#endif
}

double
gopgext_tsm_clamp_row_est(double rows)
{
	return clamp_row_est(rows);
}
