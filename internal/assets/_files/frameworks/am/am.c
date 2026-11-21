/*
 * C side of gopgext/am: index access methods written in Go.
 *
 * The extension registers an am.Method per access method (in Go, at load:
 * gopgext_am_register) and its sql/ files create each access method with
 * its own handler function, all with the C symbol gopgext_am_handler:
 *
 *	CREATE FUNCTION ivfflat_handler(internal) RETURNS index_am_handler
 *		AS 'MODULE_PATHNAME', 'gopgext_am_handler' LANGUAGE C;
 *	CREATE ACCESS METHOD ivfflat TYPE INDEX HANDLER ivfflat_handler;
 *
 * The handler finds the access method it serves (pg_am.amhandler) and
 * returns the IndexAmRoutine of its slot. Every callback dispatches to the
 * Go Method registered under the access method's name.
 *
 * Storage: Go never sees buffers. Block 0 is a meta page holding a small
 * blob (am.Index.Meta); every other page belongs to a chain (a linked list
 * of pages, identified by its first block) holding records (page items).
 * Every modification is one GenericXLog record, so it is crash-safe and
 * replicated, and applied atomically: readers (scans) only take buffer
 * share locks, page by page, and copy what they read.
 *
 * Writers (build, insert, vacuum) are serialized by a heavyweight lock on
 * page 0 of the index (LockPage, like GIN's pending list cleanup): Go code
 * modifying the index never runs concurrently with another modification,
 * so it needs no locking of its own.
 */
#include "postgres.h"

#include "access/amapi.h"
#include "access/generic_xlog.h"
#include "access/genam.h"
#include "access/htup_details.h"
#include "access/reloptions.h"
#include "access/relscan.h"
#include "access/stratnum.h"
#include "access/table.h"
#include "access/tableam.h"
#include "access/xact.h"
#include "access/xloginsert.h"
#include "catalog/index.h"
#include "catalog/pg_am.h"
#include "catalog/pg_opfamily.h"
#include "commands/defrem.h"
#include "commands/vacuum.h"
#include "miscadmin.h"
#include "nodes/pathnodes.h"
#include "storage/bufmgr.h"
#include "storage/lmgr.h"
#include "utils/float.h"
#include "utils/fmgroids.h"
#include "utils/memutils.h"
#include "utils/rel.h"
#include "utils/selfuncs.h"
#include "utils/syscache.h"

#include "gopgext.h"

/* Go (gopgext/am), called through gopgext_invoke. */
void		gopgext_go_am_build(struct gopgext_call *call);
void		gopgext_go_am_build_row(struct gopgext_call *call);
void		gopgext_go_am_insert(struct gopgext_call *call);
void		gopgext_go_am_vacuum(struct gopgext_call *call);
void		gopgext_go_am_cost(struct gopgext_call *call);
void		gopgext_go_am_scan_begin(struct gopgext_call *call);
void		gopgext_go_am_scan_next(struct gopgext_call *call);
void		gopgext_go_am_scan_end(struct gopgext_call *call);

/* Services for Go. */
ErrorData  *gopgext_am_register(const char *name, int64 nopts, const char **names,
								const char **descs, const int64 *defaults, const int64 *mins,
								const int64 *maxs, int64 support, bool orderby, bool multicol);
ErrorData  *gopgext_am_meta(Relation rel, char **data, int64 *len);
ErrorData  *gopgext_am_set_meta(Relation rel, const char *data, int64 len);
ErrorData  *gopgext_am_new_chain(Relation rel, int64 *chain);
ErrorData  *gopgext_am_append(Relation rel, int64 chain, const char *data, int64 len, int64 *rid);
ErrorData  *gopgext_am_get(Relation rel, int64 rid, char **data, int64 *len);
ErrorData  *gopgext_am_update(Relation rel, int64 rid, const char *data, int64 len);
ErrorData  *gopgext_am_delete(Relation rel, int64 rid);
ErrorData  *gopgext_am_read_page(Relation rel, int64 blkno, char **data, int64 *len);
ErrorData  *gopgext_am_build_scan(void *ctx, int64 *heap_tuples);
bool		gopgext_am_is_dead(void *ctx, int64 tid);
int64		gopgext_am_option(Relation rel, int64 i, int64 def);
char	   *gopgext_am_opfamily(Relation rel, int64 col);
int64		gopgext_am_natts(Relation rel);
int64		gopgext_am_relfilenode(Relation rel);
int64		gopgext_am_xact_gen(void);
void		gopgext_am_pfree(void *p);

/* ---- Registered access methods ----------------------------------------- */

#define AM_MAX_METHODS 8
#define AM_MAX_OPTIONS 16

typedef struct AmOptions
{
	int32		vl_len_;		/* varlena header */
	int			values[AM_MAX_OPTIONS];
	bool		set;			/* parsed from reloptions (always true) */
} AmOptions;

typedef struct AmSlot
{
	char	   *name;
	relopt_kind kind;
	int			nopts;
	relopt_parse_elt tab[AM_MAX_OPTIONS];
	int			support;
	bool		orderby;
	bool		multicol;
} AmSlot;

static AmSlot slots[AM_MAX_METHODS];
static int	nslots;

ErrorData *
gopgext_am_register(const char *name, int64 nopts, const char **names, const char **descs,
					const int64 *defaults, const int64 *mins, const int64 *maxs,
					int64 support, bool orderby, bool multicol)
{
	GOPGEXT_GUARD({
		AmSlot	   *s;

		for (int i = 0; i < nslots; i++)
			if (strcmp(slots[i].name, name) == 0)
				ereport(ERROR,
						(errcode(ERRCODE_DUPLICATE_OBJECT),
						 errmsg("access method \"%s\" is already registered", name)));
		if (nslots == AM_MAX_METHODS)
			ereport(ERROR,
					(errcode(ERRCODE_PROGRAM_LIMIT_EXCEEDED),
					 errmsg("an extension can register at most %d access methods", AM_MAX_METHODS)));
		if (nopts > AM_MAX_OPTIONS)
			ereport(ERROR,
					(errcode(ERRCODE_PROGRAM_LIMIT_EXCEEDED),
					 errmsg("an access method can have at most %d options", AM_MAX_OPTIONS)));
		s = &slots[nslots];
		s->name = MemoryContextStrdup(TopMemoryContext, name);
		s->kind = nopts > 0 ? add_reloption_kind() : 0;
		s->nopts = (int) nopts;
		for (int i = 0; i < nopts; i++)
		{
			char	   *n = MemoryContextStrdup(TopMemoryContext, names[i]);

			add_int_reloption(s->kind, n, MemoryContextStrdup(TopMemoryContext, descs[i]),
							  (int) defaults[i], (int) mins[i], (int) maxs[i],
							  AccessExclusiveLock);
			s->tab[i].optname = n;
			s->tab[i].opttype = RELOPT_TYPE_INT;
			s->tab[i].offset = offsetof(AmOptions, values) + i * sizeof(int);
		}
		s->support = (int) support;
		s->orderby = orderby;
		s->multicol = multicol;
		nslots++;
	});
}

static AmSlot *
slot_by_name(const char *name)
{
	for (int i = 0; i < nslots; i++)
		if (strcmp(slots[i].name, name) == 0)
			return &slots[i];
	ereport(ERROR,
			(errcode(ERRCODE_OBJECT_NOT_IN_PREREQUISITE_STATE),
			 errmsg("access method \"%s\" has no am.Method", name),
			 errhint("Call am.Register(\"%s\", ...) in main.", name)));
	pg_unreachable();
}

/* The name of the access method of an index. */
static char *
am_name(Relation index)
{
	char	   *name = get_am_name(index->rd_rel->relam);

	if (name == NULL)
		elog(ERROR, "cache lookup failed for access method %u", index->rd_rel->relam);
	return name;
}

/* ---- Pages ----------------------------------------------------------------- */

#define AM_MAGIC 0x676F414D		/* "goAM" */
#define AM_META_BLKNO 0
#define AM_PAGE_META 1

/* Special space of every page. */
typedef struct AmPageOpaque
{
	BlockNumber next;			/* next page of the chain */
	BlockNumber tail;			/* first page of a chain: its last page */
	uint32		flags;
	uint32		magic;
} AmPageOpaque;

#define AmOpaque(page) ((AmPageOpaque *) PageGetSpecialPointer(page))

/* Meta page contents. */
typedef struct AmMeta
{
	uint32		magic;
	uint32		version;
	uint32		len;
	char		data[FLEXIBLE_ARRAY_MEMBER];
} AmMeta;

#define AM_MAX_META (BLCKSZ - MAXALIGN(SizeOfPageHeaderData) - MAXALIGN(sizeof(AmPageOpaque)) - offsetof(AmMeta, data) - 16)
#define AM_MAX_RECORD (BLCKSZ - MAXALIGN(SizeOfPageHeaderData) - MAXALIGN(sizeof(AmPageOpaque)) - sizeof(ItemIdData) - 8)

/* Modifications are only allowed while the writer lock is held. */
static int	am_writing;

static void
check_writing(void)
{
	if (!am_writing)
		ereport(ERROR,
				(errcode(ERRCODE_OBJECT_NOT_IN_PREREQUISITE_STATE),
				 errmsg("an index can only be modified in Build, Insert and Vacuum")));
}

static void
init_page(Page page, uint32 flags)
{
	AmPageOpaque *o;

	PageInit(page, BLCKSZ, sizeof(AmPageOpaque));
	o = AmOpaque(page);
	o->next = InvalidBlockNumber;
	o->tail = InvalidBlockNumber;
	o->flags = flags;
	o->magic = AM_MAGIC;
}

static void
check_page(Relation rel, Page page, BlockNumber blkno)
{
	if (PageIsNew(page) || PageGetSpecialSize(page) != MAXALIGN(sizeof(AmPageOpaque)) ||
		AmOpaque(page)->magic != AM_MAGIC)
		ereport(ERROR,
				(errcode(ERRCODE_INDEX_CORRUPTED),
				 errmsg("index \"%s\" has an invalid page at block %u",
						RelationGetRelationName(rel), blkno)));
}

/* A new page at the end of the index (pinned and exclusively locked). */
static Buffer
new_buffer(Relation rel, ForkNumber fork)
{
	Buffer		buf;

#if PG_VERSION_NUM < 160000
	LockRelationForExtension(rel, ExclusiveLock);
#endif
	buf = ReadBufferExtended(rel, fork, P_NEW, RBM_NORMAL, NULL);
#if PG_VERSION_NUM < 160000
	UnlockRelationForExtension(rel, ExclusiveLock);
#endif
	LockBuffer(buf, BUFFER_LOCK_EXCLUSIVE);
	return buf;
}

static void
fill_meta(Page page, const char *data, int64 len)
{
	AmMeta	   *m = (AmMeta *) PageGetContents(page);

	m->magic = AM_MAGIC;
	m->version = 1;
	m->len = (uint32) len;
	if (len)
		memcpy(m->data, data, len);
	((PageHeader) page)->pd_lower = ((char *) m + offsetof(AmMeta, data) + len) - (char *) page;
}

/* Creates the meta page of an empty index. */
static void
init_meta(Relation rel)
{
	Buffer		buf = new_buffer(rel, MAIN_FORKNUM);
	GenericXLogState *state;
	Page		page;

	Assert(BufferGetBlockNumber(buf) == AM_META_BLKNO);
	state = GenericXLogStart(rel);
	page = GenericXLogRegisterBuffer(state, buf, GENERIC_XLOG_FULL_IMAGE);
	init_page(page, AM_PAGE_META);
	fill_meta(page, NULL, 0);
	GenericXLogFinish(state);
	UnlockReleaseBuffer(buf);
}

ErrorData *
gopgext_am_meta(Relation rel, char **data, int64 *len)
{
	GOPGEXT_GUARD({
		Buffer		buf = ReadBuffer(rel, AM_META_BLKNO);
		Page		page;
		AmMeta	   *m;

		LockBuffer(buf, BUFFER_LOCK_SHARE);
		page = BufferGetPage(buf);
		check_page(rel, page, AM_META_BLKNO);
		m = (AmMeta *) PageGetContents(page);
		*len = m->len;
		*data = palloc(m->len + 1);
		memcpy(*data, m->data, m->len);
		UnlockReleaseBuffer(buf);
	});
}

ErrorData *
gopgext_am_set_meta(Relation rel, const char *data, int64 len)
{
	GOPGEXT_GUARD({
		Buffer		buf;
		GenericXLogState *state;
		Page		page;

		check_writing();
		if (len < 0 || len > AM_MAX_META)
			ereport(ERROR,
					(errcode(ERRCODE_PROGRAM_LIMIT_EXCEEDED),
					 errmsg("index meta data of %lld bytes is too large (at most %d)",
							(long long) len, (int) AM_MAX_META)));
		buf = ReadBuffer(rel, AM_META_BLKNO);
		LockBuffer(buf, BUFFER_LOCK_EXCLUSIVE);
		check_page(rel, BufferGetPage(buf), AM_META_BLKNO);
		state = GenericXLogStart(rel);
		page = GenericXLogRegisterBuffer(state, buf, 0);
		fill_meta(page, data, len);
		GenericXLogFinish(state);
		UnlockReleaseBuffer(buf);
	});
}

ErrorData *
gopgext_am_new_chain(Relation rel, int64 *chain)
{
	GOPGEXT_GUARD({
		Buffer		buf;
		GenericXLogState *state;
		Page		page;

		check_writing();
		buf = new_buffer(rel, MAIN_FORKNUM);
		state = GenericXLogStart(rel);
		page = GenericXLogRegisterBuffer(state, buf, GENERIC_XLOG_FULL_IMAGE);
		init_page(page, 0);
		AmOpaque(page)->tail = BufferGetBlockNumber(buf);
		GenericXLogFinish(state);
		*chain = BufferGetBlockNumber(buf);
		UnlockReleaseBuffer(buf);
	});
}

static void
check_chain(Relation rel, int64 chain)
{
	if (chain <= AM_META_BLKNO || chain >= RelationGetNumberOfBlocks(rel))
		ereport(ERROR,
				(errcode(ERRCODE_INVALID_PARAMETER_VALUE),
				 errmsg("invalid chain %lld of index \"%s\"", (long long) chain,
						RelationGetRelationName(rel))));
}

#define RID(blk, off) (((int64) (blk) << 16) | (off))
#define RID_BLOCK(rid) ((BlockNumber) ((rid) >> 16))
#define RID_OFFSET(rid) ((OffsetNumber) ((rid) & 0xFFFF))

static void
append(Relation rel, int64 chain, const char *data, int64 len, int64 *rid)
{
	{
		Buffer		headbuf;
		Buffer		tailbuf;
		Buffer		newbuf = InvalidBuffer;
		Page		head;
		Page		tail;
		BlockNumber tailblk;
		GenericXLogState *state;
		OffsetNumber off;

		check_writing();
		check_chain(rel, chain);
		if (len < 0 || len > AM_MAX_RECORD)
			ereport(ERROR,
					(errcode(ERRCODE_PROGRAM_LIMIT_EXCEEDED),
					 errmsg("index record of %lld bytes is too large (at most %d)",
							(long long) len, (int) AM_MAX_RECORD)));
		headbuf = ReadBuffer(rel, (BlockNumber) chain);
		LockBuffer(headbuf, BUFFER_LOCK_EXCLUSIVE);
		check_page(rel, BufferGetPage(headbuf), (BlockNumber) chain);
		tailblk = AmOpaque(BufferGetPage(headbuf))->tail;
		if (tailblk == (BlockNumber) chain)
			tailbuf = headbuf;
		else
		{
			tailbuf = ReadBuffer(rel, tailblk);
			LockBuffer(tailbuf, BUFFER_LOCK_EXCLUSIVE);
			check_page(rel, BufferGetPage(tailbuf), tailblk);
		}

		state = GenericXLogStart(rel);
		tail = GenericXLogRegisterBuffer(state, tailbuf, 0);
		if (PageGetFreeSpace(tail) >= MAXALIGN(len))
		{
			off = PageAddItem(tail, (Item) data, len, InvalidOffsetNumber, false, false);
			*rid = RID(tailblk, off);
		}
		else
		{
			Page		newpage;
			BlockNumber newblk;

			newbuf = new_buffer(rel, MAIN_FORKNUM);
			newblk = BufferGetBlockNumber(newbuf);
			newpage = GenericXLogRegisterBuffer(state, newbuf, GENERIC_XLOG_FULL_IMAGE);
			init_page(newpage, 0);
			AmOpaque(tail)->next = newblk;
			head = tailbuf == headbuf ? tail : GenericXLogRegisterBuffer(state, headbuf, 0);
			AmOpaque(head)->tail = newblk;
			off = PageAddItem(newpage, (Item) data, len, InvalidOffsetNumber, false, false);
			*rid = RID(newblk, off);
		}
		if (off == InvalidOffsetNumber)
			elog(ERROR, "could not add a record to index \"%s\"", RelationGetRelationName(rel));
		GenericXLogFinish(state);
		if (BufferIsValid(newbuf))
			UnlockReleaseBuffer(newbuf);
		if (tailbuf != headbuf)
			UnlockReleaseBuffer(tailbuf);
		UnlockReleaseBuffer(headbuf);
	}
}

ErrorData *
gopgext_am_append(Relation rel, int64 chain, const char *data, int64 len, int64 *rid)
{
	GOPGEXT_GUARD(append(rel, chain, data, len, rid));
}

/* Pins and locks the page of a record, and returns its item. */
static ItemId
record_item(Relation rel, int64 rid, int mode, Buffer *buf)
{
	BlockNumber blk = RID_BLOCK(rid);
	OffsetNumber off = RID_OFFSET(rid);
	Page		page;
	ItemId		item;

	if (blk <= AM_META_BLKNO || blk >= RelationGetNumberOfBlocks(rel))
		ereport(ERROR,
				(errcode(ERRCODE_INVALID_PARAMETER_VALUE),
				 errmsg("invalid record %lld of index \"%s\"", (long long) rid,
						RelationGetRelationName(rel))));
	*buf = ReadBuffer(rel, blk);
	LockBuffer(*buf, mode);
	page = BufferGetPage(*buf);
	check_page(rel, page, blk);
	if (off < FirstOffsetNumber || off > PageGetMaxOffsetNumber(page) ||
		!ItemIdIsNormal(item = PageGetItemId(page, off)))
		ereport(ERROR,
				(errcode(ERRCODE_INVALID_PARAMETER_VALUE),
				 errmsg("record %lld of index \"%s\" does not exist", (long long) rid,
						RelationGetRelationName(rel))));
	return item;
}

ErrorData *
gopgext_am_get(Relation rel, int64 rid, char **data, int64 *len)
{
	GOPGEXT_GUARD({
		Buffer		buf;
		ItemId		item = record_item(rel, rid, BUFFER_LOCK_SHARE, &buf);

		*len = ItemIdGetLength(item);
		*data = palloc(*len + 1);
		memcpy(*data, PageGetItem(BufferGetPage(buf), item), *len);
		UnlockReleaseBuffer(buf);
	});
}

ErrorData *
gopgext_am_update(Relation rel, int64 rid, const char *data, int64 len)
{
	GOPGEXT_GUARD({
		Buffer		buf;
		ItemId		item;
		GenericXLogState *state;
		Page		page;

		check_writing();
		item = record_item(rel, rid, BUFFER_LOCK_EXCLUSIVE, &buf);
		if (ItemIdGetLength(item) != len)
			ereport(ERROR,
					(errcode(ERRCODE_INVALID_PARAMETER_VALUE),
					 errmsg("record %lld has %u bytes: an update can't change its size (%lld)",
							(long long) rid, ItemIdGetLength(item), (long long) len)));
		state = GenericXLogStart(rel);
		page = GenericXLogRegisterBuffer(state, buf, 0);
		memcpy(PageGetItem(page, PageGetItemId(page, RID_OFFSET(rid))), data, len);
		GenericXLogFinish(state);
		UnlockReleaseBuffer(buf);
	});
}

ErrorData *
gopgext_am_delete(Relation rel, int64 rid)
{
	GOPGEXT_GUARD({
		Buffer		buf;
		GenericXLogState *state;
		Page		page;

		check_writing();
		(void) record_item(rel, rid, BUFFER_LOCK_EXCLUSIVE, &buf);
		state = GenericXLogStart(rel);
		page = GenericXLogRegisterBuffer(state, buf, 0);
		ItemIdSetUnused(PageGetItemId(page, RID_OFFSET(rid)));
		PageRepairFragmentation(page);	/* reclaims the space */
		GenericXLogFinish(state);
		UnlockReleaseBuffer(buf);
	});
}

/*
 * The records of one page of a chain, copied: int64 next block (-1 at the
 * end), int64 count, then per record int64 id, int64 length and the bytes
 * (padded to 8).
 */
ErrorData *
gopgext_am_read_page(Relation rel, int64 blkno, char **data, int64 *len)
{
	GOPGEXT_GUARD({
		Buffer		buf;
		Page		page;
		OffsetNumber maxoff;
		Size		size = 16;
		char	   *p;
		int64		n = 0;

		if (blkno <= AM_META_BLKNO || blkno >= RelationGetNumberOfBlocks(rel))
			ereport(ERROR,
					(errcode(ERRCODE_INVALID_PARAMETER_VALUE),
					 errmsg("invalid chain page %lld of index \"%s\"", (long long) blkno,
							RelationGetRelationName(rel))));
		buf = ReadBuffer(rel, (BlockNumber) blkno);
		LockBuffer(buf, BUFFER_LOCK_SHARE);
		page = BufferGetPage(buf);
		check_page(rel, page, (BlockNumber) blkno);
		maxoff = PageGetMaxOffsetNumber(page);
		for (OffsetNumber off = FirstOffsetNumber; off <= maxoff; off++)
		{
			ItemId		item = PageGetItemId(page, off);

			if (ItemIdIsNormal(item))
				size += 16 + TYPEALIGN(8, ItemIdGetLength(item));
		}
		p = palloc0(size);
		*data = p;
		*len = size;
		*(int64 *) p = AmOpaque(page)->next == InvalidBlockNumber ? -1 : (int64) AmOpaque(page)->next;
		p += 16;
		for (OffsetNumber off = FirstOffsetNumber; off <= maxoff; off++)
		{
			ItemId		item = PageGetItemId(page, off);
			int64		l;

			if (!ItemIdIsNormal(item))
				continue;
			l = ItemIdGetLength(item);
			((int64 *) p)[0] = RID(blkno, off);
			((int64 *) p)[1] = l;
			memcpy(p + 16, PageGetItem(page, item), l);
			p += 16 + TYPEALIGN(8, l);
			n++;
		}
		((int64 *) *data)[1] = n;
		UnlockReleaseBuffer(buf);
	});
}

void
gopgext_am_pfree(void *p)
{
	if (p)
		pfree(p);
}

int64
gopgext_am_option(Relation rel, int64 i, int64 def)
{
	AmOptions  *opts = (AmOptions *) rel->rd_options;

	if (opts == NULL || i < 0 || i >= AM_MAX_OPTIONS)
		return def;
	return opts->values[i];
}

/* The name of the operator family of index column col (0-based). */
char *
gopgext_am_opfamily(Relation rel, int64 col)
{
	HeapTuple	tup;
	char	   *name;

	if (col < 0 || col >= IndexRelationGetNumberOfKeyAttributes(rel))
		return NULL;
	tup = SearchSysCache1(OPFAMILYOID, ObjectIdGetDatum(rel->rd_opfamily[col]));
	if (!HeapTupleIsValid(tup))
		return NULL;
	name = pstrdup(NameStr(((Form_pg_opfamily) GETSTRUCT(tup))->opfname));
	ReleaseSysCache(tup);
	return name;
}

int64
gopgext_am_natts(Relation rel)
{
	return IndexRelationGetNumberOfKeyAttributes(rel);
}

int64
gopgext_am_relfilenode(Relation rel)
{
	return rel->rd_rel->relfilenode;
}

/* Incremented when a transaction ends: Go drops scans that weren't ended. */
static int64 xact_gen;
static bool xact_callback_registered;

static void
am_xact_callback(XactEvent event, void *arg)
{
	if (event == XACT_EVENT_COMMIT || event == XACT_EVENT_ABORT ||
		event == XACT_EVENT_PARALLEL_COMMIT || event == XACT_EVENT_PARALLEL_ABORT)
	{
		xact_gen++;
		am_writing = 0;
	}
}

int64
gopgext_am_xact_gen(void)
{
	return xact_gen;
}

/* ---- Calling Go ------------------------------------------------------------ */

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

/* What Go's build, insert and vacuum calls need from C. */
struct am_ctx
{
	Relation	heap;
	Relation	index;
	IndexInfo  *info;
	IndexBulkDeleteCallback callback;
	void	   *callback_state;
};

static int64
pack_tid(ItemPointer tid)
{
	return ((int64) ItemPointerGetBlockNumberNoCheck(tid) << 16) | ItemPointerGetOffsetNumberNoCheck(tid);
}

static void
build_callback(Relation index, ItemPointer tid, Datum *values, bool *isnull,
			   bool tupleIsAlive, void *state)
{
	Datum		args[4];

	(*(int64 *) state)++;
	args[0] = Int64GetDatum(pack_tid(tid));
	args[1] = PointerGetDatum(values);
	args[2] = PointerGetDatum(isnull);
	args[3] = Int64GetDatum(IndexRelationGetNumberOfKeyAttributes(index));
	call_go(gopgext_go_am_build_row, 4, args);
}

/* Scans the table for Go's Build (each row goes to gopgext_go_am_build_row). */
ErrorData *
gopgext_am_build_scan(void *ctx, int64 *heap_tuples)
{
	GOPGEXT_GUARD({
		struct am_ctx *c = ctx;

		if (c->heap == NULL)
			elog(ERROR, "the table can only be scanned during Build");
		*heap_tuples = 0;
		table_index_build_scan(c->heap, c->index, c->info, true, true,
							   build_callback, heap_tuples, NULL);
	});
}

bool
gopgext_am_is_dead(void *ctx, int64 tid)
{
	struct am_ctx *c = ctx;
	ItemPointerData t;

	if (c->callback == NULL)
		return false;
	ItemPointerSet(&t, (BlockNumber) (tid >> 16), (OffsetNumber) (tid & 0xFFFF));
	return c->callback(&t, c->callback_state);
}

/* ---- Index AM callbacks ------------------------------------------------------- */

static void
writer_lock(Relation index)
{
	LockPage(index, AM_META_BLKNO, ExclusiveLock);
	am_writing++;
}

static void
writer_unlock(Relation index)
{
	am_writing--;
	UnlockPage(index, AM_META_BLKNO, ExclusiveLock);
}

static IndexBuildResult *
am_build(Relation heap, Relation index, IndexInfo *info)
{
	IndexBuildResult *result;
	struct am_ctx ctx = {heap, index, info, NULL, NULL};
	Datum		args[3];
	int64		ntuples;

	if (RelationGetNumberOfBlocks(index) != 0)
		elog(ERROR, "index \"%s\" already contains data", RelationGetRelationName(index));
	init_meta(index);
	args[0] = CStringGetDatum(am_name(index));
	args[1] = PointerGetDatum(index);
	args[2] = PointerGetDatum(&ctx);
	am_writing++;
	PG_TRY();
	{
		ntuples = DatumGetInt64(call_go(gopgext_go_am_build, 3, args));
	}
	PG_FINALLY();
	{
		am_writing--;
	}
	PG_END_TRY();
	result = palloc0(sizeof(IndexBuildResult));
	result->heap_tuples = (double) (ntuples >> 32);
	result->index_tuples = (double) (ntuples & 0xFFFFFFFF);
	return result;
}

static void
am_buildempty(Relation index)
{
	/* The init fork of an unlogged index: only the meta page. */
	Buffer		buf = new_buffer(index, INIT_FORKNUM);

	START_CRIT_SECTION();
	init_page(BufferGetPage(buf), AM_PAGE_META);
	fill_meta(BufferGetPage(buf), NULL, 0);
	MarkBufferDirty(buf);
	log_newpage_buffer(buf, true);
	END_CRIT_SECTION();
	UnlockReleaseBuffer(buf);
}

static bool
am_insert(Relation index, Datum *values, bool *isnull, ItemPointer tid, Relation heap,
		  IndexUniqueCheck checkUnique, bool indexUnchanged, IndexInfo *info)
{
	Datum		args[6];

	args[0] = CStringGetDatum(am_name(index));
	args[1] = PointerGetDatum(index);
	args[2] = Int64GetDatum(pack_tid(tid));
	args[3] = PointerGetDatum(values);
	args[4] = PointerGetDatum(isnull);
	args[5] = Int64GetDatum(IndexRelationGetNumberOfKeyAttributes(index));
	writer_lock(index);
	PG_TRY();
	{
		call_go(gopgext_go_am_insert, 6, args);
	}
	PG_FINALLY();
	{
		writer_unlock(index);
	}
	PG_END_TRY();
	return false;
}

static IndexBulkDeleteResult *
am_bulkdelete(IndexVacuumInfo *info, IndexBulkDeleteResult *stats,
			  IndexBulkDeleteCallback callback, void *callback_state)
{
	struct am_ctx ctx = {NULL, info->index, NULL, callback, callback_state};
	Datum		args[3];
	int64		r;

	if (stats == NULL)
		stats = palloc0(sizeof(IndexBulkDeleteResult));
	args[0] = CStringGetDatum(am_name(info->index));
	args[1] = PointerGetDatum(info->index);
	args[2] = PointerGetDatum(&ctx);
	writer_lock(info->index);
	PG_TRY();
	{
		r = DatumGetInt64(call_go(gopgext_go_am_vacuum, 3, args));
	}
	PG_FINALLY();
	{
		writer_unlock(info->index);
	}
	PG_END_TRY();
	stats->tuples_removed += (double) (r >> 32);
	stats->num_index_tuples = (double) (r & 0xFFFFFFFF);
	return stats;
}

static IndexBulkDeleteResult *
am_vacuumcleanup(IndexVacuumInfo *info, IndexBulkDeleteResult *stats)
{
	if (info->analyze_only)
		return stats;
	if (stats == NULL)
	{
		stats = palloc0(sizeof(IndexBulkDeleteResult));
		stats->num_index_tuples = info->num_heap_tuples;
		stats->estimated_count = info->estimated_count;
	}
	stats->num_pages = RelationGetNumberOfBlocks(info->index);
	return stats;
}

static void
am_costestimate(PlannerInfo *root, IndexPath *path, double loop_count,
				Cost *startup, Cost *total, Selectivity *selectivity,
				double *correlation, double *pages)
{
	GenericCosts costs;
	Relation	index;
	Datum		args[2];
	double		fraction;

	MemSet(&costs, 0, sizeof(costs));
	if (path->indexorderbys == NIL && path->indexclauses == NIL)
	{
		/* Nothing to search for: never worth it. */
		*startup = *total = get_float8_infinity();
		*selectivity = 1;
		*correlation = 0;
		*pages = 0;
		return;
	}
	index = index_open(path->indexinfo->indexoid, NoLock);
	args[0] = CStringGetDatum(am_name(index));
	args[1] = PointerGetDatum(index);
	fraction = DatumGetFloat8(call_go(gopgext_go_am_cost, 2, args));
	index_close(index, NoLock);
	fraction = Min(Max(fraction, 0.0), 1.0);

	costs.numIndexTuples = Max(path->indexinfo->tuples * fraction, 1.0);
	genericcostestimate(root, path, loop_count, &costs);
	/* Results come sorted once the scan has read what it needs. */
	*startup = costs.indexTotalCost;
	*total = costs.indexTotalCost;
	*selectivity = costs.indexSelectivity;
	*correlation = 0;
	*pages = costs.numIndexPages;
}

static bytea *am_options_slot(int slot, Datum reloptions, bool validate);

#define AM_OPTIONS_FN(i) \
	static bytea *am_options_##i(Datum reloptions, bool validate) { return am_options_slot(i, reloptions, validate); }
AM_OPTIONS_FN(0)
AM_OPTIONS_FN(1)
AM_OPTIONS_FN(2)
AM_OPTIONS_FN(3)
AM_OPTIONS_FN(4)
AM_OPTIONS_FN(5)
AM_OPTIONS_FN(6)
AM_OPTIONS_FN(7)
static amoptions_function am_options_fns[AM_MAX_METHODS] = {
	am_options_0, am_options_1, am_options_2, am_options_3,
	am_options_4, am_options_5, am_options_6, am_options_7,
};

static bytea *
am_options_slot(int slot, Datum reloptions, bool validate)
{
	AmSlot	   *s = &slots[slot];

	if (s->nopts == 0)
	{
		/* No options: reject any (default_reloptions would accept heap's). */
		if (validate && DatumGetPointer(reloptions) != NULL)
			ereport(ERROR,
					(errcode(ERRCODE_INVALID_PARAMETER_VALUE),
					 errmsg("access method \"%s\" has no options", s->name)));
		return NULL;
	}
	return (bytea *) build_reloptions(reloptions, validate, s->kind, sizeof(AmOptions),
									  s->tab, s->nopts);
}

static bool
am_validate(Oid opclassoid)
{
	return true;
}

/* Scans. */
typedef struct AmScan
{
	bool		started;
	int64		id;
} AmScan;

static int64 next_scan_id;

/* A scan key, for Go. */
struct am_key
{
	int64		attno;
	int64		strategy;
	Datum		arg;
	int64		isnull;
};

static IndexScanDesc
am_beginscan(Relation index, int nkeys, int norderbys)
{
	IndexScanDesc scan = RelationGetIndexScan(index, nkeys, norderbys);
	AmScan	   *s = palloc0(sizeof(AmScan));

	s->id = ++next_scan_id;
	scan->opaque = s;
	if (norderbys > 0)
	{
		scan->xs_orderbyvals = palloc0(sizeof(Datum) * norderbys);
		scan->xs_orderbynulls = palloc0(sizeof(bool) * norderbys);
	}
	return scan;
}

static void
end_go_scan(IndexScanDesc scan)
{
	AmScan	   *s = scan->opaque;
	Datum		args[1];

	if (!s->started)
		return;
	s->started = false;
	args[0] = Int64GetDatum(s->id);
	call_go(gopgext_go_am_scan_end, 1, args);
}

static void
am_rescan(IndexScanDesc scan, ScanKey keys, int nkeys, ScanKey orderbys, int norderbys)
{
	end_go_scan(scan);
	if (keys && scan->numberOfKeys > 0)
		memmove(scan->keyData, keys, scan->numberOfKeys * sizeof(ScanKeyData));
	if (orderbys && scan->numberOfOrderBys > 0)
		memmove(scan->orderByData, orderbys, scan->numberOfOrderBys * sizeof(ScanKeyData));
}

static struct am_key *
go_keys(ScanKey keys, int n)
{
	struct am_key *k = palloc0(sizeof(struct am_key) * Max(n, 1));

	for (int i = 0; i < n; i++)
	{
		k[i].attno = keys[i].sk_attno;
		k[i].strategy = keys[i].sk_strategy;
		k[i].arg = keys[i].sk_argument;
		k[i].isnull = (keys[i].sk_flags & SK_ISNULL) != 0;
	}
	return k;
}

static bool
am_gettuple(IndexScanDesc scan, ScanDirection dir)
{
	AmScan	   *s = scan->opaque;
	Datum		args[7];
	int64		tid;
	double		dist[INDEX_MAX_KEYS];
	bool		recheck = false,
				recheckorder = false,
				nulldist = false;

	if (!s->started)
	{
		Datum		bargs[7];

		bargs[0] = CStringGetDatum(am_name(scan->indexRelation));
		bargs[1] = PointerGetDatum(scan->indexRelation);
		bargs[2] = Int64GetDatum(s->id);
		bargs[3] = PointerGetDatum(go_keys(scan->keyData, scan->numberOfKeys));
		bargs[4] = Int64GetDatum(scan->numberOfKeys);
		bargs[5] = PointerGetDatum(go_keys(scan->orderByData, scan->numberOfOrderBys));
		bargs[6] = Int64GetDatum(scan->numberOfOrderBys);
		call_go(gopgext_go_am_scan_begin, 7, bargs);
		s->started = true;
	}
	args[0] = Int64GetDatum(s->id);
	args[1] = PointerGetDatum(&tid);
	args[2] = PointerGetDatum(dist);
	args[3] = Int64GetDatum(scan->numberOfOrderBys);
	args[4] = PointerGetDatum(&recheck);
	args[5] = PointerGetDatum(&recheckorder);
	args[6] = PointerGetDatum(&nulldist);
	if (!DatumGetBool(call_go(gopgext_go_am_scan_next, 7, args)))
		return false;
	ItemPointerSet(&scan->xs_heaptid, (BlockNumber) (tid >> 16), (OffsetNumber) (tid & 0xFFFF));
	scan->xs_recheck = recheck;
	scan->xs_recheckorderby = recheckorder;
	for (int i = 0; i < scan->numberOfOrderBys; i++)
	{
		scan->xs_orderbyvals[i] = nulldist ? (Datum) 0 : Float8GetDatum(dist[i]);
		scan->xs_orderbynulls[i] = nulldist;
	}
	if (scan->xs_want_itup && scan->xs_itup == NULL)
	{
		/*
		 * An index-only scan that needs no column (count(*)): it still
		 * wants a tuple. Indexes can't return columns (no amcanreturn).
		 */
		Datum		values[INDEX_MAX_KEYS] = {0};
		bool		nulls[INDEX_MAX_KEYS];

		memset(nulls, true, sizeof(nulls));
		scan->xs_itupdesc = RelationGetDescr(scan->indexRelation);
		scan->xs_itup = index_form_tuple(scan->xs_itupdesc, values, nulls);
	}
	return true;
}

static void
am_endscan(IndexScanDesc scan)
{
	end_go_scan(scan);
	pfree(scan->opaque);
	scan->opaque = NULL;
}

/* ---- The handler ------------------------------------------------------------- */

/* The access method whose handler is the function being called. */
static AmSlot *
handler_slot(FunctionCallInfo fcinfo)
{
	Relation	rel;
	ScanKeyData key;
	SysScanDesc scan;
	HeapTuple	tup;
	char	   *name = NULL;
	AmSlot	   *s;

	if (fcinfo->flinfo == NULL)
		elog(ERROR, "gopgext_am_handler must be called through its SQL function");
	if (fcinfo->flinfo->fn_extra)
		return fcinfo->flinfo->fn_extra;
	rel = table_open(AccessMethodRelationId, AccessShareLock);
	ScanKeyInit(&key, Anum_pg_am_amhandler, BTEqualStrategyNumber, F_OIDEQ,
				ObjectIdGetDatum(fcinfo->flinfo->fn_oid));
	scan = systable_beginscan(rel, InvalidOid, false, NULL, 1, &key);
	while (HeapTupleIsValid(tup = systable_getnext(scan)))
	{
		if (name != NULL)
			ereport(ERROR,
					(errcode(ERRCODE_OBJECT_NOT_IN_PREREQUISITE_STATE),
					 errmsg("access methods \"%s\" and \"%s\" share a handler function",
							name, NameStr(((Form_pg_am) GETSTRUCT(tup))->amname)),
					 errhint("Create one handler function per access method.")));
		name = pstrdup(NameStr(((Form_pg_am) GETSTRUCT(tup))->amname));
	}
	systable_endscan(scan);
	table_close(rel, AccessShareLock);
	if (name == NULL)
		ereport(ERROR,
				(errcode(ERRCODE_OBJECT_NOT_IN_PREREQUISITE_STATE),
				 errmsg("function %u is not the handler of an access method", fcinfo->flinfo->fn_oid)));
	s = slot_by_name(name);
	fcinfo->flinfo->fn_extra = s;
	return s;
}

PG_FUNCTION_INFO_V1(gopgext_am_handler);

Datum
gopgext_am_handler(PG_FUNCTION_ARGS)
{
	AmSlot	   *s = handler_slot(fcinfo);
	IndexAmRoutine *r = makeNode(IndexAmRoutine);

	if (!xact_callback_registered)
	{
		RegisterXactCallback(am_xact_callback, NULL);
		xact_callback_registered = true;
	}

	r->amstrategies = 0;
	r->amsupport = s->support;
	r->amoptsprocnum = 0;
	r->amcanorder = false;
	r->amcanorderbyop = s->orderby;
	r->amcanbackward = false;
	r->amcanunique = false;
	r->amcanmulticol = s->multicol;
	r->amoptionalkey = true;
	r->amsearcharray = false;
	r->amsearchnulls = false;
	r->amstorage = false;
	r->amclusterable = false;
	r->ampredlocks = false;
	r->amcanparallel = false;
#if PG_VERSION_NUM >= 170000
	r->amcanbuildparallel = false;
#endif
	r->amcaninclude = false;
	r->amusemaintenanceworkmem = false;
#if PG_VERSION_NUM >= 160000
	r->amsummarizing = false;
#endif
	r->amparallelvacuumoptions = VACUUM_OPTION_NO_PARALLEL;
	r->amkeytype = InvalidOid;

	r->ambuild = am_build;
	r->ambuildempty = am_buildempty;
	r->aminsert = am_insert;
	r->ambulkdelete = am_bulkdelete;
	r->amvacuumcleanup = am_vacuumcleanup;
	r->amcanreturn = NULL;
	r->amcostestimate = am_costestimate;
	r->amoptions = am_options_fns[s - slots];
	r->amproperty = NULL;
	r->ambuildphasename = NULL;
	r->amvalidate = am_validate;
	r->amadjustmembers = NULL;
	r->ambeginscan = am_beginscan;
	r->amrescan = am_rescan;
	r->amgettuple = am_gettuple;
	r->amgetbitmap = NULL;
	r->amendscan = am_endscan;
	r->ammarkpos = NULL;
	r->amrestrpos = NULL;
	r->amestimateparallelscan = NULL;
	r->aminitparallelscan = NULL;
	r->amparallelrescan = NULL;

	PG_RETURN_POINTER(r);
}
