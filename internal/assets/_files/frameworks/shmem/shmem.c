/*
 * C side of gopgext/shmem: shared memory (raw bytes and hash tables) and
 * lightweight locks for libraries loaded by shared_preload_libraries.
 *
 * Go registers objects while the library loads in the postmaster; their
 * space is requested (shmem_request_hook from PostgreSQL 15, directly in
 * _PG_init before), and the startup hook creates or attaches them, then
 * runs Go's startup callbacks once the memory is new. On a clean
 * shutdown, the postmaster runs Go's shutdown callbacks. Forked backends
 * inherit the attached addresses.
 */
#include "postgres.h"

#include "miscadmin.h"
#include "storage/ipc.h"
#include "storage/lwlock.h"
#include "storage/shmem.h"
#include "utils/hsearch.h"
#include "utils/memutils.h"

#include "gopgext.h"

void		gopgext_go_shmem_startup(struct gopgext_call *call);
void		gopgext_go_shmem_shutdown(struct gopgext_call *call);

enum
{
	SHMEM_LOCK,
	SHMEM_BYTES,
	SHMEM_TABLE,
};

struct shmem_object
{
	int			kind;
	char	   *name;
	Size		size;			/* SHMEM_BYTES */
	Size		keysize;		/* SHMEM_TABLE */
	Size		entrysize;
	long		max;
	void	   *ptr;			/* the bytes, the HTAB, or the LWLock */
};

static struct shmem_object *objects;
static int	nobjects;
static int	maxobjects;
static bool attached;

#if PG_VERSION_NUM >= 150000
static shmem_request_hook_type prev_shmem_request_hook;
#endif
static shmem_startup_hook_type prev_shmem_startup_hook;

int64		gopgext_shmem_register(int64 kind, const char *name, int64 size,
								   int64 keysize, int64 entrysize, int64 max);
bool		gopgext_shmem_attached(void);
void	   *gopgext_shmem_ptr(int64 i);
ErrorData  *gopgext_shmem_lock(int64 i, bool exclusive);
ErrorData  *gopgext_shmem_unlock(int64 i);
void	   *gopgext_shmem_hash_search(int64 i, const void *key, int64 action, bool *found);
int64		gopgext_shmem_hash_count(int64 i);
ErrorData  *gopgext_shmem_hash_entries(int64 i, void ***out, int64 *n);

static Size
object_size(struct shmem_object *o)
{
	switch (o->kind)
	{
		case SHMEM_BYTES:
			return MAXALIGN(o->size);
		case SHMEM_TABLE:
			return hash_estimate_size(o->max, o->entrysize);
	}
	return 0;
}

/* The marker telling whether the memory is new. */
static char *
marker_name(void)
{
	return psprintf("gopgext %s", gopgext_name());
}

static void
request_object(struct shmem_object *o)
{
	if (o->kind == SHMEM_LOCK)
		RequestNamedLWLockTranche(o->name, 1);
	else
		RequestAddinShmemSpace(object_size(o));
}

#if PG_VERSION_NUM >= 150000
static void
shmem_request(void)
{
	if (prev_shmem_request_hook)
		prev_shmem_request_hook();
	RequestAddinShmemSpace(MAXALIGN(sizeof(int64)));
	for (int i = 0; i < nobjects; i++)
		request_object(&objects[i]);
}
#endif

/* Runs a Go callback outside a transaction: errors are logged. */
static void
run_go(gopgext_gofn fn)
{
	MemoryContext ctx = CurrentMemoryContext;

	PG_TRY();
	{
		LOCAL_FCINFO(fc, 0);

		InitFunctionCallInfoData(*fc, NULL, 0, InvalidOid, NULL, NULL);
		gopgext_invoke(fc, fn);
	}
	PG_CATCH();
	{
		MemoryContextSwitchTo(ctx);
		EmitErrorReport();
		FlushErrorState();
	}
	PG_END_TRY();
}

static void
shmem_shutdown(int code, Datum arg)
{
	/* Not after a crash: the memory may be corrupt. */
	if (code == 0 && attached && gopgext_loaded())
		run_go(gopgext_go_shmem_shutdown);
}

static void
shmem_startup(void)
{
	bool		found;

	if (prev_shmem_startup_hook)
		prev_shmem_startup_hook();

	LWLockAcquire(AddinShmemInitLock, LW_EXCLUSIVE);
	ShmemInitStruct(marker_name(), sizeof(int64), &found);
	for (int i = 0; i < nobjects; i++)
	{
		struct shmem_object *o = &objects[i];
		bool		ofound;

		switch (o->kind)
		{
			case SHMEM_LOCK:
				o->ptr = &GetNamedLWLockTranche(o->name)->lock;
				break;
			case SHMEM_BYTES:
				o->ptr = ShmemInitStruct(o->name, o->size, &ofound);
				if (!ofound)
					memset(o->ptr, 0, o->size);
				break;
			case SHMEM_TABLE:
				{
					HASHCTL		info;

					memset(&info, 0, sizeof(info));
					info.keysize = o->keysize;
					info.entrysize = o->entrysize;
					o->ptr = ShmemInitHash(o->name, o->max, o->max, &info,
										   HASH_ELEM | HASH_BLOBS);
				}
				break;
		}
	}
	LWLockRelease(AddinShmemInitLock);
	attached = true;

	if (!IsUnderPostmaster)
		on_shmem_exit(shmem_shutdown, (Datum) 0);
	if (!found && gopgext_loaded())
		run_go(gopgext_go_shmem_startup);
}

/*
 * Registers an object (while the library loads); returns its index, or -1
 * if the library isn't being loaded by shared_preload_libraries.
 */
int64
gopgext_shmem_register(int64 kind, const char *name, int64 size,
					   int64 keysize, int64 entrysize, int64 max)
{
	struct shmem_object *o;

	if (!process_shared_preload_libraries_in_progress)
		return -1;
	if (nobjects == 0)
	{
#if PG_VERSION_NUM >= 150000
		prev_shmem_request_hook = shmem_request_hook;
		shmem_request_hook = shmem_request;
#else
		RequestAddinShmemSpace(MAXALIGN(sizeof(int64)));
#endif
		prev_shmem_startup_hook = shmem_startup_hook;
		shmem_startup_hook = shmem_startup;
	}
	if (nobjects == maxobjects)
	{
		maxobjects = maxobjects ? maxobjects * 2 : 8;
		objects = objects ?
			repalloc(objects, sizeof(*objects) * maxobjects) :
			MemoryContextAlloc(TopMemoryContext, sizeof(*objects) * maxobjects);
	}
	o = &objects[nobjects];
	memset(o, 0, sizeof(*o));
	o->kind = kind;
	o->name = MemoryContextStrdup(TopMemoryContext, name);
	o->size = size;
	o->keysize = keysize;
	o->entrysize = entrysize;
	o->max = max;
#if PG_VERSION_NUM < 150000
	request_object(o);
#endif
	return nobjects++;
}

bool
gopgext_shmem_attached(void)
{
	return attached;
}

void *
gopgext_shmem_ptr(int64 i)
{
	return attached ? objects[i].ptr : NULL;
}

ErrorData *
gopgext_shmem_lock(int64 i, bool exclusive)
{
	GOPGEXT_GUARD({
		LWLockAcquire(objects[i].ptr, exclusive ? LW_EXCLUSIVE : LW_SHARED);
	});
}

ErrorData *
gopgext_shmem_unlock(int64 i)
{
	GOPGEXT_GUARD({
		LWLockRelease(objects[i].ptr);
	});
}

/* hash_search; action: 0 find, 1 enter (NULL when out of memory), 2 remove. */
void *
gopgext_shmem_hash_search(int64 i, const void *key, int64 action, bool *found)
{
	static const HASHACTION actions[] = {HASH_FIND, HASH_ENTER_NULL, HASH_REMOVE};

	return hash_search(objects[i].ptr, key, actions[action], found);
}

int64
gopgext_shmem_hash_count(int64 i)
{
	return hash_get_num_entries(objects[i].ptr);
}

/* All the entries of a table, in a palloc'd array. */
ErrorData *
gopgext_shmem_hash_entries(int64 i, void ***out, int64 *n)
{
	GOPGEXT_GUARD({
		HTAB	   *h = objects[i].ptr;
		HASH_SEQ_STATUS seq;
		int64		max = hash_get_num_entries(h);
		void	   *e;
		void	  **entries = palloc(sizeof(void *) * (max + 1));
		int64		count = 0;

		hash_seq_init(&seq, h);
		while ((e = hash_seq_search(&seq)) != NULL)
		{
			if (count == max)
			{
				hash_seq_term(&seq);
				break;
			}
			entries[count++] = e;
		}
		*out = entries;
		*n = count;
	});
}
