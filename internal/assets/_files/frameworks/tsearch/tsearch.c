/*
 * C side of gopgext/tsearch: text search dictionary templates and parsers
 * implemented in Go, and text search services for Go code (data files,
 * calling dictionaries).
 *
 * PostgreSQL's structures stay here: dictionary options (a List of DefElem)
 * reach Go as name/value strings, lexemes come back as plain values that
 * this code turns into a palloc'd TSLexeme array, and tokens and token
 * types are copied into palloc'd memory. A dictionary's (or a parse's) Go
 * state is identified by a small handle allocated in the memory context
 * PostgreSQL keeps it in (a dictionary's cache entry); when that context is
 * reset or deleted, a callback records the id as freed and Go drops the
 * state on its next call, so no Go code runs from the callback.
 */
#include "postgres.h"

/*
 * ts_locale.h includes pg_locale.h, which needs ICU's headers when the
 * server was built with ICU; nothing here uses ICU (or pg_locale_t), so
 * don't require them.
 */
#undef USE_ICU

#include "catalog/pg_collation.h"
#include "catalog/pg_ts_dict.h"
#include "commands/defrem.h"
#include "mb/pg_wchar.h"
#include "nodes/parsenodes.h"
#include "tsearch/ts_cache.h"
#include "tsearch/ts_locale.h"
#include "tsearch/ts_public.h"
#include "tsearch/ts_utils.h"
#include "utils/builtins.h"
#include "utils/formatting.h"
#include "utils/lsyscache.h"
#include "utils/memutils.h"
#include "utils/syscache.h"

#include "gopgext.h"

void		gopgext_go_tsearch_init(struct gopgext_call *call);
void		gopgext_go_tsearch_lexize(struct gopgext_call *call);
void		gopgext_go_tsearch_start(struct gopgext_call *call);
void		gopgext_go_tsearch_token(struct gopgext_call *call);
void		gopgext_go_tsearch_end(struct gopgext_call *call);
void		gopgext_go_tsearch_lextype(struct gopgext_call *call);

#define TS_MAGIC 0x67747368		/* "gtsh" */

/* The Datum of a dictionary's data or a parse's state. */
struct ts_handle
{
	uint32		magic;
	uint64		id;
	MemoryContextCallback cb;
};

/* A dictionary option. Mirrored by tsearch.coption. */
struct ts_option
{
	const char *name;
	const char *value;			/* defGetString(), NULL without a value */
	int64		is_int;			/* the value is an Integer node */
	int64		ival;
};

/* What the Go entry points get. Mirrored by tsearch.cinput. */
struct ts_input
{
	const char *name;			/* SQL function: init, start or lextype */
	uint64		id;				/* handle: lexize, token, end */
	const char *text;			/* lexize: the word; start: the text */
	int64		len;
	int64		noptions;		/* init */
	struct ts_option *options;
	char	   *token;			/* token: the token found (palloc'd) */
	int64		token_len;
};

/* A lexeme from Go. Mirrored by tsearch.clexeme. */
struct ts_lexeme
{
	const char *text;
	int64		len;
	int64		nvariant;
	int64		flags;
};

/* A token type from Go. Mirrored by tsearch.ctokentype. */
struct ts_tokentype
{
	int64		id;
	const char *alias;
	int64		alias_len;
	const char *descr;
	int64		descr_len;
};

ErrorData  *gopgext_tsearch_handle(uint64 id, Datum *out);
int64		gopgext_tsearch_freed(uint64 *buf, int64 max);
ErrorData  *gopgext_tsearch_lexemes(int64 n, struct ts_lexeme *lexemes, Datum *out);
ErrorData  *gopgext_tsearch_tokentypes(int64 n, struct ts_tokentype *types, Datum *out);
ErrorData  *gopgext_tsearch_call_lexize(Oid dict, const char *word, int64 len, TSLexeme **out, int64 *n);
ErrorData  *gopgext_tsearch_dictionary(FunctionCallInfo fcinfo, const char *name, Oid *out, char **schema);
ErrorData  *gopgext_tsearch_config_path(const char *name, const char *ext, char **out);
ErrorData  *gopgext_tsearch_open(const char *path, tsearch_readline_state **out, int *err);
ErrorData  *gopgext_tsearch_readline(tsearch_readline_state *st, char **line, bool *eof);
void		gopgext_tsearch_close(tsearch_readline_state *st);
ErrorData  *gopgext_tsearch_lower(const char *s, int64 len, char **out);
int64		gopgext_tsearch_mblen(const char *s, int64 len);
int64		gopgext_tsearch_encoding(void);

/* A palloc'd copy of n bytes at s (s may be NULL if n is 0). */
static char *
copy_text(const char *s, int64 n)
{
	char	   *p = palloc(n + 1);

	if (n > 0)
		memcpy(p, s, n);
	p[n] = '\0';
	return p;
}

/* ---- Handles of Go states ------------------------------------------ */

/* Ids of freed states, not yet dropped by Go. */
static uint64 *freed;
static int	nfreed;
static int	maxfreed;

static void
handle_reset(void *arg)
{
	struct ts_handle *h = arg;

	h->magic = 0;
	if (h->id == 0)
		return;					/* already dropped (end of a parse) */
	if (nfreed == maxfreed)
	{
		int			n = maxfreed ? maxfreed * 2 : 256;
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

/* A new handle for Go state id, in the current memory context. */
ErrorData *
gopgext_tsearch_handle(uint64 id, Datum *out)
{
	GOPGEXT_GUARD({
		struct ts_handle *h = palloc(sizeof(*h));

		h->magic = TS_MAGIC;
		h->id = id;
		h->cb.func = handle_reset;
		h->cb.arg = h;
		MemoryContextRegisterResetCallback(CurrentMemoryContext, &h->cb);
		*out = PointerGetDatum(h);
	});
}

/* Moves up to max freed ids to buf; returns how many. */
int64
gopgext_tsearch_freed(uint64 *buf, int64 max)
{
	int64		n = Min((int64) nfreed, max);

	nfreed -= n;
	memcpy(buf, freed + nfreed, sizeof(uint64) * n);
	return n;
}

static struct ts_handle *
get_handle(Datum d, const char *what)
{
	struct ts_handle *h = (struct ts_handle *) DatumGetPointer(d);

	if (h == NULL || h->magic != TS_MAGIC)
		ereport(ERROR,
				(errcode(ERRCODE_DATATYPE_MISMATCH),
				 errmsg("%s was not created by gopgext/tsearch", what)));
	return h;
}

/* ---- Calling Go ---------------------------------------------------- */

/* The name of the SQL function being called (cached in fn_extra). */
static const char *
function_name(FunctionCallInfo fcinfo)
{
	if (fcinfo->flinfo->fn_extra == NULL)
	{
		char	   *name = get_func_name(fcinfo->flinfo->fn_oid);

		if (name == NULL)
			elog(ERROR, "cache lookup failed for function %u", fcinfo->flinfo->fn_oid);
		fcinfo->flinfo->fn_extra = MemoryContextStrdup(fcinfo->flinfo->fn_mcxt, name);
	}
	return fcinfo->flinfo->fn_extra;
}

static Datum
call_go(gopgext_gofn fn, struct ts_input *in)
{
	LOCAL_FCINFO(fc, 1);
	Datum		result;

	InitFunctionCallInfoData(*fc, NULL, 1, InvalidOid, NULL, NULL);
	fc->args[0].value = PointerGetDatum(in);
	fc->args[0].isnull = false;
	result = gopgext_invoke(fc, fn);
	return fc->isnull ? (Datum) 0 : result;
}

/* ---- Dictionary templates ------------------------------------------ */

PG_FUNCTION_INFO_V1(gopgext_tsearch_init);
PG_FUNCTION_INFO_V1(gopgext_tsearch_lexize);

/*
 * init(internal) RETURNS internal: the dictionary's data, from its options
 * (a List of DefElem). Called in the dictionary's memory context.
 */
Datum
gopgext_tsearch_init(PG_FUNCTION_ARGS)
{
	List	   *options = (List *) PG_GETARG_POINTER(0);
	struct ts_input in;
	ListCell   *l;
	int			i = 0;

	memset(&in, 0, sizeof(in));
	in.name = function_name(fcinfo);
	in.noptions = list_length(options);
	in.options = palloc0(sizeof(struct ts_option) * Max(in.noptions, 1));
	foreach(l, options)
	{
		DefElem    *def = (DefElem *) lfirst(l);
		struct ts_option *o = &in.options[i++];

		o->name = def->defname;
		if (def->arg != NULL)
		{
			o->value = defGetString(def);
			if (IsA(def->arg, Integer))
			{
				o->is_int = 1;
				o->ival = intVal(def->arg);
			}
		}
	}
	PG_RETURN_DATUM(call_go(gopgext_go_tsearch_init, &in));
}

/*
 * lexize(internal dictdata, internal word, internal len, internal substate)
 * RETURNS internal: a TSLexeme array, or NULL (unknown word).
 */
Datum
gopgext_tsearch_lexize(PG_FUNCTION_ARGS)
{
	struct ts_input in;

	memset(&in, 0, sizeof(in));
	in.id = get_handle(PG_GETARG_DATUM(0), "text search dictionary data")->id;
	in.text = (const char *) PG_GETARG_POINTER(1);
	in.len = PG_GETARG_INT32(2);
	PG_RETURN_DATUM(call_go(gopgext_go_tsearch_lexize, &in));
}

/* A palloc'd TSLexeme array (terminated by a NULL lexeme). */
ErrorData *
gopgext_tsearch_lexemes(int64 n, struct ts_lexeme *lexemes, Datum *out)
{
	GOPGEXT_GUARD({
		TSLexeme   *res = palloc0(sizeof(TSLexeme) * (n + 1));

		for (int64 i = 0; i < n; i++)
		{
			res[i].lexeme = copy_text(lexemes[i].text, lexemes[i].len);
			res[i].nvariant = (uint16) lexemes[i].nvariant;
			res[i].flags = (uint16) lexemes[i].flags;
		}
		*out = PointerGetDatum(res);
	});
}

/* ---- Parsers ------------------------------------------------------- */

PG_FUNCTION_INFO_V1(gopgext_tsearch_start);
PG_FUNCTION_INFO_V1(gopgext_tsearch_gettoken);
PG_FUNCTION_INFO_V1(gopgext_tsearch_end);
PG_FUNCTION_INFO_V1(gopgext_tsearch_lextype);

/* start(internal text, int4 len) RETURNS internal: a parse of text. */
Datum
gopgext_tsearch_start(PG_FUNCTION_ARGS)
{
	struct ts_input in;

	memset(&in, 0, sizeof(in));
	in.name = function_name(fcinfo);
	in.text = (const char *) PG_GETARG_POINTER(0);
	in.len = PG_GETARG_INT32(1);
	PG_RETURN_DATUM(call_go(gopgext_go_tsearch_start, &in));
}

/*
 * gettoken(internal state, internal char **token, internal int *len)
 * RETURNS int4: the next token's type, 0 at the end.
 */
Datum
gopgext_tsearch_gettoken(PG_FUNCTION_ARGS)
{
	char	  **token = (char **) PG_GETARG_POINTER(1);
	int		   *len = (int *) PG_GETARG_POINTER(2);
	struct ts_input in;
	Datum		type;

	memset(&in, 0, sizeof(in));
	in.id = get_handle(PG_GETARG_DATUM(0), "text search parser state")->id;
	type = call_go(gopgext_go_tsearch_token, &in);
	*token = in.token;
	*len = (int) in.token_len;
	PG_RETURN_DATUM(type);
}

/* end(internal state) RETURNS void */
Datum
gopgext_tsearch_end(PG_FUNCTION_ARGS)
{
	struct ts_handle *h = get_handle(PG_GETARG_DATUM(0), "text search parser state");
	struct ts_input in;

	memset(&in, 0, sizeof(in));
	in.id = h->id;
	h->id = 0;
	call_go(gopgext_go_tsearch_end, &in);
	PG_RETURN_VOID();
}

/* lextype(internal) RETURNS internal: a LexDescr array (lexid 0 ends it). */
Datum
gopgext_tsearch_lextype(PG_FUNCTION_ARGS)
{
	struct ts_input in;

	memset(&in, 0, sizeof(in));
	in.name = function_name(fcinfo);
	PG_RETURN_DATUM(call_go(gopgext_go_tsearch_lextype, &in));
}

/* A palloc'd LexDescr array. */
ErrorData *
gopgext_tsearch_tokentypes(int64 n, struct ts_tokentype *types, Datum *out)
{
	GOPGEXT_GUARD({
		LexDescr   *res = palloc0(sizeof(LexDescr) * (n + 1));

		for (int64 i = 0; i < n; i++)
		{
			res[i].lexid = (int) types[i].id;
			res[i].alias = copy_text(types[i].alias, types[i].alias_len);
			res[i].descr = copy_text(types[i].descr, types[i].descr_len);
		}
		*out = PointerGetDatum(res);
	});
}

/* ---- Services for Go ----------------------------------------------- */

/* Runs dictionary dict's lexize on word; *n is the number of lexemes. */
ErrorData *
gopgext_tsearch_call_lexize(Oid dict, const char *word, int64 len, TSLexeme **out, int64 *n)
{
	GOPGEXT_GUARD({
		TSDictionaryCacheEntry *d = lookup_ts_dictionary_cache(dict);
		char	   *w = copy_text(word, len);
		TSLexeme   *res;

		res = (TSLexeme *) DatumGetPointer(FunctionCall4(&d->lexize,
														 PointerGetDatum(d->dictData),
														 PointerGetDatum(w),
														 Int32GetDatum((int32) len),
														 PointerGetDatum(NULL)));
		*out = res;
		*n = 0;
		if (res)
			while (res[*n].lexeme)
				(*n)++;
	});
}

/*
 * The text search dictionary name in the schema of the SQL function called
 * with fcinfo (InvalidOid if there is none: *schema is that schema's name).
 */
ErrorData *
gopgext_tsearch_dictionary(FunctionCallInfo fcinfo, const char *name, Oid *out, char **schema)
{
	GOPGEXT_GUARD({
		Oid			nsp;

		if (fcinfo == NULL || fcinfo->flinfo == NULL)
			ereport(ERROR,
					(errcode(ERRCODE_FEATURE_NOT_SUPPORTED),
					 errmsg("tsearch.FindDictionary must be called by a SQL function")));
		nsp = get_func_namespace(fcinfo->flinfo->fn_oid);
		*out = GetSysCacheOid2(TSDICTNAMENSP, Anum_pg_ts_dict_oid,
							   CStringGetDatum(name), ObjectIdGetDatum(nsp));
		*schema = get_namespace_name(nsp);
	});
}

/* get_tsearch_config_filename: $SHAREDIR/tsearch_data/name.ext */
ErrorData *
gopgext_tsearch_config_path(const char *name, const char *ext, char **out)
{
	GOPGEXT_GUARD(*out = get_tsearch_config_filename(name, ext));
}

/*
 * Opens a text search data file (tsearch_readline_begin: this pushes the
 * error context "line N of configuration file" until it's closed). Returns
 * 0, or errno.
 */
static int
tsearch_open(const char *path, tsearch_readline_state **out)
{
	tsearch_readline_state *st = palloc0(sizeof(*st));

	if (!tsearch_readline_begin(st, pstrdup(path)))
	{
		int			err = errno;

		pfree(st);
		return err;
	}
	*out = st;
	return 0;
}

/*
 * tsearch_open for Go: the allocations, and AllocateFile ("exceeded
 * maxAllocatedDescs"), may raise errors. The errno is stored in *err.
 */
ErrorData *
gopgext_tsearch_open(const char *path, tsearch_readline_state **out, int *err)
{
	*out = NULL;
	*err = 0;
	GOPGEXT_GUARD(*err = tsearch_open(path, out));
}

/*
 * Reads the next line (tsearch_readline: converted from UTF-8 to the server
 * encoding). An error (e.g. a character with no equivalent in the server
 * encoding) is returned without making it pending: the next call reads the
 * next line.
 */
ErrorData *
gopgext_tsearch_readline(tsearch_readline_state *st, char **line, bool *eof)
{
	MemoryContext ctx = CurrentMemoryContext;
	ErrorData  *volatile err = NULL;

	PG_TRY();
	{
		*line = tsearch_readline(st);
		*eof = *line == NULL;
	}
	PG_CATCH();
	{
		err = gopgext_catch(ctx);
	}
	PG_END_TRY();
	return err;
}

void
gopgext_tsearch_close(tsearch_readline_state *st)
{
	tsearch_readline_end(st);
}

/* Lower-cases s with the database's default collation (str_tolower). */
ErrorData *
gopgext_tsearch_lower(const char *s, int64 len, char **out)
{
	GOPGEXT_GUARD(*out = str_tolower(s, (size_t) len, DEFAULT_COLLATION_OID));
}

/* Length of the server-encoded character at s, at most len (pg_mblen). */
int64
gopgext_tsearch_mblen(const char *s, int64 len)
{
	int			n = pg_encoding_mblen(GetDatabaseEncoding(), s);

	return Min((int64) n, len);
}

/* The server encoding (PG_UTF8, ...) */
int64
gopgext_tsearch_encoding(void)
{
	return GetDatabaseEncoding();
}
