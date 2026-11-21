/*
 * C side of gopgext/gist: the support functions of GiST operator classes,
 * implemented by the gist.OpClass the extension registers in Go.
 *
 * The extension's sql/ files declare the support functions with these C
 * symbols and their operator classes; one set of symbols serves every
 * operator class of the extension: each call is dispatched to the OpClass
 * registered under the name of the operator family the function belongs to
 * (looked up in pg_amproc once per FmgrInfo).
 *
 * Go sees keys, indexed values and queries as bytes: varlena values as they
 * are, values of fixed-size types copied into a varlena (keys Go returns are
 * converted back to the key type).
 * Go gets plain values, never GiST structures: the arguments of each call
 * are built here (with a local FunctionCallInfo), and results are written
 * back into GiST's structures here.
 */
#include "postgres.h"

#include "access/genam.h"
#include "access/gist.h"
#include "access/htup_details.h"
#include "access/reloptions.h"
#include "access/stratnum.h"
#include "access/table.h"
#include "catalog/pg_amproc.h"
#include "catalog/pg_opclass.h"
#include "catalog/pg_opfamily.h"
#include "utils/fmgroids.h"
#include "utils/lsyscache.h"
#include "utils/syscache.h"

#include "gopgext.h"

/* Go (gopgext/gist), called through gopgext_invoke. */
void		gopgext_go_gist_compress(struct gopgext_call *call);
void		gopgext_go_gist_consistent(struct gopgext_call *call);
void		gopgext_go_gist_union(struct gopgext_call *call);
void		gopgext_go_gist_penalty(struct gopgext_call *call);
void		gopgext_go_gist_picksplit(struct gopgext_call *call);
void		gopgext_go_gist_same(struct gopgext_call *call);
void		gopgext_go_gist_distance(struct gopgext_call *call);
void		gopgext_go_gist_options(struct gopgext_call *call);

/* Called by Go: operator class options (gist.OpClass.Options). */
ErrorData  *gopgext_gist_init_options(void *relopts, int64 size);
ErrorData  *gopgext_gist_add_option(void *relopts, const char *name, const char *desc,
									int64 def, int64 min, int64 max, int64 offset);
bool		gopgext_gist_option(int64 i, int64 *out);

/*
 * The options of the index the current support function serves (a bytea:
 * an int32 per option after the header), NULL if it has none.
 */
static bytea *gist_options;

/*
 * What a support function needs to know about its operator class: its
 * name (the family's), and the types of keys (STORAGE, or the indexed
 * type) and indexed values. Cached in fn_extra.
 */
typedef struct GistOpInfo
{
	char	   *family;
	Oid			intype;			/* indexed type: values, and queries by default */
	int16		keylen;
	bool		keybyval;
	int16		vallen;
	bool		valbyval;
	Oid			subtype;		/* last query type of another type */
	int16		sublen;
	bool		subbyval;
} GistOpInfo;

static GistOpInfo *
gist_info(FunctionCallInfo fcinfo)
{
	FmgrInfo   *flinfo = fcinfo->flinfo;
	Relation	rel;
	ScanKeyData key[2];
	SysScanDesc scan;
	HeapTuple	tup;
	Oid			family = InvalidOid;
	Oid			intype = InvalidOid;
	Oid			keytype = InvalidOid;
	GistOpInfo *info;

	if (flinfo == NULL)
		elog(ERROR, "gopgext/gist support functions must be called by GiST");
	if (flinfo->fn_extra)
		return flinfo->fn_extra;

	rel = table_open(AccessMethodProcedureRelationId, AccessShareLock);
	ScanKeyInit(&key[0], Anum_pg_amproc_amproc, BTEqualStrategyNumber, F_OIDEQ,
				ObjectIdGetDatum(flinfo->fn_oid));
	scan = systable_beginscan(rel, InvalidOid, false, NULL, 1, key);
	if (HeapTupleIsValid(tup = systable_getnext(scan)))
	{
		family = ((Form_pg_amproc) GETSTRUCT(tup))->amprocfamily;
		intype = ((Form_pg_amproc) GETSTRUCT(tup))->amproclefttype;
	}
	systable_endscan(scan);
	table_close(rel, AccessShareLock);
	if (!OidIsValid(family))
		ereport(ERROR,
				(errcode(ERRCODE_OBJECT_NOT_IN_PREREQUISITE_STATE),
				 errmsg("function %u is not a support function of a GiST operator class",
						flinfo->fn_oid)));

	/* The key type: the STORAGE of the family's operator class for intype. */
	rel = table_open(OperatorClassRelationId, AccessShareLock);
	scan = systable_beginscan(rel, InvalidOid, false, NULL, 0, NULL);
	while (HeapTupleIsValid(tup = systable_getnext(scan)))
	{
		Form_pg_opclass opc = (Form_pg_opclass) GETSTRUCT(tup);

		if (opc->opcfamily == family && opc->opcintype == intype)
		{
			keytype = opc->opckeytype;
			break;
		}
	}
	systable_endscan(scan);
	table_close(rel, AccessShareLock);
	if (!OidIsValid(keytype))
		keytype = intype;

	info = MemoryContextAllocZero(flinfo->fn_mcxt, sizeof(GistOpInfo));
	tup = SearchSysCache1(OPFAMILYOID, ObjectIdGetDatum(family));
	if (!HeapTupleIsValid(tup))
		elog(ERROR, "cache lookup failed for operator family %u", family);
	info->family = MemoryContextStrdup(flinfo->fn_mcxt, NameStr(((Form_pg_opfamily) GETSTRUCT(tup))->opfname));
	ReleaseSysCache(tup);
	info->intype = intype;
	get_typlenbyval(keytype, &info->keylen, &info->keybyval);
	get_typlenbyval(intype, &info->vallen, &info->valbyval);
	flinfo->fn_extra = info;
	return info;
}

/*
 * A value as Go sees it: a varlena holding its bytes. Variable-size values
 * are passed as they are; fixed-size ones are copied (by-value ones as
 * stored in a tuple, i.e. little-endian), cstrings without their NUL.
 */
static Datum
to_bytes(Datum d, int16 typlen, bool byval)
{
	struct varlena *v;
	Size		n;

	if (typlen == -1)
		return d;
	n = typlen == -2 ? strlen(DatumGetCString(d)) : (Size) typlen;
	v = palloc(VARHDRSZ + n);
	SET_VARSIZE(v, VARHDRSZ + n);
	if (byval)
		store_att_byval(VARDATA(v), d, typlen);
	else
		memcpy(VARDATA(v), DatumGetPointer(d), n);
	return PointerGetDatum(v);
}

/* A key Go returned (a varlena of its bytes) as a Datum of the key type. */
static Datum
key_from_bytes(GistOpInfo *info, Datum d)
{
	struct varlena *v = (struct varlena *) DatumGetPointer(d);
	char	   *p;

	if (info->keylen == -1)
		return d;
	if (info->keylen < 0 || VARSIZE_ANY_EXHDR(v) != (Size) info->keylen)
		ereport(ERROR,
				(errcode(ERRCODE_DATA_EXCEPTION),
				 errmsg("GiST key of operator family \"%s\" has %zu bytes, its type needs %d",
						info->family, VARSIZE_ANY_EXHDR(v), info->keylen)));
	if (info->keybyval)
		return fetch_att(VARDATA_ANY(v), true, info->keylen);
	p = palloc(info->keylen);
	memcpy(p, VARDATA_ANY(v), info->keylen);
	return PointerGetDatum(p);
}

static Datum
key_bytes(GistOpInfo *info, Datum d)
{
	return to_bytes(d, info->keylen, info->keybyval);
}

/* A query: its type is the operator's right-hand type (subtype). */
static Datum
query_bytes(GistOpInfo *info, Datum d, Oid subtype)
{
	if (!OidIsValid(subtype) || subtype == info->intype)
		return to_bytes(d, info->vallen, info->valbyval);
	if (subtype != info->subtype)
	{
		get_typlenbyval(subtype, &info->sublen, &info->subbyval);
		info->subtype = subtype;
	}
	return to_bytes(d, info->sublen, info->subbyval);
}

/* Calls Go function fn with nargs Datum arguments (none NULL). */
static Datum
call_go(FunctionCallInfo outer, gopgext_gofn fn, bool *isnull, int nargs, const Datum *args)
{
	LOCAL_FCINFO(fc, 8);
	Datum		result;
	bytea	   *saved = gist_options;

	Assert(nargs <= 8);
	InitFunctionCallInfoData(*fc, NULL, nargs, InvalidOid, NULL, NULL);
	for (int i = 0; i < nargs; i++)
	{
		fc->args[i].value = args[i];
		fc->args[i].isnull = false;
	}
	gist_options = NULL;
	if (outer && outer->flinfo && has_fn_opclass_options(outer->flinfo))
		gist_options = get_fn_opclass_options(outer->flinfo);
	PG_TRY();
	{
		result = gopgext_invoke(fc, fn);
	}
	PG_FINALLY();
	{
		gist_options = saved;
	}
	PG_END_TRY();
	if (isnull)
		*isnull = fc->isnull;
	return result;
}

PG_FUNCTION_INFO_V1(gopgext_gist_compress);
PG_FUNCTION_INFO_V1(gopgext_gist_consistent);
PG_FUNCTION_INFO_V1(gopgext_gist_union);
PG_FUNCTION_INFO_V1(gopgext_gist_penalty);
PG_FUNCTION_INFO_V1(gopgext_gist_picksplit);
PG_FUNCTION_INFO_V1(gopgext_gist_same);
PG_FUNCTION_INFO_V1(gopgext_gist_distance);
PG_FUNCTION_INFO_V1(gopgext_gist_decompress);
PG_FUNCTION_INFO_V1(gopgext_gist_options);

/*
 * decompress(internal) RETURNS internal: does nothing (Go reads keys as they
 * are stored). Only for install scripts that declare one (support function
 * 4 is optional).
 */
Datum
gopgext_gist_decompress(PG_FUNCTION_ARGS)
{
	PG_RETURN_POINTER(PG_GETARG_POINTER(0));
}

/* compress(internal) RETURNS internal: an indexed value becomes its key. */
Datum
gopgext_gist_compress(PG_FUNCTION_ARGS)
{
	GISTENTRY  *entry = (GISTENTRY *) PG_GETARG_POINTER(0);
	GISTENTRY  *retval;
	Datum		args[2];
	Datum		key;
	bool		isnull;

	GistOpInfo *info;

	if (!entry->leafkey)
		PG_RETURN_POINTER(entry);	/* already a key (union of keys) */
	info = gist_info(fcinfo);
	args[0] = CStringGetDatum(info->family);
	args[1] = to_bytes(entry->key, info->vallen, info->valbyval);
	key = call_go(fcinfo, gopgext_go_gist_compress, &isnull, 2, args);
	if (isnull)
		PG_RETURN_POINTER(entry);	/* no Compress: the value is the key */
	retval = palloc(sizeof(GISTENTRY));
	gistentryinit(*retval, key_from_bytes(info, key), entry->rel, entry->page, entry->offset, false);
	PG_RETURN_POINTER(retval);
}

/*
 * consistent(internal, query, smallint, oid, internal) RETURNS boolean:
 * can the key match the query?
 */
Datum
gopgext_gist_consistent(PG_FUNCTION_ARGS)
{
	GISTENTRY  *entry = (GISTENTRY *) PG_GETARG_POINTER(0);
	bool	   *recheck = (bool *) PG_GETARG_POINTER(4);
	GistOpInfo *info = gist_info(fcinfo);
	Datum		args[6];

	*recheck = false;
	args[0] = CStringGetDatum(info->family);
	args[1] = key_bytes(info, entry->key);
	args[2] = query_bytes(info, PG_GETARG_DATUM(1), PG_GETARG_OID(3));
	args[3] = Int64GetDatum(PG_GETARG_UINT16(2));
	args[4] = BoolGetDatum(GIST_LEAF(entry));
	args[5] = PointerGetDatum(recheck);
	return call_go(fcinfo, gopgext_go_gist_consistent, NULL, 6, args);
}

/* union(internal, internal) RETURNS key type: the union of the keys. */
Datum
gopgext_gist_union(PG_FUNCTION_ARGS)
{
	GistEntryVector *entryvec = (GistEntryVector *) PG_GETARG_POINTER(0);
	int		   *size = (int *) PG_GETARG_POINTER(1);
	Datum	   *keys = palloc(sizeof(Datum) * Max(entryvec->n, 1));
	GistOpInfo *info = gist_info(fcinfo);
	Datum		args[3];
	Datum		result;

	for (int i = 0; i < entryvec->n; i++)
		keys[i] = key_bytes(info, entryvec->vector[i].key);
	args[0] = CStringGetDatum(info->family);
	args[1] = Int64GetDatum(entryvec->n);
	args[2] = PointerGetDatum(keys);
	result = key_from_bytes(info, call_go(fcinfo, gopgext_go_gist_union, NULL, 3, args));
	*size = info->keylen == -1 ? VARSIZE_ANY(DatumGetPointer(result)) : info->keylen;
	pfree(keys);
	PG_RETURN_DATUM(result);
}

/*
 * penalty(internal, internal, internal) RETURNS internal: the cost of adding
 * a key to a subtree.
 */
Datum
gopgext_gist_penalty(PG_FUNCTION_ARGS)
{
	GISTENTRY  *orig = (GISTENTRY *) PG_GETARG_POINTER(0);
	GISTENTRY  *add = (GISTENTRY *) PG_GETARG_POINTER(1);
	float	   *result = (float *) PG_GETARG_POINTER(2);
	GistOpInfo *info = gist_info(fcinfo);
	Datum		args[3];

	args[0] = CStringGetDatum(info->family);
	args[1] = key_bytes(info, orig->key);
	args[2] = key_bytes(info, add->key);
	*result = (float) DatumGetFloat8(call_go(fcinfo, gopgext_go_gist_penalty, NULL, 3, args));
	PG_RETURN_POINTER(result);
}

/*
 * picksplit(internal, internal) RETURNS internal: splits the keys of a full
 * page in two (entries 1..n-1 of the vector), with the union of each side.
 */
Datum
gopgext_gist_picksplit(PG_FUNCTION_ARGS)
{
	GistEntryVector *entryvec = (GistEntryVector *) PG_GETARG_POINTER(0);
	GIST_SPLITVEC *v = (GIST_SPLITVEC *) PG_GETARG_POINTER(1);
	int			n = entryvec->n - 1;
	Datum	   *keys = palloc(sizeof(Datum) * Max(n, 1));
	int32	   *sides = palloc0(sizeof(int32) * Max(n, 1));
	Datum		lunion = (Datum) 0;
	Datum		runion = (Datum) 0;
	GistOpInfo *info = gist_info(fcinfo);
	Datum		args[6];

	for (int i = 0; i < n; i++)
		keys[i] = key_bytes(info, entryvec->vector[i + FirstOffsetNumber].key);
	args[0] = CStringGetDatum(info->family);
	args[1] = Int64GetDatum(n);
	args[2] = PointerGetDatum(keys);
	args[3] = PointerGetDatum(sides);
	args[4] = PointerGetDatum(&lunion);
	args[5] = PointerGetDatum(&runion);
	call_go(fcinfo, gopgext_go_gist_picksplit, NULL, 6, args);

	v->spl_left = palloc(sizeof(OffsetNumber) * Max(n, 1));
	v->spl_right = palloc(sizeof(OffsetNumber) * Max(n, 1));
	v->spl_nleft = v->spl_nright = 0;
	for (int i = 0; i < n; i++)
	{
		if (sides[i] == 0)
			v->spl_left[v->spl_nleft++] = i + FirstOffsetNumber;
		else
			v->spl_right[v->spl_nright++] = i + FirstOffsetNumber;
	}
	v->spl_ldatum = key_from_bytes(info, lunion);
	v->spl_rdatum = key_from_bytes(info, runion);
	pfree(keys);
	pfree(sides);
	PG_RETURN_POINTER(v);
}

/* same(key, key, internal) RETURNS internal: are two keys equal? */
Datum
gopgext_gist_same(PG_FUNCTION_ARGS)
{
	bool	   *result = (bool *) PG_GETARG_POINTER(2);
	GistOpInfo *info = gist_info(fcinfo);
	Datum		args[3];

	args[0] = CStringGetDatum(info->family);
	args[1] = key_bytes(info, PG_GETARG_DATUM(0));
	args[2] = key_bytes(info, PG_GETARG_DATUM(1));
	*result = DatumGetBool(call_go(fcinfo, gopgext_go_gist_same, NULL, 3, args));
	PG_RETURN_POINTER(result);
}

/*
 * distance(internal, query, smallint, oid, internal) RETURNS float8: the
 * distance from a key to the query, for ORDER BY (k-nearest neighbors).
 */
Datum
gopgext_gist_distance(PG_FUNCTION_ARGS)
{
	GISTENTRY  *entry = (GISTENTRY *) PG_GETARG_POINTER(0);
	bool	   *recheck = (bool *) PG_GETARG_POINTER(4);
	GistOpInfo *info = gist_info(fcinfo);
	Datum		args[6];

	*recheck = false;
	args[0] = CStringGetDatum(info->family);
	args[1] = key_bytes(info, entry->key);
	args[2] = query_bytes(info, PG_GETARG_DATUM(1), PG_GETARG_OID(3));
	args[3] = Int64GetDatum(PG_GETARG_UINT16(2));
	args[4] = BoolGetDatum(GIST_LEAF(entry));
	args[5] = PointerGetDatum(recheck);
	return call_go(fcinfo, gopgext_go_gist_distance, NULL, 6, args);
}

/*
 * options(internal) RETURNS void: declares the operator class parameters
 * (gist.OpClass.Options), stored as an int32 each.
 */
Datum
gopgext_gist_options(PG_FUNCTION_ARGS)
{
	Datum		args[2];

	args[0] = CStringGetDatum(gist_info(fcinfo)->family);
	args[1] = PG_GETARG_DATUM(0);
	call_go(NULL, gopgext_go_gist_options, NULL, 2, args);
	PG_RETURN_VOID();
}

ErrorData *
gopgext_gist_init_options(void *relopts, int64 size)
{
	GOPGEXT_GUARD(init_local_reloptions((local_relopts *) relopts, (Size) size));
}

ErrorData *
gopgext_gist_add_option(void *relopts, const char *name, const char *desc,
						int64 def, int64 min, int64 max, int64 offset)
{
	GOPGEXT_GUARD(add_local_int_reloption((local_relopts *) relopts, name, desc,
										  (int) def, (int) min, (int) max, (int) offset));
}

/* Option i of the current index: false if the index has no options. */
bool
gopgext_gist_option(int64 i, int64 *out)
{
	if (gist_options == NULL ||
		VARSIZE(gist_options) < VARHDRSZ + (i + 1) * sizeof(int32))
		return false;
	*out = ((int32 *) VARDATA(gist_options))[i];
	return true;
}
