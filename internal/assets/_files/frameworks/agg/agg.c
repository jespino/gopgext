/*
 * C side of gopgext/agg: handles of aggregate states that are Go values.
 *
 * A state is a small struct allocated in the aggregate's memory context
 * (the Datum of transition type internal), holding the id of the Go value
 * (agg.State) in Go's table of live states. When PostgreSQL resets or
 * deletes that context (end of a group, of the query, or an error), a
 * callback records the id as freed; Go drops those states the next time it
 * reads or returns one, so no Go code runs from the callback.
 */
#include "postgres.h"

#include "fmgr.h"
#include "utils/memutils.h"

#include "gopgext.h"

#define AGG_MAGIC 0x67616767	/* "gagg" */

struct agg_handle
{
	uint32		magic;
	uint64		id;
	MemoryContextCallback cb;
};

ErrorData  *gopgext_agg_new(struct gopgext_call *call, uint64 id, Datum *out);
ErrorData  *gopgext_agg_id(Datum d, uint64 *out);
int64		gopgext_agg_freed(uint64 *buf, int64 max);

/* Ids of freed states, not yet dropped by Go. */
static uint64 *freed;
static int	nfreed;
static int	maxfreed;

static void
agg_reset(void *arg)
{
	struct agg_handle *h = arg;

	h->magic = 0;
	if (nfreed == maxfreed)
	{
		int			n = maxfreed ? maxfreed * 2 : 1024;
		uint64	   *p = MemoryContextAllocExtended(TopMemoryContext, sizeof(uint64) * n,
												   MCXT_ALLOC_NO_OOM);

		if (p == NULL)
			return;				/* the Go value leaks; never fail here */
		if (freed)
		{
			memcpy(p, freed, sizeof(uint64) * nfreed);
			pfree(freed);
		}
		freed = p;
		maxfreed = n;
	}
	freed[nfreed++] = h->id;
}

/* A new handle for Go state id, in the aggregate's memory context. */
ErrorData *
gopgext_agg_new(struct gopgext_call *call, uint64 id, Datum *out)
{
	GOPGEXT_GUARD({
		MemoryContext aggctx;
		struct agg_handle *h;

		if (call->fcinfo == NULL || !AggCheckCallContext(call->fcinfo, &aggctx))
			ereport(ERROR,
					(errcode(ERRCODE_FEATURE_NOT_SUPPORTED),
					 errmsg("an agg.State can only be returned by a function called by an aggregate")));
		h = MemoryContextAlloc(aggctx, sizeof(*h));
		h->magic = AGG_MAGIC;
		h->id = id;
		h->cb.func = agg_reset;
		h->cb.arg = h;
		MemoryContextRegisterResetCallback(aggctx, &h->cb);
		*out = PointerGetDatum(h);
	});
}

/* The Go state id of a handle. */
ErrorData *
gopgext_agg_id(Datum d, uint64 *out)
{
	GOPGEXT_GUARD({
		struct agg_handle *h = (struct agg_handle *) DatumGetPointer(d);

		if (h == NULL || h->magic != AGG_MAGIC)
			ereport(ERROR,
					(errcode(ERRCODE_DATATYPE_MISMATCH),
					 errmsg("internal argument is not an agg.State")));
		*out = h->id;
	});
}

/* Moves up to max freed ids to buf; returns how many. */
int64
gopgext_agg_freed(uint64 *buf, int64 max)
{
	int64		n = Min((int64) nfreed, max);

	nfreed -= n;
	memcpy(buf, freed + nfreed, sizeof(uint64) * n);
	return n;
}
