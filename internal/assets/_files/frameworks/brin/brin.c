/*
 * C side of gopgext/brin: the support functions of BRIN operator classes,
 * implemented by the brin.OpClass the extension registers in Go.
 *
 * The extension's sql/ files declare the support functions with these C
 * symbols and their operator classes; one set of symbols serves every
 * operator class of the extension: each call is dispatched to the OpClass
 * registered under the name of the operator family of the index column
 * (cached in fn_extra).
 *
 * Each block range is summarized by one bytea value that Go builds and
 * reads (oi_nstored = 1). NULLs are handled here and by BRIN itself
 * (oi_regular_nulls): Go only sees non-NULL values and summaries of ranges
 * that have at least one. Values and queries reach Go as bytes: the
 * contents of variable-size values, the bytes of fixed-size ones (by-value
 * types in native byte order).
 */
#include "postgres.h"

#include "access/brin_internal.h"
#include "access/brin_tuple.h"
#include "access/htup_details.h"
#include "access/skey.h"
#include "catalog/pg_opfamily.h"
#include "catalog/pg_type.h"
#include "utils/datum.h"
#include "utils/lsyscache.h"
#include "utils/rel.h"
#include "utils/syscache.h"
#include "utils/typcache.h"

#include "gopgext.h"

/* Go (gopgext/brin), called through gopgext_invoke. */
void		gopgext_go_brin_add(struct gopgext_call *call);
void		gopgext_go_brin_consistent(struct gopgext_call *call);
void		gopgext_go_brin_union(struct gopgext_call *call);

/* The operator family name of an index column, cached per FmgrInfo. */
struct brin_family
{
	Oid			oid;
	char	   *name;
};

static const char *
brin_family(FunctionCallInfo fcinfo, BrinDesc *bdesc, AttrNumber attno)
{
	FmgrInfo   *flinfo = fcinfo->flinfo;
	Oid			family = bdesc->bd_index->rd_opfamily[attno - 1];
	struct brin_family *cache = flinfo ? flinfo->fn_extra : NULL;
	HeapTuple	tup;

	if (cache && cache->oid == family)
		return cache->name;
	tup = SearchSysCache1(OPFAMILYOID, ObjectIdGetDatum(family));
	if (!HeapTupleIsValid(tup))
		elog(ERROR, "cache lookup failed for operator family %u", family);
	if (flinfo == NULL)
	{
		char	   *name = pstrdup(NameStr(((Form_pg_opfamily) GETSTRUCT(tup))->opfname));

		ReleaseSysCache(tup);
		return name;
	}
	if (cache == NULL)
		cache = flinfo->fn_extra = MemoryContextAllocZero(flinfo->fn_mcxt, sizeof(*cache));
	cache->oid = family;
	cache->name = MemoryContextStrdup(flinfo->fn_mcxt,
									  NameStr(((Form_pg_opfamily) GETSTRUCT(tup))->opfname));
	ReleaseSysCache(tup);
	return cache->name;
}

/*
 * The bytes of a value of type typid: the contents of a variable-size value
 * (detoasted), a C string, or the bytes of a fixed-size value (*tmp holds a
 * by-value Datum).
 */
static void
datum_bytes(Datum d, Oid typid, Datum *tmp, const char **p, int64 *len)
{
	int16		typlen;
	bool		byval;

	get_typlenbyval(typid, &typlen, &byval);
	if (typlen == -1)
	{
		struct varlena *v = pg_detoast_datum_packed((struct varlena *) DatumGetPointer(d));

		*p = VARDATA_ANY(v);
		*len = VARSIZE_ANY_EXHDR(v);
	}
	else if (typlen == -2)
	{
		*p = DatumGetCString(d);
		*len = strlen(*p);
	}
	else if (byval)
	{
		*tmp = d;
		*p = (const char *) tmp;
		*len = typlen;
	}
	else
	{
		*p = DatumGetPointer(d);
		*len = typlen;
	}
}

/* The bytes of a stored summary (bytea). */
static void
summary_bytes(Datum d, const char **p, int64 *len)
{
	struct varlena *v = pg_detoast_datum_packed((struct varlena *) DatumGetPointer(d));

	*p = VARDATA_ANY(v);
	*len = VARSIZE_ANY_EXHDR(v);
}

/* Calls Go function fn with nargs Datum arguments (none NULL). */
static Datum
call_go(gopgext_gofn fn, bool *isnull, int nargs, const Datum *args)
{
	LOCAL_FCINFO(fc, 8);
	Datum		result;

	Assert(nargs <= 8);
	InitFunctionCallInfoData(*fc, NULL, nargs, InvalidOid, NULL, NULL);
	for (int i = 0; i < nargs; i++)
	{
		fc->args[i].value = args[i];
		fc->args[i].isnull = false;
	}
	result = gopgext_invoke(fc, fn);
	if (isnull)
		*isnull = fc->isnull;
	return result;
}

static Oid
column_type(BrinDesc *bdesc, AttrNumber attno)
{
	/* The operator class's input type (the index column's type may be its STORAGE). */
	return bdesc->bd_index->rd_opcintype[attno - 1];
}

PG_FUNCTION_INFO_V1(gopgext_brin_opcinfo);
PG_FUNCTION_INFO_V1(gopgext_brin_add_value);
PG_FUNCTION_INFO_V1(gopgext_brin_consistent);
PG_FUNCTION_INFO_V1(gopgext_brin_union);

/* opcinfo(internal) RETURNS internal: one bytea summary per range. */
Datum
gopgext_brin_opcinfo(PG_FUNCTION_ARGS)
{
	BrinOpcInfo *result = palloc0(SizeofBrinOpcInfo(1));

	result->oi_nstored = 1;
	result->oi_regular_nulls = true;
	result->oi_typcache[0] = lookup_type_cache(BYTEAOID, 0);
	PG_RETURN_POINTER(result);
}

/*
 * add_value(internal, internal, internal, internal) RETURNS boolean: adds a
 * (non-NULL) value to the summary of its range; true if it changed.
 */
Datum
gopgext_brin_add_value(PG_FUNCTION_ARGS)
{
	BrinDesc   *bdesc = (BrinDesc *) PG_GETARG_POINTER(0);
	BrinValues *column = (BrinValues *) PG_GETARG_POINTER(1);
	Datum		newval = PG_GETARG_DATUM(2);
	Datum		args[7];
	Datum		tmp;
	const char *p = NULL;
	int64		len = 0;
	Datum		summary;
	bool		isnull;

	Assert(!PG_GETARG_BOOL(3));
	args[0] = CStringGetDatum(brin_family(fcinfo, bdesc, column->bv_attno));
	if (!column->bv_allnulls)
		summary_bytes(column->bv_values[0], &p, &len);
	args[1] = PointerGetDatum(p);
	args[2] = Int64GetDatum(len);
	args[3] = BoolGetDatum(!column->bv_allnulls);
	datum_bytes(newval, column_type(bdesc, column->bv_attno), &tmp, &p, &len);
	args[4] = PointerGetDatum(p);
	args[5] = Int64GetDatum(len);
	summary = call_go(gopgext_go_brin_add, &isnull, 6, args);
	if (isnull)
		PG_RETURN_BOOL(false);	/* the summary already covers the value */
	if (!column->bv_allnulls)
		pfree(DatumGetPointer(column->bv_values[0]));
	column->bv_values[0] = summary;
	column->bv_allnulls = false;
	PG_RETURN_BOOL(true);
}

/*
 * consistent(internal, internal, internal, integer) RETURNS boolean: can
 * the range hold rows matching all the scan keys?
 */
Datum
gopgext_brin_consistent(PG_FUNCTION_ARGS)
{
	BrinDesc   *bdesc = (BrinDesc *) PG_GETARG_POINTER(0);
	BrinValues *column = (BrinValues *) PG_GETARG_POINTER(1);
	ScanKey    *keys = (ScanKey *) PG_GETARG_POINTER(2);
	int			nkeys = PG_GETARG_INT32(3);
	Oid			coltype = column_type(bdesc, column->bv_attno);
	int32	   *strategies = palloc(sizeof(int32) * Max(nkeys, 1));
	const char **ptrs = palloc(sizeof(char *) * Max(nkeys, 1));
	int64	   *lens = palloc(sizeof(int64) * Max(nkeys, 1));
	Datum	   *tmps = palloc(sizeof(Datum) * Max(nkeys, 1));
	const char *sp;
	int64		slen;
	Datum		args[8];
	Datum		result;

	Assert(!column->bv_allnulls);
	for (int i = 0; i < nkeys; i++)
	{
		ScanKey		key = keys[i];
		Oid			qtype = OidIsValid(key->sk_subtype) ? key->sk_subtype : coltype;

		Assert(!(key->sk_flags & SK_ISNULL));
		strategies[i] = key->sk_strategy;
		datum_bytes(key->sk_argument, qtype, &tmps[i], &ptrs[i], &lens[i]);
	}
	summary_bytes(column->bv_values[0], &sp, &slen);
	args[0] = CStringGetDatum(brin_family(fcinfo, bdesc, column->bv_attno));
	args[1] = PointerGetDatum(sp);
	args[2] = Int64GetDatum(slen);
	args[3] = Int64GetDatum(nkeys);
	args[4] = PointerGetDatum(strategies);
	args[5] = PointerGetDatum(ptrs);
	args[6] = PointerGetDatum(lens);
	result = call_go(gopgext_go_brin_consistent, NULL, 7, args);
	pfree(strategies);
	pfree(ptrs);
	pfree(lens);
	pfree(tmps);
	return result;
}

/*
 * union(internal, internal, internal) RETURNS boolean: merges the summary
 * of b into a (both have values: BRIN handles the others).
 */
Datum
gopgext_brin_union(PG_FUNCTION_ARGS)
{
	BrinDesc   *bdesc = (BrinDesc *) PG_GETARG_POINTER(0);
	BrinValues *col_a = (BrinValues *) PG_GETARG_POINTER(1);
	BrinValues *col_b = (BrinValues *) PG_GETARG_POINTER(2);
	Datum		args[5];
	const char *p;
	int64		len;

	Assert(!col_a->bv_allnulls && !col_b->bv_allnulls);
	args[0] = CStringGetDatum(brin_family(fcinfo, bdesc, col_a->bv_attno));
	summary_bytes(col_a->bv_values[0], &p, &len);
	args[1] = PointerGetDatum(p);
	args[2] = Int64GetDatum(len);
	summary_bytes(col_b->bv_values[0], &p, &len);
	args[3] = PointerGetDatum(p);
	args[4] = Int64GetDatum(len);
	col_a->bv_values[0] = call_go(gopgext_go_brin_union, NULL, 5, args);
	PG_RETURN_BOOL(true);
}
