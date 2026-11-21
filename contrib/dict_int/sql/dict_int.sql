-- upstream's dict_int--1.0.sql, with gopgext/tsearch's C symbols.

CREATE FUNCTION dintdict_init(internal)
        RETURNS internal
        AS 'MODULE_PATHNAME', 'gopgext_tsearch_init'
        LANGUAGE C STRICT;

CREATE FUNCTION dintdict_lexize(internal, internal, internal, internal)
        RETURNS internal
        AS 'MODULE_PATHNAME', 'gopgext_tsearch_lexize'
        LANGUAGE C STRICT;

CREATE TEXT SEARCH TEMPLATE intdict_template (
        LEXIZE = dintdict_lexize,
	INIT   = dintdict_init
);

CREATE TEXT SEARCH DICTIONARY intdict (
	TEMPLATE = intdict_template
);

COMMENT ON TEXT SEARCH DICTIONARY intdict IS 'dictionary for integers';
