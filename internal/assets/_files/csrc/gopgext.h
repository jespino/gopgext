/*
 * gopgext C interface: the C runtime of a gopgext extension (runtime.c, spi.c)
 * and the generated function wrappers include this.
 *
 * Calling Go from C
 * -----------------
 * Every call into Go goes through gopgext_invoke(), which records the stack
 * range the Go collector scans, catches anything that would unwind through Go
 * frames and, once Go has returned, raises the error Go reported (if any)
 * with ereport().
 *
 * Calling C from Go
 * -----------------
 * A PostgreSQL error must never longjmp through Go frames. Helpers Go calls
 * that may raise one catch it (GOPGEXT_GUARD), store it as the pending error
 * of the current call and return it; Go then unwinds (a panic, recovered by
 * the generated wrapper) and gopgext_invoke re-throws it unchanged. While an
 * error is pending, helpers that run SQL refuse to (the transaction must be
 * aborted first). SPI calls run in a subtransaction instead, so their errors
 * are ordinary Go errors that code can handle and continue.
 */
#ifndef GOPGEXT_H
#define GOPGEXT_H

#include "postgres.h"
#include "fmgr.h"

/*
 * State of one call from PostgreSQL into Go. Mirrored by fmgr.Call in Go:
 * keep the layout in sync (checked with static assertions in runtime.c).
 */
struct gopgext_call {
	FunctionCallInfo fcinfo;	/* NULL for _PG_init */
	Datum result;
	int32 isnull;
	int32 sqlstate;				/* error to raise when non-zero */
	char *message;				/* palloc'd strings of that error */
	char *detail;
	char *hint;
	ErrorData *edata;			/* else: a PostgreSQL error to re-throw */
	void *srf;					/* set-returning function state */
	int32 flags;				/* GOPGEXT_CALL_* */
	int32 pad;
};

/* The error was returned by Go code (not a panic): it can be reported softly. */
#define GOPGEXT_CALL_RETURNED_ERROR 1

/* Saved by tinygo_pgext_enter (runtime_pgext.go's pgextEntry). */
struct gopgext_entry {
	unsigned long top;
	void *defer_frame;
};

typedef void (*gopgext_gofn) (struct gopgext_call *);

Datum gopgext_invoke(FunctionCallInfo fcinfo, gopgext_gofn fn);

/* Generated Go entry point that runs package initialization and main. */
void gopgext_go_init(struct gopgext_call *call);

/* Implemented in Go (overlay/src/runtime/runtime_pgext.go). */
bool tinygo_pgext_init(unsigned long heap_size, unsigned long heap_max);
void tinygo_pgext_enter(unsigned long sp, struct gopgext_entry *e);
void tinygo_pgext_leave(struct gopgext_entry *e);
void tinygo_scanstack(unsigned long sp);

/* runtime.c: services called by Go (the runtime, gopgext/pg, gopgext/fmgr). */
struct gopgext_errinfo;
struct gopgext_spi_result;
struct gopgext_value;
bool		gopgext_loading(void);
const char *gopgext_name(void);
unsigned long gopgext_heap_reserve(unsigned long size);
void		gopgext_print(const char *buf, long len);
void		gopgext_fatal(void);
int64		gopgext_monotonic_ns(void);
int64		gopgext_realtime_ns(void);
void		gopgext_sleep_us(int64 us);
bool		gopgext_strong_random(void *buf, unsigned long len);
ErrorData  *gopgext_check_interrupts(void);
ErrorData  *gopgext_set_config(const char *name, const char *value, bool local);
void		gopgext_error_info(ErrorData *edata, struct gopgext_errinfo *out);
char	   *gopgext_strdup(const char *p, int64 len);
ErrorData  *gopgext_log(int64 level, const char *msg, int64 len);
ErrorData  *gopgext_report(int64 level, const char *text, const char *detail,
						   const char *hint, int64 flags);
ErrorData  *gopgext_varlena_get(Datum d, const char **ptr, int64 *len);
ErrorData  *gopgext_varlena_new(const char *p, int64 len, Datum *out);
ErrorData  *gopgext_array_get(Datum d, Oid elemtype, int64 *n, Datum **elems, bool **nulls);
ErrorData  *gopgext_array_new(Oid elemtype, int64 n, Datum *elems, bool *nulls, Datum *out);
ErrorData  *gopgext_srf_begin(struct gopgext_call *call);
ErrorData  *gopgext_srf_put(struct gopgext_call *call, Datum value, bool isnull, bool free_value);
ErrorData  *gopgext_row_form(struct gopgext_call *call, int64 n, Datum *values, bool *nulls, Datum *out);
ErrorData  *gopgext_result_elemtype(struct gopgext_call *call, int64 column, Oid *out);
ErrorData  *gopgext_row_get(Datum d, int64 n, Datum *values, bool *nulls, Oid *types);
ErrorData  *gopgext_arg_type(struct gopgext_call *call, int64 i, Oid *out);
ErrorData  *gopgext_any_out(Oid typ, Datum d, char **out);
ErrorData  *gopgext_float8_out(double x, char **out);
ErrorData  *gopgext_any_in(Oid typ, char *s, Datum *out);
ErrorData  *gopgext_array_elemtype(Datum d, Oid *out);
ErrorData  *gopgext_array_dims(Datum d, int64 *ndim, int32 *dims);
ErrorData  *gopgext_array_type(Oid elem, Oid *out);
ErrorData  *gopgext_srf_put_row(struct gopgext_call *call, int64 n, Datum *values, bool *nulls, bool *alloc);
ErrorData  *gopgext_guc_define(int64 kind, const char *name, const char *desc,
							   int64 ival, double fval, const char *sval,
							   int64 imin, int64 imax, double fmin, double fmax,
							   int64 context, const char **options, int64 noptions,
							   void **storage);
Oid			gopgext_user_id(void);
Oid			gopgext_database_id(void);
bool		gopgext_loaded(void);
bool		gopgext_preloading(void);
ErrorData  *gopgext_has_privs_of(const char *role, bool *out);
int			gopgext_file_read(const char *path, char **data, int64 *len);
int			gopgext_file_write(const char *path, const char *data, int64 len);
int			gopgext_file_remove(const char *path);
const char *gopgext_strerror(int err);
ErrorData  *gopgext_spi_execute(const char *sql, int64 nargs, struct gopgext_value *args,
								bool read_only, int64 limit, struct gopgext_spi_result *res);

/* runtime.c: the pending error of the innermost call into Go. */
extern ErrorData *gopgext_pending;
ErrorData *gopgext_catch(MemoryContext ctx);

/*
 * Body of a helper returning ErrorData *: runs code, and if it raises an
 * error, catches it, makes it the pending error and returns it (NULL on
 * success).
 */
#define GOPGEXT_GUARD(code)											\
	do {															\
		MemoryContext __gopgext_ctx = CurrentMemoryContext;			\
		ErrorData *volatile __gopgext_err = NULL;					\
		PG_TRY();													\
		{															\
			code;													\
		}															\
		PG_CATCH();													\
		{															\
			__gopgext_err = gopgext_catch(__gopgext_ctx);			\
			if (!gopgext_pending)									\
				gopgext_pending = __gopgext_err;					\
		}															\
		PG_END_TRY();												\
		return __gopgext_err;										\
	} while (0)

/*
 * A value exchanged with Go (SPI parameters and results). Mirrored by
 * pg.cvalue.
 */
struct gopgext_value {
	int64 i;					/* GOPGEXT_BOOL, GOPGEXT_INT */
	double f;					/* GOPGEXT_FLOAT */
	const char *ptr;			/* GOPGEXT_TEXT, GOPGEXT_BYTES, GOPGEXT_OTHER */
	int64 len;
	int32 kind;
	int32 type;					/* PostgreSQL type OID (0: from kind) */
};

enum {
	GOPGEXT_NULL,
	GOPGEXT_BOOL,
	GOPGEXT_INT,
	GOPGEXT_FLOAT,
	GOPGEXT_TEXT,
	GOPGEXT_BYTES,
	GOPGEXT_OTHER,				/* any other type, in its text form */
	GOPGEXT_DATUM,				/* parameters: a Datum (i) of type type */
};

#endif
