/*
 * C side of gopgext/trigger: trigger and event trigger functions written in
 * Go.
 *
 * PostgreSQL's structures (TriggerData, the relation, its tuples) stay
 * here: Go asks for the trigger's description, the columns of the
 * relation, and the old and new rows deformed into Datum arrays (valid
 * during the call). The result is formed here too: the original tuple, a
 * copy with the columns Go changed (heap_modify_tuple), or NULL.
 */
#include "postgres.h"

#include "access/htup_details.h"
#include "access/sysattr.h"
#include "commands/event_trigger.h"
#include "commands/trigger.h"
#include "tcop/cmdtag.h"
#include "utils/builtins.h"
#include "utils/datum.h"
#include "utils/lsyscache.h"
#include "utils/rel.h"

#include "gopgext.h"

/* The trigger's description. Mirrored by trigger.cinfo. */
struct trigger_info
{
	int64		when;			/* 0 BEFORE, 1 AFTER, 2 INSTEAD OF */
	int64		op;				/* 0 INSERT, 1 UPDATE, 2 DELETE, 3 TRUNCATE */
	int64		row;			/* FOR EACH ROW */
	int64		nargs;
	int64		natts;
	int64		relid;
	int64		has_old;		/* the old row (UPDATE, DELETE) is there */
	int64		has_new;		/* the new row (INSERT, UPDATE) is there */
	const char *name;
	const char *relname;
	const char *nspname;
	char	  **args;
};

/* A column of the relation. Mirrored by trigger.ccolumn. */
struct trigger_column
{
	const char *name;
	const char *typname;		/* format_type_with_typemod */
	int64		type;
	int64		basetype;		/* type, or the base type of a domain */
	int64		typmod;
	int64		typlen;
	int64		dropped;
	int64		updated;		/* assigned by the UPDATE (tg_updatedcols) */
};

ErrorData  *gopgext_trigger_info(struct gopgext_call *call, struct trigger_info *out);
ErrorData  *gopgext_trigger_columns(struct gopgext_call *call, struct trigger_column *out);
ErrorData  *gopgext_trigger_row(struct gopgext_call *call, int64 which, Datum *values, bool *nulls);
ErrorData  *gopgext_trigger_return(struct gopgext_call *call, int64 which, Datum *values,
								   bool *nulls, bool *replace);
ErrorData  *gopgext_trigger_input(Oid typ, int32 typmod, const char *s, Datum *out);
ErrorData  *gopgext_trigger_equal(Oid typ, Datum a, Datum b, bool *out);
ErrorData  *gopgext_trigger_event(struct gopgext_call *call, const char **event, const char **tag);

static TriggerData *
trigger_data(struct gopgext_call *call)
{
	if (call->fcinfo == NULL || !CALLED_AS_TRIGGER(call->fcinfo))
		ereport(ERROR,
				(errcode(ERRCODE_FEATURE_NOT_SUPPORTED),
				 errmsg("trigger functions can only be called as triggers")));
	return (TriggerData *) call->fcinfo->context;
}

/* The old (which = 0) or new (1) row: NULL if there is none. */
static HeapTuple
trigger_tuple(TriggerData *td, int64 which)
{
	TriggerEvent ev = td->tg_event;

	if (!TRIGGER_FIRED_FOR_ROW(ev))
		return NULL;
	if (which == 0)
		return TRIGGER_FIRED_BY_UPDATE(ev) || TRIGGER_FIRED_BY_DELETE(ev) ? td->tg_trigtuple : NULL;
	if (TRIGGER_FIRED_BY_INSERT(ev))
		return td->tg_trigtuple;
	return TRIGGER_FIRED_BY_UPDATE(ev) ? td->tg_newtuple : NULL;
}

ErrorData *
gopgext_trigger_info(struct gopgext_call *call, struct trigger_info *out)
{
	GOPGEXT_GUARD({
		TriggerData *td = trigger_data(call);
		TriggerEvent ev = td->tg_event;
		Relation	rel = td->tg_relation;

		memset(out, 0, sizeof(*out));
		out->when = TRIGGER_FIRED_BEFORE(ev) ? 0 : TRIGGER_FIRED_AFTER(ev) ? 1 : 2;
		out->op = TRIGGER_FIRED_BY_INSERT(ev) ? 0 : TRIGGER_FIRED_BY_UPDATE(ev) ? 1 :
			TRIGGER_FIRED_BY_DELETE(ev) ? 2 : 3;
		out->row = TRIGGER_FIRED_FOR_ROW(ev);
		out->nargs = td->tg_trigger->tgnargs;
		out->args = td->tg_trigger->tgargs;
		out->natts = RelationGetDescr(rel)->natts;
		out->relid = RelationGetRelid(rel);
		out->has_old = trigger_tuple(td, 0) != NULL;
		out->has_new = trigger_tuple(td, 1) != NULL;
		out->name = td->tg_trigger->tgname;
		out->relname = RelationGetRelationName(rel);
		out->nspname = get_namespace_name(RelationGetNamespace(rel));
	});
}

ErrorData *
gopgext_trigger_columns(struct gopgext_call *call, struct trigger_column *out)
{
	GOPGEXT_GUARD({
		TriggerData *td = trigger_data(call);
		TupleDesc	desc = RelationGetDescr(td->tg_relation);
		int			i;

		for (i = 0; i < desc->natts; i++)
		{
			Form_pg_attribute att = TupleDescAttr(desc, i);
			struct trigger_column *c = &out[i];

			memset(c, 0, sizeof(*c));
			c->name = NameStr(att->attname);
			c->dropped = att->attisdropped;
			if (c->dropped)
				continue;
			c->type = att->atttypid;
			c->basetype = getBaseType(att->atttypid);
			c->typmod = att->atttypmod;
			c->typlen = att->attlen;
			c->typname = format_type_with_typemod(att->atttypid, att->atttypmod);
			c->updated = TRIGGER_FIRED_BY_UPDATE(td->tg_event) &&
				bms_is_member(i + 1 - FirstLowInvalidHeapAttributeNumber, td->tg_updatedcols);
		}
	});
}

/* Deforms the old or new row into values and nulls (natts entries). */
ErrorData *
gopgext_trigger_row(struct gopgext_call *call, int64 which, Datum *values, bool *nulls)
{
	GOPGEXT_GUARD({
		TriggerData *td = trigger_data(call);
		HeapTuple	tuple = trigger_tuple(td, which);

		if (tuple == NULL)
			elog(ERROR, "trigger has no %s row", which ? "new" : "old");
		heap_deform_tuple(tuple, RelationGetDescr(td->tg_relation), values, nulls);
	});
}

/*
 * Sets the result: the old or new row (which = 0, 1) with the replaced
 * columns (replace NULL: unchanged), or NULL (which < 0). A trigger function
 * returns NULL as a null pointer, not as SQL NULL.
 */
ErrorData *
gopgext_trigger_return(struct gopgext_call *call, int64 which, Datum *values,
					   bool *nulls, bool *replace)
{
	GOPGEXT_GUARD({
		TriggerData *td = trigger_data(call);
		HeapTuple	tuple = which < 0 ? NULL : trigger_tuple(td, which);

		if (tuple != NULL && replace != NULL)
			tuple = heap_modify_tuple(tuple, RelationGetDescr(td->tg_relation),
									  values, nulls, replace);
		call->result = PointerGetDatum(tuple);
	});
}

/*
 * A value of a column of type typ (typmod) from its text form, through the
 * type's input function: a domain's checks and a typmod's limits apply. s
 * NULL is SQL NULL (checked by domains).
 */
ErrorData *
gopgext_trigger_input(Oid typ, int32 typmod, const char *s, Datum *out)
{
	GOPGEXT_GUARD({
		Oid			fn;
		Oid			ioparam;
		FmgrInfo	flinfo;

		getTypeInputInfo(typ, &fn, &ioparam);
		fmgr_info(fn, &flinfo);
		*out = InputFunctionCall(&flinfo, (char *) s, ioparam, typmod);
	});
}

/* Whether two values of type typ are identical (datum_image_eq). */
ErrorData *
gopgext_trigger_equal(Oid typ, Datum a, Datum b, bool *out)
{
	GOPGEXT_GUARD({
		int16		typlen;
		bool		byval;

		get_typlenbyval(typ, &typlen, &byval);
		*out = datum_image_eq(a, b, byval, typlen);
	});
}

/* The event and command tag of an event trigger. */
ErrorData *
gopgext_trigger_event(struct gopgext_call *call, const char **event, const char **tag)
{
	GOPGEXT_GUARD({
		EventTriggerData *ed;

		if (call->fcinfo == NULL || !CALLED_AS_EVENT_TRIGGER(call->fcinfo))
			ereport(ERROR,
					(errcode(ERRCODE_FEATURE_NOT_SUPPORTED),
					 errmsg("event trigger functions can only be called as event triggers")));
		ed = (EventTriggerData *) call->fcinfo->context;
		*event = ed->event;
		*tag = GetCommandTagName(ed->tag);
	});
}
