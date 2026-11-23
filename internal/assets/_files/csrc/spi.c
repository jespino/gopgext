/*
 * SPI for Go (pg.Exec, pg.Query). Each statement runs in its own
 * subtransaction, like PL/Python's plpy.execute: if it fails, the
 * subtransaction is rolled back and the error is returned to Go as a value,
 * so Go code can handle it and go on using the database.
 *
 * Results are copied out of SPI before SPI_finish(), into the memory context
 * of the calling function: Go reads them from there and copies them into Go
 * values before the call returns.
 */
#include "postgres.h"

#include "access/xact.h"
#include "catalog/pg_type.h"
#include "executor/spi.h"
#include "parser/parse_coerce.h"
#include "utils/builtins.h"
#include "utils/lsyscache.h"
#include "utils/memutils.h"
#include "utils/resowner.h"

#include "gopgext.h"

/* Mirrored by pg.cresult. */
struct gopgext_spi_result
{
	int64		processed;		/* rows returned or affected */
	int64		ncols;
	int64		nrows;
	const char **colnames;
	int64	   *coltypes;
	struct gopgext_value *values;	/* nrows * ncols, row-major */
	int64	   *coltypmods;
};

static Datum
gopgext_param(struct gopgext_value *v, Oid *type, char *null)
{
	*null = ' ';
	switch (v->kind)
	{
		case GOPGEXT_BOOL:
			*type = BOOLOID;
			return BoolGetDatum(v->i != 0);
		case GOPGEXT_INT:
			switch (v->type)
			{
				case INT2OID:
					*type = INT2OID;
					return Int16GetDatum((int16) v->i);
				case INT4OID:
					*type = INT4OID;
					return Int32GetDatum((int32) v->i);
				default:
					*type = INT8OID;
					return Int64GetDatum(v->i);
			}
		case GOPGEXT_FLOAT:
			if (v->type == FLOAT4OID)
			{
				*type = FLOAT4OID;
				return Float4GetDatum((float4) v->f);
			}
			*type = FLOAT8OID;
			return Float8GetDatum(v->f);
		case GOPGEXT_TEXT:
		case GOPGEXT_BYTES:
			{
				struct varlena *t = (struct varlena *) palloc(v->len + VARHDRSZ);

				SET_VARSIZE(t, v->len + VARHDRSZ);
				if (v->len)
					memcpy(VARDATA(t), v->ptr, v->len);
				*type = v->kind == GOPGEXT_TEXT ? TEXTOID : BYTEAOID;
				return PointerGetDatum(t);
			}
		case GOPGEXT_DATUM:
			*type = (Oid) v->type;
			return (Datum) v->i;
		default:
			*type = v->type ? (Oid) v->type : UNKNOWNOID;
			*null = 'n';
			return (Datum) 0;
	}
}

static void
gopgext_result_value(struct gopgext_value *out, Datum d, bool isnull, Oid type)
{
	memset(out, 0, sizeof(*out));
	out->type = type;
	if (isnull)
	{
		out->kind = GOPGEXT_NULL;
		return;
	}
	switch (type)
	{
		case BOOLOID:
			out->kind = GOPGEXT_BOOL;
			out->i = DatumGetBool(d);
			break;
		case INT2OID:
			out->kind = GOPGEXT_INT;
			out->i = DatumGetInt16(d);
			break;
		case INT4OID:
			out->kind = GOPGEXT_INT;
			out->i = DatumGetInt32(d);
			break;
		case INT8OID:
			out->kind = GOPGEXT_INT;
			out->i = DatumGetInt64(d);
			break;
		case FLOAT4OID:
			out->kind = GOPGEXT_FLOAT;
			out->f = DatumGetFloat4(d);
			break;
		case FLOAT8OID:
			out->kind = GOPGEXT_FLOAT;
			out->f = DatumGetFloat8(d);
			break;
		case TEXTOID:
		case VARCHAROID:
		case BPCHAROID:
		case BYTEAOID:
			{
				/* A copy: the tuple's memory goes away at SPI_finish(). */
				struct varlena *v = pg_detoast_datum_copy((struct varlena *) DatumGetPointer(d));

				out->kind = type == BYTEAOID ? GOPGEXT_BYTES : GOPGEXT_TEXT;
				out->ptr = VARDATA_ANY(v);
				out->len = VARSIZE_ANY_EXHDR(v);
				break;
			}
		default:
			{
				Oid			outfn;
				bool		varlena;
				char	   *s;

				getTypeOutputInfo(type, &outfn, &varlena);
				s = OidOutputFunctionCall(outfn, d);
				out->kind = GOPGEXT_OTHER;
				out->ptr = s;
				out->len = strlen(s);
				break;
			}
	}
}

/*
 * The kinds of statement spi_run runs: SQL text, a prepared plan, or a
 * statement to prepare.
 */
enum spi_op
{
	SPI_RUN_SQL,
	SPI_RUN_PLAN,
	SPI_RUN_PREPARE,
};

/*
 * Runs a statement in a subtransaction. On success fills *res (allocated
 * in the caller's memory context) and returns NULL; on error returns it
 * (the subtransaction has been rolled back). SPI_RUN_PREPARE prepares sql
 * with the parameter types in args (their type field) and stores the
 * saved plan in *plan.
 */
static ErrorData *
spi_run(enum spi_op op, const char *sql, SPIPlanPtr *plan, int64 nargs,
		struct gopgext_value *args, bool read_only, int64 limit,
		struct gopgext_spi_result *res)
{
	MemoryContext callctx = CurrentMemoryContext;
	ResourceOwner owner = CurrentResourceOwner;
	ErrorData  *volatile err = NULL;

	if (gopgext_pending)
	{
		/* An uncaught error is on its way out: the transaction is unusable. */
		return gopgext_pending;
	}

	if (res)
		memset(res, 0, sizeof(*res));
	if (!IsTransactionState())
	{
		/* E.g. a background worker outside a transaction. */
		PG_TRY();
		{
			ereport(ERROR,
					(errcode(ERRCODE_INVALID_TRANSACTION_STATE),
					 errmsg("cannot run SQL outside a transaction")));
		}
		PG_CATCH();
		{
			err = gopgext_catch(callctx);
		}
		PG_END_TRY();
		return err;
	}
	BeginInternalSubTransaction(NULL);
	MemoryContextSwitchTo(callctx);
	PG_TRY();
	{
		Oid		   *types = nargs ? palloc(sizeof(Oid) * nargs) : NULL;
		Datum	   *values = nargs ? palloc(sizeof(Datum) * nargs) : NULL;
		char	   *nulls = nargs ? palloc(nargs + 1) : NULL;
		int			rc;
		int64		i,
					j;

		if (op == SPI_RUN_PREPARE)
		{
			SPIPlanPtr	p;

			for (i = 0; i < nargs; i++)
				types[i] = (Oid) args[i].type;
			if (SPI_connect() != SPI_OK_CONNECT)
				elog(ERROR, "SPI_connect failed");
			p = SPI_prepare(sql, (int) nargs, types);
			if (p == NULL)
				elog(ERROR, "SPI_prepare failed: %s", SPI_result_code_string(SPI_result));
			if (SPI_keepplan(p) != 0)
				elog(ERROR, "SPI_keepplan failed");
			SPI_finish();
			*plan = p;
		}
		else
		{
			for (i = 0; i < nargs; i++)
			{
				values[i] = gopgext_param(&args[i], &types[i], &nulls[i]);
				if (op == SPI_RUN_PLAN && nulls[i] != 'n')
				{
					/* The plan's types: text converts with their input function. */
					Oid			want = SPI_getargtypeid(*plan, (int) i);

					if (types[i] == TEXTOID && want != TEXTOID)
					{
						Oid			fn;
						Oid			ioparam;

						getTypeInputInfo(want, &fn, &ioparam);
						values[i] = OidInputFunctionCall(fn, TextDatumGetCString(values[i]), ioparam, -1);
					}
					else if (types[i] != want &&
							 !(types[i] == UNKNOWNOID || IsBinaryCoercible(types[i], want)))
						ereport(ERROR,
								(errcode(ERRCODE_DATATYPE_MISMATCH),
								 errmsg("parameter $%d of the prepared statement is %s, not %s",
										(int) i + 1, format_type_be(want), format_type_be(types[i]))));
				}
			}

			if (SPI_connect() != SPI_OK_CONNECT)
				elog(ERROR, "SPI_connect failed");
			if (op == SPI_RUN_PLAN)
			{
				if (SPI_getargcount(*plan) != nargs)
					ereport(ERROR,
							(errcode(ERRCODE_PROTOCOL_VIOLATION),
							 errmsg("the prepared statement has %d parameters, not %d",
									SPI_getargcount(*plan), (int) nargs)));
				rc = SPI_execute_plan(*plan, values, nulls, read_only, limit);
			}
			else
				rc = SPI_execute_with_args(sql, (int) nargs, types, values, nulls, read_only, limit);
			if (rc < 0)
				elog(ERROR, "SPI execution failed: %s", SPI_result_code_string(rc));

			res->processed = (int64) SPI_processed;
			if (SPI_tuptable != NULL)
			{
				TupleDesc	desc = SPI_tuptable->tupdesc;
				MemoryContext spictx = MemoryContextSwitchTo(callctx);

				res->ncols = desc->natts;
				res->nrows = (int64) SPI_processed;
				res->colnames = palloc0(sizeof(char *) * (res->ncols + 1));
				res->coltypes = palloc0(sizeof(int64) * (res->ncols + 1));
				res->coltypmods = palloc0(sizeof(int64) * (res->ncols + 1));
				res->values = palloc0(sizeof(struct gopgext_value) * (res->ncols * res->nrows + 1));
				for (j = 0; j < res->ncols; j++)
				{
					res->colnames[j] = pstrdup(NameStr(TupleDescAttr(desc, j)->attname));
					res->coltypes[j] = SPI_gettypeid(desc, j + 1);
					res->coltypmods[j] = TupleDescAttr(desc, j)->atttypmod;
				}
				for (i = 0; i < res->nrows; i++)
				{
					HeapTuple	tuple = SPI_tuptable->vals[i];

					for (j = 0; j < res->ncols; j++)
					{
						bool		isnull;
						Datum		d = SPI_getbinval(tuple, desc, j + 1, &isnull);

						gopgext_result_value(&res->values[i * res->ncols + j], d, isnull,
											 (Oid) res->coltypes[j]);
					}
				}
				MemoryContextSwitchTo(spictx);
			}
			SPI_finish();
		}

		ReleaseCurrentSubTransaction();
		MemoryContextSwitchTo(callctx);
		CurrentResourceOwner = owner;
	}
	PG_CATCH();
	{
		err = gopgext_catch(callctx);
		RollbackAndReleaseCurrentSubTransaction();
		MemoryContextSwitchTo(callctx);
		CurrentResourceOwner = owner;
	}
	PG_END_TRY();
	return err;
}

/* Runs sql with nargs parameters (see spi_run). */
ErrorData *
gopgext_spi_execute(const char *sql, int64 nargs, struct gopgext_value *args,
					bool read_only, int64 limit, struct gopgext_spi_result *res)
{
	return spi_run(SPI_RUN_SQL, sql, NULL, nargs, args, read_only, limit, res);
}

/* Prepares sql with nargs parameters of types args[i].type (see spi_run). */
ErrorData *
gopgext_spi_prepare(const char *sql, int64 nargs, struct gopgext_value *args, void **plan)
{
	*plan = NULL;
	return spi_run(SPI_RUN_PREPARE, sql, (SPIPlanPtr *) plan, nargs, args, false, 0, NULL);
}

/* Runs a prepared plan (see spi_run). */
ErrorData *
gopgext_spi_execute_plan(void *plan, int64 nargs, struct gopgext_value *args,
						 bool read_only, int64 limit, struct gopgext_spi_result *res)
{
	SPIPlanPtr	p = (SPIPlanPtr) plan;

	return spi_run(SPI_RUN_PLAN, NULL, &p, nargs, args, read_only, limit, res);
}

/* Frees a prepared plan. */
void
gopgext_spi_free_plan(void *plan)
{
	if (plan)
		SPI_freeplan((SPIPlanPtr) plan);
}
