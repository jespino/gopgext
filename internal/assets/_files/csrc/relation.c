/*
 * Services for gopgext/pg about tables and roles: relation metadata from
 * the catalog caches (columns, primary key, triggers; usable anywhere in
 * a transaction, including the planner, without SPI), relcache
 * invalidation, running code as another role (pg.RunAs), and the binary
 * images of Datums (pg.FromImage, AnyElement.Image). Like runtime.c,
 * helpers that may raise an error catch it (GOPGEXT_GUARD) and return it.
 */
#include "postgres.h"

#include "access/htup_details.h"
#include "access/relation.h"
#include "access/table.h"
#include "catalog/pg_authid.h"
#include "catalog/pg_class.h"
#include "catalog/pg_index.h"
#include "catalog/pg_trigger.h"
#include "commands/trigger.h"
#include "miscadmin.h"
#include "utils/builtins.h"
#include "utils/guc.h"
#include "utils/inval.h"
#include "utils/lsyscache.h"
#include "utils/rel.h"
#include "utils/relcache.h"
#include "utils/syscache.h"

#include "gopgext.h"

/* A column of a relation. Mirrored by pg.crelcol. */
struct gopgext_relcol
{
	const char *name;
	int64		type;
	int64		typmod;
	int64		collation;
	const char *typname;
	int64		notnull;
	int64		dropped;
};

/* A trigger of a relation. Mirrored by pg.creltrig. */
struct gopgext_reltrig
{
	const char *name;
	int64		function;
	const char *funcname;
	int64		type;			/* tgtype */
	int64		enabled;		/* tgenabled */
	int64		internal;
	int64		has_when;
	int64		nargs;
	char	  **args;
	int64		ncols;
	int16	   *cols;
};

/* A relation. Mirrored by pg.crelinfo. */
struct gopgext_relinfo
{
	int64		id;
	const char *name;
	const char *schema;
	int64		owner;
	int64		kind;
	int64		row_security;
	int64		force_row_security;
	int64		has_subclass;
	int64		is_partition;
	int64		ncols;
	struct gopgext_relcol *cols;
	int64		npk;
	int16	   *pk;
	int64		ntrig;
	struct gopgext_reltrig *trigs;
};

ErrorData  *gopgext_relation_info(Oid relid, bool *found, struct gopgext_relinfo *out);
ErrorData  *gopgext_relation_invalidate(Oid relid, bool *found);
bool		gopgext_trigger_fires(int64 enabled);

struct gopgext_saved_user
{
	int64		userid;
	int64		sec_context;
	int64		nestlevel;
};

ErrorData  *gopgext_run_as_begin(Oid role, struct gopgext_saved_user *saved);
void		gopgext_run_as_end(struct gopgext_saved_user *saved);
ErrorData  *gopgext_image_get(Oid typ, Datum d, const char **ptr, int64 *len);
ErrorData  *gopgext_image_datum(Oid typ, const char *ptr, int64 len, Datum *out);

/*
 * Describes relation relid (*found = false if there's none). The relation
 * is locked with AccessShareLock until the end of the transaction, like
 * any relation a query reads. Everything is palloc'd in the current
 * memory context.
 */
ErrorData *
gopgext_relation_info(Oid relid, bool *found, struct gopgext_relinfo *out)
{
	GOPGEXT_GUARD({
		Relation	rel = try_relation_open(relid, AccessShareLock);
		TupleDesc	desc;
		Oid			pkindex;

		memset(out, 0, sizeof(*out));
		*found = rel != NULL;
		if (rel != NULL)
		{
			desc = RelationGetDescr(rel);
			out->id = relid;
			out->name = pstrdup(RelationGetRelationName(rel));
			out->schema = get_namespace_name(RelationGetNamespace(rel));
			out->owner = rel->rd_rel->relowner;
			out->kind = rel->rd_rel->relkind;
			out->row_security = rel->rd_rel->relrowsecurity;
			out->force_row_security = rel->rd_rel->relforcerowsecurity;
			out->has_subclass = rel->rd_rel->relhassubclass;
			out->is_partition = rel->rd_rel->relispartition;
			out->ncols = desc->natts;
			out->cols = palloc0(sizeof(struct gopgext_relcol) * Max(desc->natts, 1));
			for (int i = 0; i < desc->natts; i++)
			{
				Form_pg_attribute att = TupleDescAttr(desc, i);
				struct gopgext_relcol *c = &out->cols[i];

				c->name = pstrdup(NameStr(att->attname));
				c->dropped = att->attisdropped;
				if (att->attisdropped)
					continue;
				c->type = att->atttypid;
				c->typmod = att->atttypmod;
				c->collation = att->attcollation;
				c->typname = format_type_with_typemod(att->atttypid, att->atttypmod);
				c->notnull = att->attnotnull;
			}
			if (rel->rd_rel->relkind == RELKIND_RELATION ||
				rel->rd_rel->relkind == RELKIND_PARTITIONED_TABLE ||
				rel->rd_rel->relkind == RELKIND_MATVIEW)
			{
				pkindex = RelationGetPrimaryKeyIndex(rel, false);
				if (OidIsValid(pkindex))
				{
					HeapTuple	tup = SearchSysCache1(INDEXRELID, ObjectIdGetDatum(pkindex));

					if (HeapTupleIsValid(tup))
					{
						Form_pg_index idx = (Form_pg_index) GETSTRUCT(tup);

						out->npk = idx->indnkeyatts;
						out->pk = palloc(sizeof(int16) * Max(out->npk, 1));
						for (int i = 0; i < idx->indnkeyatts; i++)
							out->pk[i] = idx->indkey.values[i];
						ReleaseSysCache(tup);
					}
				}
			}
			if (rel->trigdesc != NULL)
			{
				out->ntrig = rel->trigdesc->numtriggers;
				out->trigs = palloc0(sizeof(struct gopgext_reltrig) * Max(out->ntrig, 1));
				for (int i = 0; i < rel->trigdesc->numtriggers; i++)
				{
					Trigger    *t = &rel->trigdesc->triggers[i];
					struct gopgext_reltrig *o = &out->trigs[i];

					o->name = pstrdup(t->tgname);
					o->function = t->tgfoid;
					o->funcname = get_func_name(t->tgfoid);
					o->type = t->tgtype;
					o->enabled = t->tgenabled;
					o->internal = t->tgisinternal;
					o->has_when = t->tgqual != NULL;
					o->nargs = t->tgnargs;
					o->args = palloc(sizeof(char *) * Max(t->tgnargs, 1));
					for (int a = 0; a < t->tgnargs; a++)
						o->args[a] = pstrdup(t->tgargs[a]);
					o->ncols = t->tgnattr;
					o->cols = palloc(sizeof(int16) * Max(t->tgnattr, 1));
					for (int a = 0; a < t->tgnattr; a++)
						o->cols[a] = t->tgattr[a];
				}
			}
			relation_close(rel, NoLock);
		}
	});
}

/*
 * Invalidates the relcache entry of relid in every backend when the
 * transaction commits (in this one at once): their cached plans using it
 * are made again, and extensions' caches of it reset.
 */
ErrorData *
gopgext_relation_invalidate(Oid relid, bool *found)
{
	GOPGEXT_GUARD({
		*found = SearchSysCacheExists1(RELOID, ObjectIdGetDatum(relid));
		if (*found)
			CacheInvalidateRelcacheByRelid(relid);
	});
}

/*
 * Whether a trigger with tgenabled enabled fires in this session
 * (session_replication_role decides for ORIGIN, REPLICA and ALWAYS
 * triggers).
 */
bool
gopgext_trigger_fires(int64 enabled)
{
	if (enabled == TRIGGER_DISABLED)
		return false;
	if (SessionReplicationRole == SESSION_REPLICATION_ROLE_REPLICA)
		return enabled == TRIGGER_FIRES_ON_REPLICA || enabled == TRIGGER_FIRES_ALWAYS;
	return enabled != TRIGGER_FIRES_ON_REPLICA;
}

/*
 * Switches to role, in a security-restricted operation with a safe
 * search_path (as maintenance commands run the code of a table's owner),
 * saving what gopgext_run_as_end restores.
 */
ErrorData *
gopgext_run_as_begin(Oid role, struct gopgext_saved_user *saved)
{
	GOPGEXT_GUARD({
		Oid			userid;
		int			sec;

		if (!SearchSysCacheExists1(AUTHOID, ObjectIdGetDatum(role)))
			ereport(ERROR,
					(errcode(ERRCODE_UNDEFINED_OBJECT),
					 errmsg("role with OID %u does not exist", role)));
		GetUserIdAndSecContext(&userid, &sec);
		saved->userid = userid;
		saved->sec_context = sec;
		SetUserIdAndSecContext(role, sec | SECURITY_LOCAL_USERID_CHANGE | SECURITY_RESTRICTED_OPERATION);
		saved->nestlevel = NewGUCNestLevel();
#if PG_VERSION_NUM >= 170000
		RestrictSearchPath();
#else
		(void) set_config_option("search_path", "pg_catalog, pg_temp", PGC_USERSET,
								 PGC_S_SESSION, GUC_ACTION_SAVE, true, 0, false);
#endif
	});
}

/* Restores the role and settings saved by gopgext_run_as_begin. */
void
gopgext_run_as_end(struct gopgext_saved_user *saved)
{
	AtEOXact_GUC(false, (int) saved->nestlevel);
	SetUserIdAndSecContext((Oid) saved->userid, (int) saved->sec_context);
}

/*
 * The binary image of Datum d of type typ: the Datum itself for types
 * passed by value (*ptr NULL), the bytes it points to for fixed-length
 * types, the (detoasted) varlena for variable-length ones, the string and
 * its terminator for cstrings.
 */
ErrorData *
gopgext_image_get(Oid typ, Datum d, const char **ptr, int64 *len)
{
	GOPGEXT_GUARD({
		int16		typlen;
		bool		typbyval;

		get_typlenbyval(typ, &typlen, &typbyval);
		if (typbyval)
		{
			*ptr = NULL;
			*len = sizeof(Datum);
		}
		else if (typlen > 0)
		{
			*ptr = DatumGetPointer(d);
			*len = typlen;
		}
		else if (typlen == -1)
		{
			struct varlena *v = pg_detoast_datum_packed((struct varlena *) DatumGetPointer(d));

			*ptr = (const char *) v;
			*len = VARSIZE_ANY(v);
		}
		else
		{
			*ptr = DatumGetCString(d);
			*len = strlen(*ptr) + 1;
		}
	});
}

/* A Datum of type typ from its binary image (copied into palloc memory). */
ErrorData *
gopgext_image_datum(Oid typ, const char *ptr, int64 len, Datum *out)
{
	GOPGEXT_GUARD({
		int16		typlen;
		bool		typbyval;

		get_typlenbyval(typ, &typlen, &typbyval);
		if (typbyval)
		{
			Datum		d = 0;

			if (len != sizeof(Datum))
				ereport(ERROR,
						(errcode(ERRCODE_INVALID_BINARY_REPRESENTATION),
						 errmsg("invalid image of a value of type %s", format_type_be(typ))));
			memcpy(&d, ptr, sizeof(Datum));
			*out = d;
		}
		else
		{
			char	   *copy;

			if ((typlen > 0 && len != typlen) ||
				(typlen == -1 && (len < 1 || VARSIZE_ANY(ptr) != (Size) len)) ||
				(typlen == -2 && (len < 1 || ptr[len - 1] != '\0')))
				ereport(ERROR,
						(errcode(ERRCODE_INVALID_BINARY_REPRESENTATION),
						 errmsg("invalid image of a value of type %s", format_type_be(typ))));
			copy = palloc(len);
			memcpy(copy, ptr, len);
			*out = PointerGetDatum(copy);
		}
	});
}
