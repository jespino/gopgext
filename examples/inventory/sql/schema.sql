CREATE TABLE items (
	id    bigserial PRIMARY KEY,
	name  text NOT NULL UNIQUE,
	qty   integer NOT NULL CHECK (qty >= 0),
	price numeric(10, 2)
);
SELECT pg_catalog.pg_extension_config_dump('items', '');
