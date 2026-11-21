-- upstream's unaccent--1.1.sql, with gopgext/tsearch's C symbols (and a
-- symbol of its own for the one-argument unaccent()).

CREATE FUNCTION unaccent(regdictionary, text)
	RETURNS text
	AS 'MODULE_PATHNAME', 'unaccent_dict'
	LANGUAGE C STABLE STRICT PARALLEL SAFE;

CREATE FUNCTION unaccent(text)
	RETURNS text
	AS 'MODULE_PATHNAME', 'unaccent_dict_default'
	LANGUAGE C STABLE STRICT PARALLEL SAFE;

CREATE FUNCTION unaccent_init(internal)
	RETURNS internal
	AS 'MODULE_PATHNAME', 'gopgext_tsearch_init'
	LANGUAGE C PARALLEL SAFE;

CREATE FUNCTION unaccent_lexize(internal,internal,internal,internal)
	RETURNS internal
	AS 'MODULE_PATHNAME', 'gopgext_tsearch_lexize'
	LANGUAGE C PARALLEL SAFE;

CREATE TEXT SEARCH TEMPLATE unaccent (
	INIT = unaccent_init,
	LEXIZE = unaccent_lexize
);

CREATE TEXT SEARCH DICTIONARY unaccent (
	TEMPLATE = unaccent,
	RULES    = 'unaccent'
);
