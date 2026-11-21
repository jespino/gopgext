/*
 * C side of gopgext/gin: the support functions of GIN operator classes,
 * implemented by the gin.OpClass the extension registers in Go.
 *
 * As in gopgext/gist, the extension's sql/ files declare the support
 * functions with these C symbols, and each call is dispatched to the
 * OpClass registered under the name of the operator family the function
 * belongs to (looked up in pg_amproc once per FmgrInfo).
 *
 * Indexed values, queries and keys are varlena values. Go gets plain values
 * (bytes, arrays of flags) and returns plain values: GIN's arrays are
 * allocated here, with gopgext_gin_copy.
 */
#include "postgres.h"

#include "access/genam.h"
#include "access/gin.h"
#include "access/htup_details.h"
#include "access/stratnum.h"
#include "access/table.h"
#include "catalog/pg_amproc.h"
#include "catalog/pg_opfamily.h"
#include "utils/fmgroids.h"
#include "utils/syscache.h"

#include "gopgext.h"

/* Go (gopgext/gin), called through gopgext_invoke. */
void		gopgext_go_gin_compare(struct gopgext_call *call);
void		gopgext_go_gin_extract_value(struct gopgext_call *call);
void		gopgext_go_gin_extract_query(struct gopgext_call *call);
void		gopgext_go_gin_consistent(struct gopgext_call *call);
void		gopgext_go_gin_compare_partial(struct gopgext_call *call);
void		gopgext_go_gin_tri_consistent(struct gopgext_call *call);

/* Called by Go: a palloc'd copy of size bytes at src (*out). */
ErrorData  *gopgext_gin_copy(const void *src, int64 size, void **out);

ErrorData *
gopgext_gin_copy(const void *src, int64 size, void **out)
{
	GOPGEXT_GUARD({
		void	   *p = palloc(Max(size, 1));

		if (size > 0)
			memcpy(p, src, size);
		*out = p;
	});
}

/* The name of the operator family of the support function being called. */
static char *
gin_family(FunctionCallInfo fcinfo)
{
	FmgrInfo   *flinfo = fcinfo->flinfo;
	Relation	rel;
	ScanKeyData key;
	SysScanDesc scan;
	HeapTuple	tup;
	Oid			family = InvalidOid;
	char	   *name;

	if (flinfo == NULL)
		elog(ERROR, "gopgext/gin support functions must be called by GIN");
	if (flinfo->fn_extra)
		return flinfo->fn_extra;

	rel = table_open(AccessMethodProcedureRelationId, AccessShareLock);
	ScanKeyInit(&key, Anum_pg_amproc_amproc, BTEqualStrategyNumber, F_OIDEQ,
				ObjectIdGetDatum(flinfo->fn_oid));
	scan = systable_beginscan(rel, InvalidOid, false, NULL, 1, &key);
	if (HeapTupleIsValid(tup = systable_getnext(scan)))
		family = ((Form_pg_amproc) GETSTRUCT(tup))->amprocfamily;
	systable_endscan(scan);
	table_close(rel, AccessShareLock);
	if (!OidIsValid(family))
		ereport(ERROR,
				(errcode(ERRCODE_OBJECT_NOT_IN_PREREQUISITE_STATE),
				 errmsg("function %u is not a support function of a GIN operator class",
						flinfo->fn_oid)));

	tup = SearchSysCache1(OPFAMILYOID, ObjectIdGetDatum(family));
	if (!HeapTupleIsValid(tup))
		elog(ERROR, "cache lookup failed for operator family %u", family);
	name = MemoryContextStrdup(flinfo->fn_mcxt, NameStr(((Form_pg_opfamily) GETSTRUCT(tup))->opfname));
	ReleaseSysCache(tup);
	flinfo->fn_extra = name;
	return name;
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

PG_FUNCTION_INFO_V1(gopgext_gin_compare);
PG_FUNCTION_INFO_V1(gopgext_gin_extract_value);
PG_FUNCTION_INFO_V1(gopgext_gin_extract_query);
PG_FUNCTION_INFO_V1(gopgext_gin_consistent);
PG_FUNCTION_INFO_V1(gopgext_gin_compare_partial);
PG_FUNCTION_INFO_V1(gopgext_gin_tri_consistent);

/* compare(key, key) RETURNS integer: orders keys. */
Datum
gopgext_gin_compare(PG_FUNCTION_ARGS)
{
	Datum		args[3];

	args[0] = CStringGetDatum(gin_family(fcinfo));
	args[1] = PG_GETARG_DATUM(0);
	args[2] = PG_GETARG_DATUM(1);
	return Int32GetDatum((int32) DatumGetInt64(call_go(gopgext_go_gin_compare, 3, args)));
}

/* extractValue(value, internal, internal) RETURNS internal: keys of a value. */
Datum
gopgext_gin_extract_value(PG_FUNCTION_ARGS)
{
	int32	   *nkeys = (int32 *) PG_GETARG_POINTER(1);
	Datum	   *keys = NULL;
	int64		n = 0;
	Datum		args[4];

	args[0] = CStringGetDatum(gin_family(fcinfo));
	args[1] = PG_GETARG_DATUM(0);
	args[2] = PointerGetDatum(&keys);
	args[3] = PointerGetDatum(&n);
	call_go(gopgext_go_gin_extract_value, 4, args);
	*nkeys = (int32) n;
	PG_RETURN_POINTER(keys);
}

/*
 * extractQuery(query, internal, smallint, internal, internal, internal,
 * internal) RETURNS internal: the keys a query looks for, which of them
 * are partial matches, and the search mode.
 */
Datum
gopgext_gin_extract_query(PG_FUNCTION_ARGS)
{
	int32	   *nkeys = (int32 *) PG_GETARG_POINTER(1);
	bool	  **pmatch = (bool **) PG_GETARG_POINTER(3);
	int32	   *searchMode = (int32 *) PG_GETARG_POINTER(6);
	Datum	   *keys = NULL;
	bool	   *partial = NULL;
	int64		n = 0;
	int64		mode = GIN_SEARCH_MODE_DEFAULT;
	Datum		args[7];

	args[0] = CStringGetDatum(gin_family(fcinfo));
	args[1] = PG_GETARG_DATUM(0);
	args[2] = Int64GetDatum(PG_GETARG_UINT16(2));
	args[3] = PointerGetDatum(&keys);
	args[4] = PointerGetDatum(&n);
	args[5] = PointerGetDatum(&partial);
	args[6] = PointerGetDatum(&mode);
	call_go(gopgext_go_gin_extract_query, 7, args);
	*nkeys = (int32) n;
	if (partial)
		*pmatch = partial;
	*searchMode = (int32) mode;
	PG_RETURN_POINTER(keys);
}

/*
 * consistent(internal, smallint, query, integer, internal, internal,
 * internal, internal) RETURNS boolean: does an item with the keys marked
 * in check match the query?
 */
Datum
gopgext_gin_consistent(PG_FUNCTION_ARGS)
{
	bool	   *recheck = (bool *) PG_GETARG_POINTER(5);
	Datum		args[6];

	*recheck = false;
	args[0] = CStringGetDatum(gin_family(fcinfo));
	args[1] = PG_GETARG_DATUM(0);	/* bool check[] */
	args[2] = Int64GetDatum(PG_GETARG_INT32(3));
	args[3] = Int64GetDatum(PG_GETARG_UINT16(1));
	args[4] = PG_GETARG_DATUM(2);
	args[5] = PointerGetDatum(recheck);
	return call_go(gopgext_go_gin_consistent, 6, args);
}

/*
 * comparePartial(key, key, smallint, internal) RETURNS integer: does an
 * indexed key match a partial-match query key (0), come before matches (<0)
 * or end the matches (>0)?
 */
Datum
gopgext_gin_compare_partial(PG_FUNCTION_ARGS)
{
	Datum		args[4];

	args[0] = CStringGetDatum(gin_family(fcinfo));
	args[1] = PG_GETARG_DATUM(0);
	args[2] = PG_GETARG_DATUM(1);
	args[3] = Int64GetDatum(PG_GETARG_UINT16(2));
	return Int32GetDatum((int32) DatumGetInt64(call_go(gopgext_go_gin_compare_partial, 4, args)));
}

/*
 * triConsistent(internal, smallint, query, integer, internal, internal,
 * internal) RETURNS "char": consistent with keys that may be present.
 */
Datum
gopgext_gin_tri_consistent(PG_FUNCTION_ARGS)
{
	Datum		args[5];

	args[0] = CStringGetDatum(gin_family(fcinfo));
	args[1] = PG_GETARG_DATUM(0);	/* GinTernaryValue check[] */
	args[2] = Int64GetDatum(PG_GETARG_INT32(3));
	args[3] = Int64GetDatum(PG_GETARG_UINT16(1));
	args[4] = PG_GETARG_DATUM(2);
	PG_RETURN_GIN_TERNARY_VALUE((GinTernaryValue) DatumGetInt64(call_go(gopgext_go_gin_tri_consistent, 5, args)));
}
