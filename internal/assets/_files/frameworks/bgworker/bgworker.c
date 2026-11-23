/*
 * C side of gopgext/bgworker: background worker processes whose main
 * function is Go.
 *
 * Go registers workers while the library loads in the postmaster
 * (shared_preload_libraries); each is a static background worker whose
 * entry point, gopgext_export_bgworker_main, runs the Go main function of
 * the worker (its index is the worker's main argument). The forked worker
 * inherits the Go heap of the postmaster, so the registered functions are
 * there.
 *
 * A worker waits on its latch (bgworker.Context.Wait), set by other
 * backends (Handle.Wake) through a slot in shared memory holding its
 * PGPROC, and runs SQL in transactions it starts and ends itself
 * (Context.Transaction). SIGTERM asks it to stop: Wait returns false.
 * SIGHUP reloads the configuration on the next Wait.
 */
#include "postgres.h"

#include "access/xact.h"
#include "miscadmin.h"
#include "pgstat.h"
#include "postmaster/bgworker.h"
#include "postmaster/interrupt.h"
#include "storage/ipc.h"
#include "storage/latch.h"
#include "storage/proc.h"
#include "storage/shmem.h"
#include "utils/guc.h"
#include "utils/memutils.h"
#include "utils/snapmgr.h"
#include "utils/wait_event.h"

#include "gopgext.h"

void		gopgext_go_bgworker_main(struct gopgext_call *call);

PGDLLEXPORT void gopgext_export_bgworker_main(Datum arg);

ErrorData  *gopgext_bgworker_register(int64 index, const char *name, const char *type,
									  int64 start, int64 restart, bool database);
ErrorData  *gopgext_bgworker_connect(const char *database, const char *user);
bool		gopgext_bgworker_wait(int64 timeout_ms);
ErrorData  *gopgext_bgworker_begin(const char *activity);
void	   *gopgext_bgworker_end(bool commit);
void		gopgext_bgworker_free_error(void *edata);
void		gopgext_bgworker_wake(int64 index);
int64		gopgext_bgworker_pid(int64 index);
bool		gopgext_bgworker_shutdown_requested(void);
bool		gopgext_bgworker_attached(void);

/* Registered workers, at most this many per library. */
#define MAX_WORKERS 64

/* The shared state of a worker. */
struct worker_slot
{
	pid_t		pid;			/* 0 when not running */
	PGPROC	   *proc;			/* its PGPROC (same address in every process) */
};

static struct worker_slot *slots;
static int	nregistered;
static int	my_index = -1;
static MemoryContext worker_context;

#if PG_VERSION_NUM >= 150000
static shmem_request_hook_type prev_shmem_request_hook;
#endif
static shmem_startup_hook_type prev_shmem_startup_hook;

static char *
slots_name(void)
{
	return psprintf("gopgext %s bgworkers", gopgext_name());
}

#if PG_VERSION_NUM >= 150000
static void
bgworker_shmem_request(void)
{
	if (prev_shmem_request_hook)
		prev_shmem_request_hook();
	RequestAddinShmemSpace(MAXALIGN(sizeof(struct worker_slot) * MAX_WORKERS));
}
#endif

static void
bgworker_shmem_startup(void)
{
	bool		found;

	if (prev_shmem_startup_hook)
		prev_shmem_startup_hook();
	LWLockAcquire(AddinShmemInitLock, LW_EXCLUSIVE);
	slots = ShmemInitStruct(slots_name(), sizeof(struct worker_slot) * MAX_WORKERS, &found);
	if (!found)
		memset(slots, 0, sizeof(struct worker_slot) * MAX_WORKERS);
	LWLockRelease(AddinShmemInitLock);
}

/*
 * Registers worker index (Go's list) as a static background worker.
 * start: 0 after recovery, 1 at consistent state, 2 at postmaster start;
 * restart: seconds before restarting after a failure, -1 never.
 */
ErrorData *
gopgext_bgworker_register(int64 index, const char *name, const char *type,
						  int64 start, int64 restart, bool database)
{
	GOPGEXT_GUARD({
		BackgroundWorker w;

		if (!process_shared_preload_libraries_in_progress)
			ereport(ERROR,
					(errcode(ERRCODE_OBJECT_NOT_IN_PREREQUISITE_STATE),
					 errmsg("background workers of \"%s\" can only be registered when it is loaded via \"shared_preload_libraries\"",
							gopgext_name())));
		if (index >= MAX_WORKERS)
			ereport(ERROR,
					(errcode(ERRCODE_PROGRAM_LIMIT_EXCEEDED),
					 errmsg("too many background workers in \"%s\" (at most %d)",
							gopgext_name(), MAX_WORKERS)));
		if (nregistered == 0)
		{
#if PG_VERSION_NUM >= 150000
			prev_shmem_request_hook = shmem_request_hook;
			shmem_request_hook = bgworker_shmem_request;
#else
			RequestAddinShmemSpace(MAXALIGN(sizeof(struct worker_slot) * MAX_WORKERS));
#endif
			prev_shmem_startup_hook = shmem_startup_hook;
			shmem_startup_hook = bgworker_shmem_startup;
		}
		nregistered++;

		memset(&w, 0, sizeof(w));
		w.bgw_flags = BGWORKER_SHMEM_ACCESS |
			(database ? BGWORKER_BACKEND_DATABASE_CONNECTION : 0);
		w.bgw_start_time = start == 2 ? BgWorkerStart_PostmasterStart :
			start == 1 ? BgWorkerStart_ConsistentState : BgWorkerStart_RecoveryFinished;
		w.bgw_restart_time = restart < 0 ? BGW_NEVER_RESTART : (int) restart;
		strlcpy(w.bgw_library_name, gopgext_name(), sizeof(w.bgw_library_name));
		strlcpy(w.bgw_function_name, "gopgext_export_bgworker_main", sizeof(w.bgw_function_name));
		strlcpy(w.bgw_name, name, sizeof(w.bgw_name));
		strlcpy(w.bgw_type, type, sizeof(w.bgw_type));
		w.bgw_main_arg = Int32GetDatum((int32) index);
		w.bgw_notify_pid = 0;
		RegisterBackgroundWorker(&w);
	});
}

static void
clear_slot(int code, Datum arg)
{
	if (slots && my_index >= 0)
	{
		slots[my_index].proc = NULL;
		pg_write_barrier();
		slots[my_index].pid = 0;
	}
}

/* The entry point of every worker of the library. */
void
gopgext_export_bgworker_main(Datum arg)
{
	LOCAL_FCINFO(fc, 1);

	my_index = DatumGetInt32(arg);
	pqsignal(SIGHUP, SignalHandlerForConfigReload);
	pqsignal(SIGTERM, SignalHandlerForShutdownRequest);
	BackgroundWorkerUnblockSignals();

	if (slots)
	{
		slots[my_index].proc = MyProc;
		pg_write_barrier();
		slots[my_index].pid = MyProcPid;
		before_shmem_exit(clear_slot, (Datum) 0);
	}

	/*
	 * C memory Go allocates outside transactions (log messages, ...) goes
	 * here, and is freed at each Wait.
	 */
	worker_context = AllocSetContextCreate(TopMemoryContext, "gopgext bgworker",
										   ALLOCSET_DEFAULT_SIZES);
	MemoryContextSwitchTo(worker_context);

	InitFunctionCallInfoData(*fc, NULL, 1, InvalidOid, NULL, NULL);
	fc->args[0].value = Int64GetDatum((int64) my_index);
	fc->args[0].isnull = false;
	/* An error Go returns is raised here: the worker exits with code 1. */
	gopgext_invoke(fc, gopgext_go_bgworker_main);
	proc_exit(0);
}

/* Connects the worker to a database, as a role (NULL: the bootstrap superuser). */
ErrorData *
gopgext_bgworker_connect(const char *database, const char *user)
{
	GOPGEXT_GUARD(BackgroundWorkerInitializeConnection(database, user, 0));
}

bool
gopgext_bgworker_shutdown_requested(void)
{
	return ShutdownRequestPending;
}

/*
 * Waits until the latch is set or timeout_ms pass (< 0: no timeout), then
 * reloads the configuration if asked to. Returns false once the worker
 * was asked to stop. Exits if the postmaster dies.
 */
bool
gopgext_bgworker_wait(int64 timeout_ms)
{
	int			events = WL_LATCH_SET | WL_EXIT_ON_PM_DEATH;

	if (ShutdownRequestPending)
		return false;
	MemoryContextSwitchTo(worker_context);
	MemoryContextReset(worker_context);
	if (timeout_ms >= 0)
		events |= WL_TIMEOUT;
	pgstat_report_activity(STATE_IDLE, NULL);
	(void) WaitLatch(MyLatch, events, timeout_ms >= 0 ? (long) Min(timeout_ms, LONG_MAX) : -1L,
					 PG_WAIT_EXTENSION);
	ResetLatch(MyLatch);

	/*
	 * Process interrupts: procsignal barriers (DROP DATABASE waits for
	 * every process to absorb one), memory context logging... A query
	 * cancel (pg_cancel_backend) is reported and ignored.
	 */
	if (INTERRUPTS_PENDING_CONDITION())
	{
		MemoryContext ctx = CurrentMemoryContext;

		PG_TRY();
		{
			CHECK_FOR_INTERRUPTS();
		}
		PG_CATCH();
		{
			MemoryContextSwitchTo(ctx);
			EmitErrorReport();
			FlushErrorState();
		}
		PG_END_TRY();
	}
	if (ConfigReloadPending)
	{
		MemoryContext ctx = CurrentMemoryContext;

		ConfigReloadPending = false;
		PG_TRY();
		{
			ProcessConfigFile(PGC_SIGHUP);
		}
		PG_CATCH();
		{
			MemoryContextSwitchTo(ctx);
			EmitErrorReport();
			FlushErrorState();
		}
		PG_END_TRY();
	}
	return !ShutdownRequestPending;
}

/* Starts a transaction with a snapshot, for the SQL Go runs. */
ErrorData *
gopgext_bgworker_begin(const char *activity)
{
	GOPGEXT_GUARD({
		if (IsTransactionState())
			ereport(ERROR,
					(errcode(ERRCODE_ACTIVE_SQL_TRANSACTION),
					 errmsg("a background worker transaction is already in progress")));
		SetCurrentStatementStartTimestamp();
		StartTransactionCommand();
		PushActiveSnapshot(GetTransactionSnapshot());
		pgstat_report_activity(STATE_RUNNING, activity);
	});
}

/*
 * Commits (or aborts) the transaction. A commit that fails (deferred
 * constraints, serialization failures...) aborts it, and its error is
 * returned (free it with gopgext_bgworker_free_error): unlike the errors of
 * other helpers, it isn't the pending error, as the transaction is over.
 */
void *
gopgext_bgworker_end(bool commit)
{
	ErrorData  *volatile err = NULL;

	PG_TRY();
	{
		if (ActiveSnapshotSet())
			PopActiveSnapshot();
		if (commit)
			CommitTransactionCommand();
		else
			AbortCurrentTransaction();
	}
	PG_CATCH();
	{
		MemoryContextSwitchTo(TopMemoryContext);
		err = CopyErrorData();
		FlushErrorState();
		AbortCurrentTransaction();
	}
	PG_END_TRY();
	MemoryContextSwitchTo(worker_context);
	pgstat_report_stat(false);
	pgstat_report_activity(STATE_IDLE, NULL);
	return err;
}

void
gopgext_bgworker_free_error(void *edata)
{
	FreeErrorData((ErrorData *) edata);
}

bool
gopgext_bgworker_attached(void)
{
	return slots != NULL;
}

/* Sets the latch of worker index, if it's running. */
void
gopgext_bgworker_wake(int64 index)
{
	PGPROC	   *proc;

	if (slots == NULL || index < 0 || index >= MAX_WORKERS)
		return;
	proc = slots[index].proc;
	pg_read_barrier();
	/* A stale PGPROC only gets a spurious wakeup: latches allow them. */
	if (proc != NULL)
		SetLatch(&proc->procLatch);
}

int64
gopgext_bgworker_pid(int64 index)
{
	if (slots == NULL || index < 0 || index >= MAX_WORKERS)
		return 0;
	return slots[index].pid;
}
