/*
 * str_toupper() for dmetaphone, which upper-cases its input with the
 * database's default collation like the original C implementation.
 */
#include "gopgext.h"

#include "catalog/pg_collation.h"
#include "utils/formatting.h"

ErrorData *fuzzystrmatch_toupper(const char *s, int64 len, char **out);

ErrorData *
fuzzystrmatch_toupper(const char *s, int64 len, char **out)
{
	GOPGEXT_GUARD(*out = str_toupper(s, (size_t) len, DEFAULT_COLLATION_OID));
}
