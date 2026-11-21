/*
 * C side of gopgext/spgist: the support functions of SP-GiST operator
 * classes, implemented by the spgist.OpClass the extension registers in Go.
 *
 * As in gopgext/gist, one set of C symbols serves every operator class of
 * the extension: a call is dispatched to the OpClass registered under the
 * name of the operator family of the support function (looked up in
 * pg_amproc once per FmgrInfo).
 *
 * Prefixes, node labels and leaf values are bytea (or the indexed type for
 * leaves, without a compress function), and Go sees their bytes. Go never
 * sees SP-GiST structures: the inputs are flattened into the gopgext_spg_*
 * structs below (mirrored in lib/spgist/spgist.go: every field is 8 bytes),
 * and Go fills an output struct, which is copied into SP-GiST's structures
 * here. Arrays in the output structs point into Go's heap: they're copied
 * right after the call, before Go runs again.
 */
#include "postgres.h"

#include "access/genam.h"
#include "access/htup_details.h"
#include "access/spgist.h"
#include "access/stratnum.h"
#include "access/table.h"
#include "catalog/pg_amproc.h"
#include "catalog/pg_opfamily.h"
#include "catalog/pg_type.h"
#include "utils/fmgroids.h"
#include "utils/syscache.h"

#include "gopgext.h"

/* Go (gopgext/spgist), called through gopgext_invoke. */
void		gopgext_go_spgist_config(struct gopgext_call *call);
void		gopgext_go_spgist_compress(struct gopgext_call *call);
void		gopgext_go_spgist_choose(struct gopgext_call *call);
void		gopgext_go_spgist_picksplit(struct gopgext_call *call);
void		gopgext_go_spgist_inner_consistent(struct gopgext_call *call);
void		gopgext_go_spgist_leaf_consistent(struct gopgext_call *call);

/* Mirrored by spgist.go: keep in sync (all fields 8 bytes). */
struct gopgext_spg_key
{
	int64		strategy;
	Datum		query;
};

struct gopgext_spg_scan
{
	struct gopgext_spg_key *keys;
	int64		nkeys;
	struct gopgext_spg_key *orderbys;
	int64		norderbys;
	Datum		reconstructed;	/* 0: none */
	void	   *traversal;		/* a varlena, or NULL */
	int64		level;
	int64		return_data;
};

struct gopgext_spg_inner
{
	int64		all_the_same;
	Datum		prefix;			/* 0: none */
	int64		nnodes;
	Datum	   *labels;			/* NULL: none */
};

struct gopgext_spg_choose
{
	int64		action;			/* spgChooseResultType */
	int64		node;
	int64		level_add;
	Datum		rest;			/* 0: the current leaf datum */
	Datum		label;
	int64		prefix_has_prefix;
	Datum		prefix_prefix;
	int64		prefix_nnodes;
	Datum	   *prefix_labels;	/* NULL: none */
	int64		child_node;
	int64		postfix_has_prefix;
	Datum		postfix_prefix;
};

struct gopgext_spg_picksplit
{
	int64		has_prefix;
	Datum		prefix;
	int64		nnodes;
	Datum	   *labels;			/* NULL: none */
	int64	   *nodes;			/* node of each tuple */
	Datum	   *leaves;			/* new leaf datum of each tuple */
};

struct gopgext_spg_visits
{
	int64		n;
	int64	   *nodes;
	int64	   *level_adds;
	Datum	   *reconstructed;	/* NULL, or 0 for none */
	Datum	   *traversal;		/* NULL, or 0 for none */
	double	   *distances;		/* n * norderbys */
};

struct gopgext_spg_leaf
{
	int64		recheck;
	int64		recheck_distances;
	double	   *distances;		/* norderbys */
	Datum		value;			/* 0: the leaf datum */
};

/* Config flags (spgist.go). */
#define SPG_PREFIXES		1
#define SPG_LABELS			2
#define SPG_RETURN_DATA		4
#define SPG_LONG_VALUES		8
#define SPG_COMPRESS		16

/*
 * The name of the operator family of the support function being called:
 * the name its spgist.OpClass is registered under.
 */
static char *
spg_family(FunctionCallInfo fcinfo)
{
	FmgrInfo   *flinfo = fcinfo->flinfo;
	Relation	rel;
	ScanKeyData key;
	SysScanDesc scan;
	HeapTuple	tup;
	Oid			family = InvalidOid;
	char	   *name;

	if (flinfo == NULL)
		elog(ERROR, "gopgext/spgist support functions must be called by SP-GiST");
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
				 errmsg("function %u is not a support function of an SP-GiST operator class",
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

static void
check_count(int64 n, int64 max, const char *what)
{
	if (n < 0 || n > max)
		ereport(ERROR,
				(errcode(ERRCODE_INTERNAL_ERROR),
				 errmsg("spgist.OpClass returned an invalid %s: %lld", what, (long long) n)));
}

/* A palloc'd copy of n Datums (NULL for none). */
static Datum *
copy_datums(const Datum *src, int n)
{
	Datum	   *d;

	if (src == NULL)
		return NULL;
	d = palloc(sizeof(Datum) * Max(n, 1));
	memcpy(d, src, sizeof(Datum) * n);
	return d;
}

static struct gopgext_spg_key *
flatten_keys(ScanKey keys, int n)
{
	struct gopgext_spg_key *out = palloc(sizeof(*out) * Max(n, 1));

	for (int i = 0; i < n; i++)
	{
		out[i].strategy = keys[i].sk_strategy;
		out[i].query = (keys[i].sk_flags & SK_ISNULL) ? (Datum) 0 : keys[i].sk_argument;
	}
	return out;
}

PG_FUNCTION_INFO_V1(gopgext_spgist_config);
PG_FUNCTION_INFO_V1(gopgext_spgist_compress);
PG_FUNCTION_INFO_V1(gopgext_spgist_choose);
PG_FUNCTION_INFO_V1(gopgext_spgist_picksplit);
PG_FUNCTION_INFO_V1(gopgext_spgist_inner_consistent);
PG_FUNCTION_INFO_V1(gopgext_spgist_leaf_consistent);

/* config(internal, internal) RETURNS void */
Datum
gopgext_spgist_config(PG_FUNCTION_ARGS)
{
	spgConfigIn *in = (spgConfigIn *) PG_GETARG_POINTER(0);
	spgConfigOut *out = (spgConfigOut *) PG_GETARG_POINTER(1);
	Datum		args[1];
	int64		flags;

	args[0] = CStringGetDatum(spg_family(fcinfo));
	flags = DatumGetInt64(call_go(gopgext_go_spgist_config, 1, args));
	out->prefixType = (flags & SPG_PREFIXES) ? BYTEAOID : VOIDOID;
	out->labelType = (flags & SPG_LABELS) ? BYTEAOID : VOIDOID;
	out->leafType = (flags & SPG_COMPRESS) ? BYTEAOID : in->attType;
	out->canReturnData = (flags & SPG_RETURN_DATA) != 0;
	out->longValuesOK = (flags & SPG_LONG_VALUES) != 0;
	PG_RETURN_VOID();
}

/* compress(value) RETURNS bytea: an indexed value becomes its leaf value. */
Datum
gopgext_spgist_compress(PG_FUNCTION_ARGS)
{
	Datum		args[2];

	args[0] = CStringGetDatum(spg_family(fcinfo));
	args[1] = PG_GETARG_DATUM(0);
	return call_go(gopgext_go_spgist_compress, 2, args);
}

/* choose(internal, internal) RETURNS void: where does a new value go? */
Datum
gopgext_spgist_choose(PG_FUNCTION_ARGS)
{
	spgChooseIn *in = (spgChooseIn *) PG_GETARG_POINTER(0);
	spgChooseOut *out = (spgChooseOut *) PG_GETARG_POINTER(1);
	struct gopgext_spg_inner inner;
	struct gopgext_spg_choose res;
	Datum		args[6];

	inner.all_the_same = in->allTheSame;
	inner.prefix = in->hasPrefix ? in->prefixDatum : (Datum) 0;
	inner.nnodes = in->nNodes;
	inner.labels = in->nodeLabels;
	memset(&res, 0, sizeof(res));
	args[0] = CStringGetDatum(spg_family(fcinfo));
	args[1] = in->datum;
	args[2] = in->leafDatum;
	args[3] = Int64GetDatum(in->level);
	args[4] = PointerGetDatum(&inner);
	args[5] = PointerGetDatum(&res);
	call_go(gopgext_go_spgist_choose, 6, args);

	switch (res.action)
	{
		case spgMatchNode:
			check_count(res.node, in->nNodes - 1, "node number");
			out->resultType = spgMatchNode;
			out->result.matchNode.nodeN = (int) res.node;
			out->result.matchNode.levelAdd = (int) res.level_add;
			out->result.matchNode.restDatum = res.rest ? res.rest : in->leafDatum;
			break;
		case spgAddNode:
			check_count(res.node, in->nNodes, "node number");
			out->resultType = spgAddNode;
			out->result.addNode.nodeLabel = res.label;
			out->result.addNode.nodeN = (int) res.node;
			break;
		case spgSplitTuple:
			check_count(res.prefix_nnodes, INT_MAX, "number of nodes");
			check_count(res.child_node, res.prefix_nnodes - 1, "child node");
			out->resultType = spgSplitTuple;
			out->result.splitTuple.prefixHasPrefix = res.prefix_has_prefix != 0;
			out->result.splitTuple.prefixPrefixDatum = res.prefix_prefix;
			out->result.splitTuple.prefixNNodes = (int) res.prefix_nnodes;
			out->result.splitTuple.prefixNodeLabels = copy_datums(res.prefix_labels, (int) res.prefix_nnodes);
			out->result.splitTuple.childNodeN = (int) res.child_node;
			out->result.splitTuple.postfixHasPrefix = res.postfix_has_prefix != 0;
			out->result.splitTuple.postfixPrefixDatum = res.postfix_prefix;
			break;
		default:
			ereport(ERROR,
					(errcode(ERRCODE_INTERNAL_ERROR),
					 errmsg("spgist.OpClass Choose returned an invalid action %lld", (long long) res.action)));
	}
	PG_RETURN_VOID();
}

/* picksplit(internal, internal) RETURNS void: a new inner tuple for leaves. */
Datum
gopgext_spgist_picksplit(PG_FUNCTION_ARGS)
{
	spgPickSplitIn *in = (spgPickSplitIn *) PG_GETARG_POINTER(0);
	spgPickSplitOut *out = (spgPickSplitOut *) PG_GETARG_POINTER(1);
	struct gopgext_spg_picksplit res;
	Datum		args[5];
	int			n = in->nTuples;

	memset(&res, 0, sizeof(res));
	args[0] = CStringGetDatum(spg_family(fcinfo));
	args[1] = Int64GetDatum(n);
	args[2] = PointerGetDatum(in->datums);
	args[3] = Int64GetDatum(in->level);
	args[4] = PointerGetDatum(&res);
	call_go(gopgext_go_spgist_picksplit, 5, args);

	check_count(res.nnodes, INT_MAX, "number of nodes");
	out->hasPrefix = res.has_prefix != 0;
	out->prefixDatum = res.prefix;
	out->nNodes = (int) res.nnodes;
	out->nodeLabels = copy_datums(res.labels, (int) res.nnodes);
	out->mapTuplesToNodes = palloc(sizeof(int) * Max(n, 1));
	out->leafTupleDatums = palloc(sizeof(Datum) * Max(n, 1));
	for (int i = 0; i < n; i++)
	{
		check_count(res.nodes[i], res.nnodes - 1, "node number");
		out->mapTuplesToNodes[i] = (int) res.nodes[i];
		out->leafTupleDatums[i] = res.leaves[i];
	}
	PG_RETURN_VOID();
}

/*
 * inner_consistent(internal, internal) RETURNS void: which child nodes can
 * hold matches (and their distances, for ORDER BY)?
 */
Datum
gopgext_spgist_inner_consistent(PG_FUNCTION_ARGS)
{
	spgInnerConsistentIn *in = (spgInnerConsistentIn *) PG_GETARG_POINTER(0);
	spgInnerConsistentOut *out = (spgInnerConsistentOut *) PG_GETARG_POINTER(1);
	struct gopgext_spg_scan scan;
	struct gopgext_spg_inner inner;
	struct gopgext_spg_visits res;
	Datum		args[4];
	int			n;
	int			nob = in->norderbys;

	scan.keys = flatten_keys(in->scankeys, in->nkeys);
	scan.nkeys = in->nkeys;
	scan.orderbys = flatten_keys(in->orderbys, nob);
	scan.norderbys = nob;
	scan.reconstructed = in->reconstructedValue;
	scan.traversal = in->traversalValue;
	scan.level = in->level;
	scan.return_data = in->returnData;
	inner.all_the_same = in->allTheSame;
	inner.prefix = in->hasPrefix ? in->prefixDatum : (Datum) 0;
	inner.nnodes = in->nNodes;
	inner.labels = in->nodeLabels;
	memset(&res, 0, sizeof(res));
	args[0] = CStringGetDatum(spg_family(fcinfo));
	args[1] = PointerGetDatum(&scan);
	args[2] = PointerGetDatum(&inner);
	args[3] = PointerGetDatum(&res);
	call_go(gopgext_go_spgist_inner_consistent, 4, args);

	check_count(res.n, in->nNodes, "number of nodes to visit");
	n = (int) res.n;
	out->nNodes = n;
	out->nodeNumbers = palloc(sizeof(int) * Max(n, 1));
	out->levelAdds = palloc(sizeof(int) * Max(n, 1));
	out->reconstructedValues = res.reconstructed ? palloc(sizeof(Datum) * Max(n, 1)) : NULL;
	out->traversalValues = res.traversal ? palloc(sizeof(void *) * Max(n, 1)) : NULL;
	out->distances = nob > 0 ? palloc(sizeof(double *) * Max(n, 1)) : NULL;
	for (int i = 0; i < n; i++)
	{
		check_count(res.nodes[i], in->nNodes - 1, "node number");
		out->nodeNumbers[i] = (int) res.nodes[i];
		out->levelAdds[i] = (int) res.level_adds[i];
		if (res.reconstructed)
			out->reconstructedValues[i] = res.reconstructed[i];
		if (res.traversal)
		{
			/* Traversal values live in traversalMemoryContext. */
			struct varlena *v = (struct varlena *) DatumGetPointer(res.traversal[i]);

			out->traversalValues[i] = NULL;
			if (v)
			{
				Size		size = VARSIZE_ANY(v);

				out->traversalValues[i] = MemoryContextAlloc(in->traversalMemoryContext, size);
				memcpy(out->traversalValues[i], v, size);
			}
		}
		if (nob > 0)
		{
			out->distances[i] = palloc(sizeof(double) * nob);
			memcpy(out->distances[i], res.distances + i * nob, sizeof(double) * nob);
		}
	}
	PG_RETURN_VOID();
}

/* leaf_consistent(internal, internal) RETURNS boolean: does a leaf match? */
Datum
gopgext_spgist_leaf_consistent(PG_FUNCTION_ARGS)
{
	spgLeafConsistentIn *in = (spgLeafConsistentIn *) PG_GETARG_POINTER(0);
	spgLeafConsistentOut *out = (spgLeafConsistentOut *) PG_GETARG_POINTER(1);
	struct gopgext_spg_scan scan;
	struct gopgext_spg_leaf res;
	Datum		args[4];
	Datum		match;
	int			nob = in->norderbys;

	scan.keys = flatten_keys(in->scankeys, in->nkeys);
	scan.nkeys = in->nkeys;
	scan.orderbys = flatten_keys(in->orderbys, nob);
	scan.norderbys = nob;
	scan.reconstructed = in->reconstructedValue;
	scan.traversal = in->traversalValue;
	scan.level = in->level;
	scan.return_data = in->returnData;
	memset(&res, 0, sizeof(res));
	args[0] = CStringGetDatum(spg_family(fcinfo));
	args[1] = PointerGetDatum(&scan);
	args[2] = in->leafDatum;
	args[3] = PointerGetDatum(&res);
	match = call_go(gopgext_go_spgist_leaf_consistent, 4, args);

	out->recheck = res.recheck != 0;
	out->recheckDistances = res.recheck_distances != 0;
	out->leafValue = (Datum) 0;
	if (in->returnData)
		out->leafValue = res.value ? res.value : in->leafDatum;
	out->distances = NULL;
	if (nob > 0 && DatumGetBool(match))
	{
		out->distances = palloc(sizeof(double) * nob);
		memcpy(out->distances, res.distances, sizeof(double) * nob);
	}
	return match;
}
