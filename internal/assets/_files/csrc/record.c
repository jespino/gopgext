/*
 * Results of type record (gopgext/fmgr's ReturnRecord*, pg.ResultColumns):
 * rows whose columns the caller defines, with a column definition list
 * (SELECT * FROM f() AS t(a int, b text)) or the composite type the
 * function is declared to return. Go converts each value either to a Datum
 * of the column's type or to text, which the column type's input function
 * reads here (like BuildTupleFromCStrings).
 */
#include "postgres.h"

#include "access/htup_details.h"
#include "funcapi.h"
#include "utils/builtins.h"
#include "utils/lsyscache.h"
#include "utils/tuplestore.h"

#include "gopgext.h"

/* A column of a row result. Mirrored by core.ccolumn. */
struct gopgext_column
{
	const char *name;
	const char *typname;
	int64		type;
	int64		typmod;
};

/* A value of a row built by Go. Mirrored by fmgr.cell. */
struct gopgext_cell
{
	Datum		d;
	const char *s;				/* text form (not NUL-terminated) */
	int64		len;
	int32		kind;			/* 0 NULL, 1 Datum, 2 text */
	int32		alloc;			/* d was palloc'd: freed once stored */
};

StaticAssertDecl(sizeof(struct gopgext_column) == 32, "gopgext_column layout");
StaticAssertDecl(sizeof(struct gopgext_cell) == 32, "gopgext_cell layout");

struct gopgext_record
{
	TupleDesc	desc;
	AttInMetadata *meta;		/* input functions, built when text is seen */
	Tuplestorestate *store;		/* set-returning functions */
	Datum	   *values;
	bool	   *nulls;
	bool	   *free;
};

ErrorData  *gopgext_result_columns(struct gopgext_call *call, int64 *n, struct gopgext_column **cols);
ErrorData  *gopgext_format_type(Oid type, int32 typmod, char **out);
ErrorData  *gopgext_record_begin(struct gopgext_call *call, struct gopgext_record **state,
								 int64 *n, struct gopgext_column **cols);
ErrorData  *gopgext_record_form(struct gopgext_record *r, int64 n, struct gopgext_cell *cells, Datum *out);
ErrorData  *gopgext_record_put(struct gopgext_record *r, int64 n, struct gopgext_cell *cells);

/* The row type the caller of the function expects (raises an error if none). */
static TupleDesc
result_desc(struct gopgext_call *call)
{
	TupleDesc	desc;

	if (call == NULL || call->fcinfo == NULL || call->fcinfo->flinfo == NULL)
		elog(ERROR, "not in a call of a SQL function");
	switch (get_call_result_type(call->fcinfo, NULL, &desc))
	{
		case TYPEFUNC_COMPOSITE:
		case TYPEFUNC_COMPOSITE_DOMAIN:
			return desc;
		case TYPEFUNC_RECORD:
			ereport(ERROR,
					(errcode(ERRCODE_FEATURE_NOT_SUPPORTED),
					 errmsg("function returning record called in context "
							"that cannot accept type record")));
			break;
		default:
			ereport(ERROR,
					(errcode(ERRCODE_DATATYPE_MISMATCH),
					 errmsg("return type must be a row type")));
	}
	return NULL;
}

static struct gopgext_column *
describe(TupleDesc desc)
{
	struct gopgext_column *cols = palloc0(sizeof(*cols) * (desc->natts + 1));

	for (int i = 0; i < desc->natts; i++)
	{
		Form_pg_attribute att = TupleDescAttr(desc, i);

		cols[i].name = pstrdup(NameStr(att->attname));
		cols[i].type = att->atttypid;
		cols[i].typmod = att->atttypmod;
		cols[i].typname = att->attisdropped ? "-" :
			format_type_with_typemod(att->atttypid, att->atttypmod);
	}
	return cols;
}

/* The columns of the row result of a call. */
ErrorData *
gopgext_result_columns(struct gopgext_call *call, int64 *n, struct gopgext_column **cols)
{
	GOPGEXT_GUARD({
		TupleDesc	desc = result_desc(call);

		*n = desc->natts;
		*cols = describe(desc);
	});
}

ErrorData *
gopgext_format_type(Oid type, int32 typmod, char **out)
{
	GOPGEXT_GUARD(*out = format_type_with_typemod(type, typmod));
}

/*
 * Starts building record results for a call: one row (the result), or the
 * rows of a set-returning function (after gopgext_srf_begin).
 */
ErrorData *
gopgext_record_begin(struct gopgext_call *call, struct gopgext_record **state,
					 int64 *n, struct gopgext_column **cols)
{
	GOPGEXT_GUARD({
		struct gopgext_record *r = palloc0(sizeof(*r));

		if (call->srf)
		{
			ReturnSetInfo *rsinfo = (ReturnSetInfo *) call->fcinfo->resultinfo;

			r->desc = rsinfo->setDesc;
			r->store = rsinfo->setResult;
		}
		else
			r->desc = BlessTupleDesc(result_desc(call));
		r->values = palloc0(sizeof(Datum) * (r->desc->natts + 1));
		r->nulls = palloc0(sizeof(bool) * (r->desc->natts + 1));
		r->free = palloc0(sizeof(bool) * (r->desc->natts + 1));
		*n = r->desc->natts;
		*cols = describe(r->desc);
		*state = r;
	});
}

/* Converts the cells of a row (n is checked by Go). */
static void
record_values(struct gopgext_record *r, int64 n, struct gopgext_cell *cells)
{
	for (int i = 0; i < n; i++)
	{
		Form_pg_attribute att = TupleDescAttr(r->desc, i);
		struct gopgext_cell *c = &cells[i];

		r->free[i] = false;
		r->nulls[i] = c->kind == 0 || att->attisdropped;
		r->values[i] = (Datum) 0;
		if (r->nulls[i])
			continue;
		if (c->kind == 1)
		{
			r->values[i] = c->d;
			r->free[i] = c->alloc != 0;
			continue;
		}
		{
			char	   *s = pnstrdup(c->s, c->len);

			if (r->meta == NULL)
				r->meta = TupleDescGetAttInMetadata(r->desc);
			r->values[i] = InputFunctionCall(&r->meta->attinfuncs[i], s,
											 r->meta->attioparams[i],
											 r->meta->atttypmods[i]);
			r->free[i] = !att->attbyval && att->attlen == -1;
			pfree(s);
		}
	}
}

static void
record_free(struct gopgext_record *r, int64 n)
{
	for (int i = 0; i < n; i++)
		if (r->free[i] && !r->nulls[i])
			pfree(DatumGetPointer(r->values[i]));
}

/* One row: the composite result Datum. */
ErrorData *
gopgext_record_form(struct gopgext_record *r, int64 n, struct gopgext_cell *cells, Datum *out)
{
	GOPGEXT_GUARD({
		record_values(r, n, cells);
		*out = HeapTupleGetDatum(heap_form_tuple(r->desc, r->values, r->nulls));
		record_free(r, n);
	});
}

/* One row of a set-returning function. */
ErrorData *
gopgext_record_put(struct gopgext_record *r, int64 n, struct gopgext_cell *cells)
{
	GOPGEXT_GUARD({
		record_values(r, n, cells);
		tuplestore_putvalues(r->store, r->desc, r->values, r->nulls);
		record_free(r, n);
	});
}
