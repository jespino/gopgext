-- A parser and a dictionary template from gopgext/tsearch.

CREATE FUNCTION tagprs_start(internal, int4) RETURNS internal
	AS 'MODULE_PATHNAME', 'gopgext_tsearch_start' LANGUAGE C STRICT;
CREATE FUNCTION tagprs_gettoken(internal, internal, internal) RETURNS internal
	AS 'MODULE_PATHNAME', 'gopgext_tsearch_gettoken' LANGUAGE C STRICT;
CREATE FUNCTION tagprs_end(internal) RETURNS void
	AS 'MODULE_PATHNAME', 'gopgext_tsearch_end' LANGUAGE C STRICT;
CREATE FUNCTION tagprs_lextype(internal) RETURNS internal
	AS 'MODULE_PATHNAME', 'gopgext_tsearch_lextype' LANGUAGE C STRICT;
CREATE TEXT SEARCH PARSER tagprs (START = tagprs_start, GETTOKEN = tagprs_gettoken,
	END = tagprs_end, LEXTYPES = tagprs_lextype, HEADLINE = prsd_headline);

CREATE FUNCTION compound_init(internal) RETURNS internal
	AS 'MODULE_PATHNAME', 'gopgext_tsearch_init' LANGUAGE C STRICT;
CREATE FUNCTION compound_lexize(internal, internal, internal, internal) RETURNS internal
	AS 'MODULE_PATHNAME', 'gopgext_tsearch_lexize' LANGUAGE C STRICT;
CREATE TEXT SEARCH TEMPLATE compound (INIT = compound_init, LEXIZE = compound_lexize);
CREATE TEXT SEARCH DICTIONARY compounds (TEMPLATE = compound, FILE = textsearch_compounds);

CREATE TEXT SEARCH CONFIGURATION tags (PARSER = tagprs);
ALTER TEXT SEARCH CONFIGURATION tags ADD MAPPING FOR word WITH compounds, simple;
ALTER TEXT SEARCH CONFIGURATION tags ADD MAPPING FOR tag, number WITH simple;
