/*
 * C side of gopgext/xact: transaction callbacks.
 *
 * Go schedules callbacks for the current transaction (xact.Callback,
 * a bitmask here); at PRE_COMMIT (and PRE_PREPARE) they run, in Go, where
 * an error aborts the commit. Other transaction events never call Go:
 * the end of a transaction (commit or abort) only bumps the transaction
 * counter (xact.ID) and drops the scheduled callbacks, and the ends of
 * subtransactions are logged (once Go asked to track them, xact.Current)
 * so Go can tell which savepoints were rolled back.
 */
#include "postgres.h"

#include "access/xact.h"
#include "utils/memutils.h"

#include "gopgext.h"

void		gopgext_go_xact_precommit(struct gopgext_call *call);

void		gopgext_xact_install(void);
void		gopgext_xact_schedule(int64 index);
uint64		gopgext_xact_id(void);
uint32		gopgext_xact_current(void);
int64		gopgext_xact_events(void);
void		gopgext_xact_event(int64 i, uint32 *sub, uint32 *parent, bool *aborted);
bool		gopgext_xact_in_progress(void);

struct subxact_event
{
	SubTransactionId sub;
	SubTransactionId parent;
	bool		aborted;
};

static uint64 scheduled;		/* callbacks to run before commit */
static uint64 xact_id = 1;		/* changes at the end of each transaction */
static bool tracking;			/* log the ends of subtransactions */
static struct subxact_event *events;
static int	nevents;
static int	maxevents;

static void
end_of_xact(void)
{
	scheduled = 0;
	tracking = false;
	nevents = 0;
	xact_id++;
}

static void
xact_callback(XactEvent event, void *arg)
{
	switch (event)
	{
		case XACT_EVENT_PRE_COMMIT:
		case XACT_EVENT_PRE_PREPARE:
			/* Callbacks can schedule others: run until none is left. */
			while (scheduled != 0)
			{
				LOCAL_FCINFO(fc, 1);
				uint64		mask = scheduled;

				scheduled = 0;
				InitFunctionCallInfoData(*fc, NULL, 1, InvalidOid, NULL, NULL);
				fc->args[0].value = UInt64GetDatum(mask);
				fc->args[0].isnull = false;
				gopgext_invoke(fc, gopgext_go_xact_precommit);
			}
			break;
		case XACT_EVENT_COMMIT:
		case XACT_EVENT_ABORT:
		case XACT_EVENT_PREPARE:
		case XACT_EVENT_PARALLEL_COMMIT:
		case XACT_EVENT_PARALLEL_ABORT:
			end_of_xact();
			break;
		default:
			break;
	}
}

static void
subxact_callback(SubXactEvent event, SubTransactionId sub,
				 SubTransactionId parent, void *arg)
{
	if (!tracking || (event != SUBXACT_EVENT_COMMIT_SUB && event != SUBXACT_EVENT_ABORT_SUB))
		return;
	if (nevents == maxevents)
	{
		int			n = maxevents ? maxevents * 2 : 16;

		/* No error during an abort: keep the log as it is if out of memory. */
		struct subxact_event *e = MemoryContextAllocExtended(TopMemoryContext, sizeof(*events) * n,
															 MCXT_ALLOC_NO_OOM);

		if (e == NULL)
			return;
		if (events)
		{
			memcpy(e, events, sizeof(*events) * nevents);
			pfree(events);
		}
		events = e;
		maxevents = n;
	}
	events[nevents].sub = sub;
	events[nevents].parent = parent;
	events[nevents].aborted = event == SUBXACT_EVENT_ABORT_SUB;
	nevents++;
}

void
gopgext_xact_install(void)
{
	static bool installed;

	if (installed)
		return;
	installed = true;
	RegisterXactCallback(xact_callback, NULL);
	RegisterSubXactCallback(subxact_callback, NULL);
}

void
gopgext_xact_schedule(int64 index)
{
	scheduled |= UINT64CONST(1) << index;
}

uint64
gopgext_xact_id(void)
{
	return xact_id;
}

/* The current subtransaction; from now on, the ends of subtransactions are logged. */
uint32
gopgext_xact_current(void)
{
	tracking = true;
	return GetCurrentSubTransactionId();
}

int64
gopgext_xact_events(void)
{
	return nevents;
}

void
gopgext_xact_event(int64 i, uint32 *sub, uint32 *parent, bool *aborted)
{
	*sub = events[i].sub;
	*parent = events[i].parent;
	*aborted = events[i].aborted;
}

bool
gopgext_xact_in_progress(void)
{
	return IsTransactionState();
}
