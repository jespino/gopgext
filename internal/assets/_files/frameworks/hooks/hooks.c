/*
 * C side of gopgext/hooks: PostgreSQL's query processing hooks (post parse
 * analysis, planner, executor, utility), reported to Go as events.
 *
 * The hooks are installed when Go registers callbacks (hooks.Register, in
 * main). They chain to the previous hooks (or the standard functions), keep
 * the statement nesting level, and call Go only for the events it
 * registered for: a decision ("measure this statement?") before planning,
 * executing or running a utility statement, and the measurements (time,
 * rows, buffers, WAL, JIT) after it. PostgreSQL code runs in C only: errors
 * of the planner or executor never cross Go frames.
 *
 * The execution decision is taken before ExecutorStart, so that Go can ask
 * for per-node instrumentation; Go can get the EXPLAIN output of a measured
 * execution when it ends (gopgext_hooks_explain), before ExecutorEnd.
 */
#include "postgres.h"

#include "access/parallel.h"
#include "commands/explain.h"
#if PG_VERSION_NUM >= 180000
#include "commands/explain_format.h"
#include "commands/explain_state.h"
#endif
#include "executor/executor.h"
#include "executor/instrument.h"
#include "jit/jit.h"
#include "optimizer/planner.h"
#include "parser/analyze.h"
#include "parser/scanner.h"
#include "parser/scansup.h"
#include "tcop/cmdtag.h"
#include "tcop/utility.h"
#include "utils/memutils.h"
#if PG_VERSION_NUM >= 160000
#include "nodes/queryjumble.h"
#else
#include "utils/queryjumble.h"
#endif

#include "gopgext.h"

void		gopgext_go_hooks_event(struct gopgext_call *call);

/* Event kinds (hooks.go). */
enum
{
	HOOKS_PARSED,
	HOOKS_PLAN,
	HOOKS_PLANNED,
	HOOKS_EXECUTE,
	HOOKS_EXECUTED,
	HOOKS_UTILITY,
	HOOKS_UTILITY_DONE,
};

/* Resource usage of a statement. Mirrored by hooks.cusage. */
struct hooks_usage
{
	double		time;			/* milliseconds */
	int64		rows;
	int64		shared_blks_hit;
	int64		shared_blks_read;
	int64		shared_blks_dirtied;
	int64		shared_blks_written;
	int64		local_blks_hit;
	int64		local_blks_read;
	int64		local_blks_dirtied;
	int64		local_blks_written;
	int64		temp_blks_read;
	int64		temp_blks_written;
	double		shared_blk_read_time;
	double		shared_blk_write_time;
	double		local_blk_read_time;
	double		local_blk_write_time;
	double		temp_blk_read_time;
	double		temp_blk_write_time;
	int64		wal_records;
	int64		wal_fpi;
	uint64		wal_bytes;
	int64		wal_buffers_full;
	int64		jit;			/* JIT compilation was used */
	int64		jit_functions;
	double		jit_generation_time;
	double		jit_inlining_time;
	double		jit_optimization_time;
	double		jit_emission_time;
	double		jit_deform_time;
	int64		parallel_workers_to_launch;
	int64		parallel_workers_launched;
};

/* An event. Mirrored by hooks.cevent. */
struct hooks_event
{
	int64		kind;
	int64		nesting;		/* statement nesting level (0: top level) */
	int64		query_id;		/* HOOKS_PARSED: Go may change it */
	const char *source;			/* the source text (may hold more statements) */
	int64		location;		/* the statement in source, trimmed */
	int64		length;
	const char *command;		/* command tag, e.g. "SELECT" */
	JumbleState *jstate;		/* HOOKS_PARSED: constants, or NULL */
	uint64		mask;			/* the registrations measuring the statement */
	struct hooks_usage usage;
	int64		executor_nesting;	/* executions in progress (executor events) */
	int64		instrument;		/* HOOKS_EXECUTE: hooks.Instrumentation wanted */
	QueryDesc  *qd;				/* HOOKS_EXECUTED: the execution */
};

/* Options of gopgext_hooks_explain. Mirrored by hooks.cexplain. */
struct hooks_explain
{
	int64		format;			/* 0 text, 1 xml, 2 json, 3 yaml */
	int64		flags;			/* EXPLAIN_* */
	int64		parameters;		/* max bytes of parameter values; 0: none, -1: all */
};

#define EXPLAIN_ANALYZE		(1 << 0)
#define EXPLAIN_TIMING		(1 << 1)
#define EXPLAIN_BUFFERS		(1 << 2)
#define EXPLAIN_WAL			(1 << 3)
#define EXPLAIN_TRIGGERS	(1 << 4)
#define EXPLAIN_VERBOSE		(1 << 5)
#define EXPLAIN_SETTINGS	(1 << 6)
#define EXPLAIN_NO_COSTS	(1 << 7)
#define EXPLAIN_QUERY_TEXT	(1 << 8)

/* A constant of a parsed statement. Mirrored by hooks.cconstant. */
struct hooks_constant
{
	int64		location;		/* relative to the statement */
	int64		length;			/* -1: a duplicate (to skip) */
	int64		flags;			/* 1: a squashed list, 2: an external parameter */
};

void		gopgext_hooks_install(int64 kinds);
bool		gopgext_hooks_parallel_worker(void);
void		gopgext_hooks_enable_query_id(void);
ErrorData  *gopgext_hooks_constants(struct hooks_event *ev, struct hooks_constant **out,
									int64 *n, int64 *highest_param, bool *squashed_lists);
ErrorData  *gopgext_hooks_explain(struct hooks_event *ev, struct hooks_explain *o,
								  char **out, int64 *len);

static int64 hook_kinds;		/* the events Go wants (1 << kind) */
static bool installed;
static int	nesting_level = 0;
static int	executor_level = 0;	/* ExecutorRun/ExecutorFinish in progress */

static post_parse_analyze_hook_type prev_post_parse_analyze_hook;
static planner_hook_type prev_planner_hook;
static ExecutorStart_hook_type prev_ExecutorStart;
static ExecutorRun_hook_type prev_ExecutorRun;
static ExecutorFinish_hook_type prev_ExecutorFinish;
static ExecutorEnd_hook_type prev_ExecutorEnd;
static ProcessUtility_hook_type prev_ProcessUtility;

static bool
wants(int kind)
{
	return (hook_kinds & (INT64CONST(1) << kind)) != 0 && gopgext_loaded();
}

/* Fills the statement fields of an event. */
static void
event_init(struct hooks_event *ev, int kind, int64 query_id, const char *source,
		   int location, int length, CommandTag tag)
{
	memset(ev, 0, sizeof(*ev));
	ev->kind = kind;
	ev->nesting = nesting_level;
	ev->query_id = query_id;
	ev->source = source;
	(void) CleanQuerytext(source, &location, &length);
	ev->location = location;
	ev->length = length;
	ev->command = GetCommandTagName(tag);
}

/* Calls Go with an event; returns ev->mask as Go left it. */
static uint64
call_go(struct hooks_event *ev)
{
	LOCAL_FCINFO(fc, 1);

	InitFunctionCallInfoData(*fc, NULL, 1, InvalidOid, NULL, NULL);
	fc->args[0].value = PointerGetDatum(ev);
	fc->args[0].isnull = false;
	gopgext_invoke(fc, gopgext_go_hooks_event);
	return ev->mask;
}

static void
buffer_usage(struct hooks_usage *u, const BufferUsage *b)
{
	u->shared_blks_hit = b->shared_blks_hit;
	u->shared_blks_read = b->shared_blks_read;
	u->shared_blks_dirtied = b->shared_blks_dirtied;
	u->shared_blks_written = b->shared_blks_written;
	u->local_blks_hit = b->local_blks_hit;
	u->local_blks_read = b->local_blks_read;
	u->local_blks_dirtied = b->local_blks_dirtied;
	u->local_blks_written = b->local_blks_written;
	u->temp_blks_read = b->temp_blks_read;
	u->temp_blks_written = b->temp_blks_written;
#if PG_VERSION_NUM >= 170000
	u->shared_blk_read_time = INSTR_TIME_GET_MILLISEC(b->shared_blk_read_time);
	u->shared_blk_write_time = INSTR_TIME_GET_MILLISEC(b->shared_blk_write_time);
	u->local_blk_read_time = INSTR_TIME_GET_MILLISEC(b->local_blk_read_time);
	u->local_blk_write_time = INSTR_TIME_GET_MILLISEC(b->local_blk_write_time);
#else
	/* Shared and local blocks together (blk_read_time). */
	u->shared_blk_read_time = INSTR_TIME_GET_MILLISEC(b->blk_read_time);
	u->shared_blk_write_time = INSTR_TIME_GET_MILLISEC(b->blk_write_time);
#endif
#if PG_VERSION_NUM >= 150000
	u->temp_blk_read_time = INSTR_TIME_GET_MILLISEC(b->temp_blk_read_time);
	u->temp_blk_write_time = INSTR_TIME_GET_MILLISEC(b->temp_blk_write_time);
#endif
}

static void
wal_usage(struct hooks_usage *u, const WalUsage *w)
{
	u->wal_records = w->wal_records;
	u->wal_fpi = w->wal_fpi;
	u->wal_bytes = w->wal_bytes;
#if PG_VERSION_NUM >= 180000
	u->wal_buffers_full = w->wal_buffers_full;
#endif
}

/* Usage counters before a statement, and the differences after it. */
struct usage_start
{
	instr_time	start;
	BufferUsage buf;
	WalUsage	wal;
};

static void
usage_begin(struct usage_start *s)
{
	s->buf = pgBufferUsage;
	s->wal = pgWalUsage;
	INSTR_TIME_SET_CURRENT(s->start);
}

static void
usage_end(struct usage_start *s, struct hooks_usage *u)
{
	instr_time	duration;
	BufferUsage buf;
	WalUsage	wal;

	INSTR_TIME_SET_CURRENT(duration);
	INSTR_TIME_SUBTRACT(duration, s->start);
	u->time = INSTR_TIME_GET_MILLISEC(duration);
	memset(&buf, 0, sizeof(buf));
	BufferUsageAccumDiff(&buf, &pgBufferUsage, &s->buf);
	memset(&wal, 0, sizeof(wal));
	WalUsageAccumDiff(&wal, &pgWalUsage, &s->wal);
	buffer_usage(u, &buf);
	wal_usage(u, &wal);
}

/* ---- Parse analysis ------------------------------------------------- */

static void
hooks_post_parse_analyze(ParseState *pstate, Query *query, JumbleState *jstate)
{
	struct hooks_event ev;

	if (prev_post_parse_analyze_hook)
		prev_post_parse_analyze_hook(pstate, query, jstate);
	if (!wants(HOOKS_PARSED) || pstate->p_sourcetext == NULL)
		return;
	event_init(&ev, HOOKS_PARSED, query->queryId, pstate->p_sourcetext,
			   query->stmt_location, query->stmt_len, CreateCommandTag((Node *) query));
	ev.jstate = jstate;
	call_go(&ev);
	query->queryId = ev.query_id;
}

/* Sorts constants by location. */
static int
constant_cmp(const void *a, const void *b)
{
	int			l = ((const LocationLen *) a)->location;
	int			r = ((const LocationLen *) b)->location;

	return (l > r) - (l < r);
}

/*
 * The constants of the statement of a HOOKS_PARSED event with their
 * lengths (the parser only records locations), in a palloc'd array
 * (*out, *n entries). Lengths come from the core lexer, as
 * pg_stat_statements does; a negative constant includes its '-'.
 */
ErrorData *
gopgext_hooks_constants(struct hooks_event *ev, struct hooks_constant **out,
						int64 *n, int64 *highest_param, bool *squashed_lists)
{
	GOPGEXT_GUARD({
		JumbleState *jstate = ev->jstate;
		const char *query = ev->source + ev->location;
		int			query_loc = ev->location;
		int			count = jstate ? jstate->clocations_count : 0;
		LocationLen *locs;
		core_yyscan_t yyscanner;
		core_yy_extra_type yyextra;
		core_YYSTYPE yylval;
		YYLTYPE		yylloc;
		int			tok = 1;

		*n = count;
		*out = NULL;
		*highest_param = jstate ? jstate->highest_extern_param_id : 0;
#if PG_VERSION_NUM >= 180000
		*squashed_lists = jstate ? jstate->has_squashed_lists : false;
#else
		*squashed_lists = false;
#endif
		if (count == 0)
			break;
		locs = palloc(sizeof(LocationLen) * count);
		memcpy(locs, jstate->clocations, sizeof(LocationLen) * count);
		if (count > 1)
			qsort(locs, count, sizeof(LocationLen), constant_cmp);

		yyscanner = scanner_init(query, &yyextra, &ScanKeywords, ScanKeywordTokens);
#if PG_VERSION_NUM < 190000
		yyextra.escape_string_warning = false;
#endif
		for (int i = 0; i < count && tok != 0; i++)
		{
			int			loc;

			/* Only the first constant at a location counts. */
			if (i > 0 && locs[i].location == locs[i - 1].location)
			{
				locs[i].length = -1;
				continue;
			}
#if PG_VERSION_NUM >= 180000
			/* A squashed list: the parser recorded its length. */
			if (locs[i].squashed)
				continue;
#endif
			loc = locs[i].location - query_loc;
			for (;;)
			{
				tok = core_yylex(&yylval, &yylloc, yyscanner);
				if (tok == 0)
					break;
				if (yylloc >= loc)
				{
					if (query[loc] == '-')
					{
						tok = core_yylex(&yylval, &yylloc, yyscanner);
						if (tok == 0)
							break;
					}
					/* flex puts a NUL after the current token in scanbuf */
					locs[i].length = strlen(yyextra.scanbuf + loc);
					break;
				}
			}
		}
		scanner_finish(yyscanner);

		*out = palloc(sizeof(struct hooks_constant) * count);
		for (int i = 0; i < count; i++)
		{
			(*out)[i].location = locs[i].location - query_loc;
			(*out)[i].length = locs[i].length;
			(*out)[i].flags = 0;
#if PG_VERSION_NUM >= 180000
			if (locs[i].squashed)
				(*out)[i].flags |= 1;
			if (locs[i].extern_param)
				(*out)[i].flags |= 2;
#endif
		}
		pfree(locs);
	});
}

/* ---- Planner -------------------------------------------------------- */

static PlannedStmt *
hooks_planner(Query *parse, const char *query_string, int cursorOptions,
			  ParamListInfo boundParams
#if PG_VERSION_NUM >= 190000
			  ,ExplainState *es
#endif
)
{
	PlannedStmt *result;
	struct hooks_event ev;
	struct usage_start start;
	uint64		mask = 0;

	if (wants(HOOKS_PLAN) && query_string != NULL)
	{
		event_init(&ev, HOOKS_PLAN, parse->queryId, query_string, parse->stmt_location,
				   parse->stmt_len, CreateCommandTag((Node *) parse));
		mask = call_go(&ev);
	}
	if (mask)
		usage_begin(&start);

	/* Statements run while planning (e.g. functions folded) are nested. */
	nesting_level++;
	PG_TRY();
	{
#if PG_VERSION_NUM >= 190000
		if (prev_planner_hook)
			result = prev_planner_hook(parse, query_string, cursorOptions, boundParams, es);
		else
			result = standard_planner(parse, query_string, cursorOptions, boundParams, es);
#else
		if (prev_planner_hook)
			result = prev_planner_hook(parse, query_string, cursorOptions, boundParams);
		else
			result = standard_planner(parse, query_string, cursorOptions, boundParams);
#endif
	}
	PG_FINALLY();
	{
		nesting_level--;
	}
	PG_END_TRY();

	if (mask)
	{
		usage_end(&start, &ev.usage);
		ev.kind = HOOKS_PLANNED;
		ev.mask = mask;
		call_go(&ev);
	}
	return result;
}

/* ---- Executor ------------------------------------------------------- */

/*
 * An execution Go measures: found again in ExecutorEnd. Allocated in the
 * query's memory context, whose deletion unlinks it (when the query fails,
 * ExecutorEnd isn't called).
 */
struct hooks_exec
{
	QueryDesc  *qd;
	int			nesting;
	int			executor_nesting;
	uint64		mask;
	struct hooks_exec *next;
	MemoryContextCallback cb;
};

static struct hooks_exec *execs;

static void
exec_unlink(struct hooks_exec *e)
{
	for (struct hooks_exec **p = &execs; *p; p = &(*p)->next)
	{
		if (*p == e)
		{
			*p = e->next;
			return;
		}
	}
}

static void
exec_forget(void *arg)
{
	exec_unlink(arg);
}

/* Instrumentation options from hooks.Instrumentation. */
static int
instrument_options(int64 i)
{
	int			opts = 0;

	if (i & 1)
		opts |= INSTRUMENT_ROWS;
	if (i & 2)
		opts |= INSTRUMENT_TIMER;
	if (i & 4)
		opts |= INSTRUMENT_BUFFERS;
	if (i & 8)
		opts |= INSTRUMENT_WAL;
	return opts;
}

static void
hooks_ExecutorStart(QueryDesc *qd, int eflags)
{
	struct hooks_event ev;
	uint64		mask = 0;

	if (wants(HOOKS_EXECUTE) && qd->sourceText != NULL)
	{
		event_init(&ev, HOOKS_EXECUTE, qd->plannedstmt->queryId, qd->sourceText,
				   qd->plannedstmt->stmt_location, qd->plannedstmt->stmt_len,
				   CreateCommandTag((Node *) qd->plannedstmt));
		ev.executor_nesting = executor_level;
		mask = call_go(&ev);
		/* Per-node instrumentation is set up by ExecutorStart. */
		if (mask && (eflags & EXEC_FLAG_EXPLAIN_ONLY) == 0)
			qd->instrument_options |= instrument_options(ev.instrument);
	}

	if (prev_ExecutorStart)
		prev_ExecutorStart(qd, eflags);
	else
		standard_ExecutorStart(qd, eflags);

	if (mask)
	{
		MemoryContext old = MemoryContextSwitchTo(qd->estate->es_query_cxt);
		struct hooks_exec *e = palloc0(sizeof(*e));

		/* Summary instrumentation of the whole query (time, buffers, WAL). */
		if (qd->totaltime == NULL)
			qd->totaltime = InstrAlloc(1, INSTRUMENT_ALL, false);
		e->qd = qd;
		e->nesting = nesting_level;
		e->executor_nesting = executor_level;
		e->mask = mask;
		e->cb.func = exec_forget;
		e->cb.arg = e;
		MemoryContextRegisterResetCallback(qd->estate->es_query_cxt, &e->cb);
		e->next = execs;
		execs = e;
		MemoryContextSwitchTo(old);
	}
}

#if PG_VERSION_NUM >= 180000
static void
hooks_ExecutorRun(QueryDesc *qd, ScanDirection direction, uint64 count)
#else
static void
hooks_ExecutorRun(QueryDesc *qd, ScanDirection direction, uint64 count, bool execute_once)
#endif
{
	nesting_level++;
	executor_level++;
	PG_TRY();
	{
#if PG_VERSION_NUM >= 180000
		if (prev_ExecutorRun)
			prev_ExecutorRun(qd, direction, count);
		else
			standard_ExecutorRun(qd, direction, count);
#else
		if (prev_ExecutorRun)
			prev_ExecutorRun(qd, direction, count, execute_once);
		else
			standard_ExecutorRun(qd, direction, count, execute_once);
#endif
	}
	PG_FINALLY();
	{
		nesting_level--;
		executor_level--;
	}
	PG_END_TRY();
}

static void
hooks_ExecutorFinish(QueryDesc *qd)
{
	nesting_level++;
	executor_level++;
	PG_TRY();
	{
		if (prev_ExecutorFinish)
			prev_ExecutorFinish(qd);
		else
			standard_ExecutorFinish(qd);
	}
	PG_FINALLY();
	{
		nesting_level--;
		executor_level--;
	}
	PG_END_TRY();
}

static void
hooks_ExecutorEnd(QueryDesc *qd)
{
	struct hooks_exec *e;

	for (e = execs; e && e->qd != qd; e = e->next)
		;
	if (e && qd->totaltime && gopgext_loaded())
	{
		struct hooks_event ev;
		EState	   *estate = qd->estate;

		exec_unlink(e);
		InstrEndLoop(qd->totaltime);
		event_init(&ev, HOOKS_EXECUTED, qd->plannedstmt->queryId, qd->sourceText,
				   qd->plannedstmt->stmt_location, qd->plannedstmt->stmt_len,
				   CreateCommandTag((Node *) qd->plannedstmt));
		ev.nesting = e->nesting;
		ev.executor_nesting = e->executor_nesting;
		ev.mask = e->mask;
		ev.qd = qd;
		ev.usage.time = qd->totaltime->total * 1000.0;
#if PG_VERSION_NUM >= 160000
		ev.usage.rows = estate->es_total_processed;
#else
		ev.usage.rows = estate->es_processed;
#endif
		buffer_usage(&ev.usage, &qd->totaltime->bufusage);
		wal_usage(&ev.usage, &qd->totaltime->walusage);
		if (estate->es_jit)
		{
			JitInstrumentation *j = &estate->es_jit->instr;

			ev.usage.jit = 1;
			ev.usage.jit_functions = j->created_functions;
			ev.usage.jit_generation_time = INSTR_TIME_GET_MILLISEC(j->generation_counter);
			ev.usage.jit_inlining_time = INSTR_TIME_GET_MILLISEC(j->inlining_counter);
			ev.usage.jit_optimization_time = INSTR_TIME_GET_MILLISEC(j->optimization_counter);
			ev.usage.jit_emission_time = INSTR_TIME_GET_MILLISEC(j->emission_counter);
#if PG_VERSION_NUM >= 170000
			ev.usage.jit_deform_time = INSTR_TIME_GET_MILLISEC(j->deform_counter);
#endif
		}
#if PG_VERSION_NUM >= 180000
		ev.usage.parallel_workers_to_launch = estate->es_parallel_workers_to_launch;
		ev.usage.parallel_workers_launched = estate->es_parallel_workers_launched;
#endif
		call_go(&ev);
	}

	if (prev_ExecutorEnd)
		prev_ExecutorEnd(qd);
	else
		standard_ExecutorEnd(qd);
}

/*
 * The EXPLAIN output of the execution of a HOOKS_EXECUTED event (*out, *len,
 * in the query's memory context), as auto_explain prints it: the query text
 * and parameters (if asked), the plan, triggers (with ANALYZE) and the JIT
 * summary (with costs). ANALYZE requires per-node instrumentation.
 */
ErrorData *
gopgext_hooks_explain(struct hooks_event *ev, struct hooks_explain *o, char **out, int64 *len)
{
	GOPGEXT_GUARD({
		QueryDesc  *qd = ev->qd;
		MemoryContext old = MemoryContextSwitchTo(qd->estate->es_query_cxt);
		ExplainState *es = NewExplainState();
		int64		f = o->flags;

		es->analyze = (f & EXPLAIN_ANALYZE) && qd->instrument_options != 0;
		es->verbose = (f & EXPLAIN_VERBOSE) != 0;
		es->costs = (f & EXPLAIN_NO_COSTS) == 0;
		es->buffers = es->analyze && (f & EXPLAIN_BUFFERS);
		es->wal = es->analyze && (f & EXPLAIN_WAL);
		es->timing = es->analyze && (f & EXPLAIN_TIMING);
		es->summary = es->analyze;
		es->settings = (f & EXPLAIN_SETTINGS) != 0;
		switch (o->format)
		{
			case 1:
				es->format = EXPLAIN_FORMAT_XML;
				break;
			case 2:
				es->format = EXPLAIN_FORMAT_JSON;
				break;
			case 3:
				es->format = EXPLAIN_FORMAT_YAML;
				break;
			default:
				es->format = EXPLAIN_FORMAT_TEXT;
		}

		ExplainBeginOutput(es);
		if (f & EXPLAIN_QUERY_TEXT)
			ExplainQueryText(es, qd);
#if PG_VERSION_NUM >= 160000
		if (o->parameters != 0)
			ExplainQueryParameters(es, qd->params, (int) o->parameters);
#endif
		ExplainPrintPlan(es, qd);
		if (es->analyze && (f & EXPLAIN_TRIGGERS))
			ExplainPrintTriggers(es, qd);
		if (es->costs)
			ExplainPrintJITSummary(es, qd);
		ExplainEndOutput(es);
		*out = es->str->data;
		*len = es->str->len;
		MemoryContextSwitchTo(old);
	});
}

/* ---- Utility statements --------------------------------------------- */

static void
hooks_ProcessUtility(PlannedStmt *pstmt, const char *queryString, bool readOnlyTree,
					 ProcessUtilityContext context, ParamListInfo params,
					 QueryEnvironment *queryEnv, DestReceiver *dest, QueryCompletion *qc)
{
	Node	   *parsetree = pstmt->utilityStmt;
	struct hooks_event ev;
	struct usage_start start;
	uint64		mask = 0;

	/*
	 * EXECUTE and PREPARE don't add a nesting level: the statement EXECUTE
	 * runs, and the one PREPARE parses, are at the level of the EXECUTE or
	 * PREPARE (pg_stat_statements' rule).
	 */
	bool		bump = !IsA(parsetree, ExecuteStmt) && !IsA(parsetree, PrepareStmt);

	if (wants(HOOKS_UTILITY) && queryString != NULL)
	{
		event_init(&ev, HOOKS_UTILITY, pstmt->queryId, queryString, pstmt->stmt_location,
				   pstmt->stmt_len, CreateCommandTag(parsetree));
		mask = call_go(&ev);
	}
	if (mask)
	{
		/*
		 * The statement is measured as a whole: the executor doesn't measure
		 * an optimizable statement it contains (EXPLAIN, DECLARE CURSOR,
		 * CREATE TABLE AS...) a second time.
		 */
		pstmt->queryId = INT64CONST(0);
		usage_begin(&start);
	}

	if (bump)
		nesting_level++;
	PG_TRY();
	{
		if (prev_ProcessUtility)
			prev_ProcessUtility(pstmt, queryString, readOnlyTree, context, params,
								queryEnv, dest, qc);
		else
			standard_ProcessUtility(pstmt, queryString, readOnlyTree, context, params,
									queryEnv, dest, qc);
	}
	PG_FINALLY();
	{
		if (bump)
			nesting_level--;
	}
	PG_END_TRY();

	/* pstmt may have been freed (e.g. by ROLLBACK): only ev is used now. */
	if (mask)
	{
		usage_end(&start, &ev.usage);
		/* Rows of COPY, FETCH, CREATE TABLE AS, SELECT INTO, REFRESH. */
		if (qc && (qc->commandTag == CMDTAG_COPY || qc->commandTag == CMDTAG_FETCH ||
				   qc->commandTag == CMDTAG_SELECT ||
				   qc->commandTag == CMDTAG_REFRESH_MATERIALIZED_VIEW))
			ev.usage.rows = qc->nprocessed;
		ev.kind = HOOKS_UTILITY_DONE;
		ev.mask = mask;
		call_go(&ev);
	}
}

/* ---- Called by Go ----------------------------------------------------- */

/* Installs the hooks (once) and adds kinds (1 << kind) to the events Go wants. */
void
gopgext_hooks_install(int64 kinds)
{
	if (!installed)
	{
		installed = true;
		prev_post_parse_analyze_hook = post_parse_analyze_hook;
		post_parse_analyze_hook = hooks_post_parse_analyze;
		prev_planner_hook = planner_hook;
		planner_hook = hooks_planner;
		prev_ExecutorStart = ExecutorStart_hook;
		ExecutorStart_hook = hooks_ExecutorStart;
		prev_ExecutorRun = ExecutorRun_hook;
		ExecutorRun_hook = hooks_ExecutorRun;
		prev_ExecutorFinish = ExecutorFinish_hook;
		ExecutorFinish_hook = hooks_ExecutorFinish;
		prev_ExecutorEnd = ExecutorEnd_hook;
		ExecutorEnd_hook = hooks_ExecutorEnd;
		prev_ProcessUtility = ProcessUtility_hook;
		ProcessUtility_hook = hooks_ProcessUtility;
	}
	hook_kinds |= kinds;
}

bool
gopgext_hooks_parallel_worker(void)
{
	return IsParallelWorker();
}

void
gopgext_hooks_enable_query_id(void)
{
	EnableQueryId();
}
