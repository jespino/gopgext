/*
 * Services for gopgext/pg: the call's collation and argument types,
 * collation-aware string functions, composite values (records) and
 * multi-dimensional arrays. Like runtime.c, helpers that may raise an
 * error catch it (GOPGEXT_GUARD) and return it.
 */
#include "postgres.h"

#include "access/htup_details.h"
#include "access/tupdesc.h"
#include "fmgr.h"
#include "funcapi.h"
#include "utils/array.h"
#include "utils/builtins.h"
#include "utils/formatting.h"
#include "utils/lsyscache.h"
#include "utils/typcache.h"
#include "utils/varlena.h"

#include "gopgext.h"

/* pg.Collation reads FunctionCallInfoBaseData.fncollation directly. */
StaticAssertDecl(offsetof(FunctionCallInfoBaseData, fncollation) == 24, "fcinfo layout");

ErrorData  *gopgext_fn_argtype(FunctionCallInfo fcinfo, int64 i, Oid *out);
ErrorData  *gopgext_str_case(const char *p, int64 len, Oid coll, bool upper, char **out, int64 *outlen);
ErrorData  *gopgext_str_cmp(const char *a, int64 alen, const char *b, int64 blen, Oid coll, int64 *out);
ErrorData  *gopgext_type_is_rowtype(Oid typ, bool *out);

struct gopgext_column
{
	const char *name;
	Oid			type;
	int32		typmod;
	bool		dropped;
};

ErrorData  *gopgext_record_columns(Oid typ, int32 typmod, int64 *n, struct gopgext_column **out);
ErrorData  *gopgext_record_type(Datum d, Oid *typ, int32 *typmod);
ErrorData  *gopgext_record_fields(Datum d, int64 *n, Datum **values, bool **nulls, Oid **types);
ErrorData  *gopgext_record_new(Oid typ, int32 typmod, int64 n, Datum *values, bool *nulls, Datum *out);
ErrorData  *gopgext_input(Oid typ, int32 typmod, char *s, Datum *out, bool *isnull);
ErrorData  *gopgext_array_bounds(Datum d, int64 *ndim, int32 *dims, int32 *lbs);
ErrorData  *gopgext_array_new_md(Oid elemtype, int64 ndim, int32 *dims, int32 *lbs,
								 Datum *elems, bool *nulls, Datum *out);

/* The actual type of argument i of the call (0 if unknown). */
ErrorData *
gopgext_fn_argtype(FunctionCallInfo fcinfo, int64 i, Oid *out)
{
	GOPGEXT_GUARD({
		*out = InvalidOid;
		if (fcinfo && fcinfo->flinfo && i >= 0 && i < fcinfo->nargs)
			*out = get_fn_expr_argtype(fcinfo->flinfo, (int) i);
	});
}

/* lower()/upper() of p[0:len] with collation coll (str_tolower/str_toupper). */
ErrorData *
gopgext_str_case(const char *p, int64 len, Oid coll, bool upper, char **out, int64 *outlen)
{
	GOPGEXT_GUARD({
		char	   *r = upper ? str_toupper(p, len, coll) : str_tolower(p, len, coll);

		*out = r;
		*outlen = strlen(r);
	});
}

/* Compares two strings with collation coll, like text's < = > (varstr_cmp). */
ErrorData *
gopgext_str_cmp(const char *a, int64 alen, const char *b, int64 blen, Oid coll, int64 *out)
{
	GOPGEXT_GUARD({
		int			r = varstr_cmp(a ? a : "", (int) alen, b ? b : "", (int) blen, coll);

		*out = (r > 0) - (r < 0);
	});
}

ErrorData *
gopgext_type_is_rowtype(Oid typ, bool *out)
{
	GOPGEXT_GUARD(*out = type_is_rowtype(typ));
}

/* The columns of a composite type (or domain over one, or registered record). */
ErrorData *
gopgext_record_columns(Oid typ, int32 typmod, int64 *n, struct gopgext_column **out)
{
	GOPGEXT_GUARD({
		TupleDesc	desc = lookup_rowtype_tupdesc_domain(typ, typmod, false);
		struct gopgext_column *cols = palloc0(sizeof(*cols) * Max(desc->natts, 1));

		for (int i = 0; i < desc->natts; i++)
		{
			Form_pg_attribute att = TupleDescAttr(desc, i);

			cols[i].name = pstrdup(NameStr(att->attname));
			cols[i].type = att->atttypid;
			cols[i].typmod = att->atttypmod;
			cols[i].dropped = att->attisdropped;
		}
		*n = desc->natts;
		*out = cols;
		ReleaseTupleDesc(desc);
	});
}

/* The type of a composite value, from the value itself. */
ErrorData *
gopgext_record_type(Datum d, Oid *typ, int32 *typmod)
{
	GOPGEXT_GUARD({
		HeapTupleHeader th = DatumGetHeapTupleHeader(d);

		*typ = HeapTupleHeaderGetTypeId(th);
		*typmod = HeapTupleHeaderGetTypMod(th);
	});
}

/* All the columns of a composite value (dropped ones too, as NULL). */
ErrorData *
gopgext_record_fields(Datum d, int64 *n, Datum **values, bool **nulls, Oid **types)
{
	GOPGEXT_GUARD({
		HeapTupleHeader th = DatumGetHeapTupleHeader(d);
		TupleDesc	desc = lookup_rowtype_tupdesc(HeapTupleHeaderGetTypeId(th),
												  HeapTupleHeaderGetTypMod(th));
		HeapTupleData tuple;
		int			natts = desc->natts;

		*values = palloc(sizeof(Datum) * Max(natts, 1));
		*nulls = palloc(sizeof(bool) * Max(natts, 1));
		*types = palloc(sizeof(Oid) * Max(natts, 1));
		tuple.t_len = HeapTupleHeaderGetDatumLength(th);
		ItemPointerSetInvalid(&tuple.t_self);
		tuple.t_tableOid = InvalidOid;
		tuple.t_data = th;
		heap_deform_tuple(&tuple, desc, *values, *nulls);
		for (int i = 0; i < natts; i++)
		{
			Form_pg_attribute att = TupleDescAttr(desc, i);

			(*types)[i] = att->atttypid;
			if (att->attisdropped)
				(*nulls)[i] = true;
		}
		*n = natts;
		ReleaseTupleDesc(desc);
	});
}

/*
 * A composite value of type typ from its column values; a domain's
 * constraints are checked.
 */
ErrorData *
gopgext_record_new(Oid typ, int32 typmod, int64 n, Datum *values, bool *nulls, Datum *out)
{
	GOPGEXT_GUARD({
		TupleDesc	desc = lookup_rowtype_tupdesc_domain(typ, typmod, false);
		HeapTuple	tup;

		if (desc->natts != n)
			ereport(ERROR,
					(errcode(ERRCODE_DATATYPE_MISMATCH),
					 errmsg("type %s has %d columns, got %d values",
							format_type_be(typ), desc->natts, (int) n)));
		for (int i = 0; i < desc->natts; i++)
			if (TupleDescAttr(desc, i)->attisdropped)
				nulls[i] = true;
		tup = heap_form_tuple(desc, values, nulls);
		*out = HeapTupleGetDatum(tup);
		if (typ != desc->tdtypeid)
			domain_check(*out, false, typ, NULL, NULL);
		ReleaseTupleDesc(desc);
	});
}

/*
 * A value of type typ from text s with typmod (the input function, as for
 * a column); NULL s runs it for NULL (domain checks).
 */
ErrorData *
gopgext_input(Oid typ, int32 typmod, char *s, Datum *out, bool *isnull)
{
	GOPGEXT_GUARD({
		Oid			fn;
		Oid			ioparam;

		getTypeInputInfo(typ, &fn, &ioparam);
		*out = OidInputFunctionCall(fn, s, ioparam, typmod);
		*isnull = (s == NULL);
	});
}

/* Number of dimensions (0: empty array), their lengths and lower bounds. */
ErrorData *
gopgext_array_bounds(Datum d, int64 *ndim, int32 *dims, int32 *lbs)
{
	GOPGEXT_GUARD({
		ArrayType  *a = DatumGetArrayTypeP(d);

		*ndim = ARR_NDIM(a);
		memcpy(dims, ARR_DIMS(a), ARR_NDIM(a) * sizeof(int));
		memcpy(lbs, ARR_LBOUND(a), ARR_NDIM(a) * sizeof(int));
	});
}

/* A new array with the given dimensions and lower bounds (ndim 0: empty). */
ErrorData *
gopgext_array_new_md(Oid elemtype, int64 ndim, int32 *dims, int32 *lbs,
					 Datum *elems, bool *nulls, Datum *out)
{
	GOPGEXT_GUARD({
		int16		typlen;
		bool		byval;
		char		align;

		if (ndim == 0)
			*out = PointerGetDatum(construct_empty_array(elemtype));
		else
		{
			if (ndim < 0 || ndim > MAXDIM)
				ereport(ERROR,
						(errcode(ERRCODE_PROGRAM_LIMIT_EXCEEDED),
						 errmsg("number of array dimensions (%d) exceeds the maximum allowed (%d)",
								(int) ndim, MAXDIM)));
			get_typlenbyvalalign(elemtype, &typlen, &byval, &align);
			*out = PointerGetDatum(construct_md_array(elems, nulls, (int) ndim, dims, lbs,
													  elemtype, typlen, byval, align));
		}
	});
}
