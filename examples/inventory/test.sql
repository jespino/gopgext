CREATE EXTENSION inventory;

DO $$
DECLARE
	s text;
	n bigint;
BEGIN
	ASSERT greet('Ana') = 'Hello, Ana!';
	SET inventory.greeting = 'Hi';
	ASSERT greet('Ana') = 'Hi, Ana!', greet('Ana');
	RESET inventory.greeting;

	ASSERT add_item('apple', 3, 0.5) = 1;
	ASSERT add_item('pear', 10, NULL) = 2;
	ASSERT add_item('fig', 1, 2.25) = 3;
	ASSERT restock('kiwi', 2) = 'new item kiwi';
	ASSERT restock('apple', 1) = 'restocked apple: 4', restock('apple', 0);
	ASSERT (SELECT qty FROM items WHERE name = 'apple') = 4;

	ASSERT (SELECT string_agg(l, ',') FROM low_stock() l) = 'apple (4),fig (1),kiwi (2)';
	SET inventory.low_stock = 3;
	ASSERT (SELECT string_agg(l, ',') FROM low_stock() l) = 'fig (1),kiwi (2)';
	RESET inventory.low_stock;

	ASSERT price_of('fig') = 2.8125, price_of('fig');
	ASSERT price_of('pear') IS NULL;
	BEGIN
		PERFORM price_of('banana');
		RAISE 'expected an error';
	EXCEPTION WHEN no_data_found THEN
		ASSERT SQLERRM = 'no item "banana"', SQLERRM;
	END;

	s := render($q$SELECT 1::int AS a, 'x'::text AS b, NULL::int AS c, 2.5::numeric AS d,
	              '{"k": [1, 2]}'::jsonb AS e, '\x00ff'::bytea AS f, true AS g, 1.5::float8 AS h$q$);
	ASSERT s = E'a|b|c|d|e|f|g|h\n1|x|NULL|2.5|{"k": [1, 2]}|[0 255]|true|1.5', s;
	ASSERT render('SELECT name FROM items WHERE false') = 'name';

	-- errors from SPI, re-raised unchanged
	BEGIN
		PERFORM run('SELECT * FROM missing_table');
		RAISE 'expected an error';
	EXCEPTION WHEN undefined_table THEN
		ASSERT SQLERRM = 'relation "missing_table" does not exist', SQLERRM;
	END;
	BEGIN
		PERFORM run('INSERT INTO items (name, qty) VALUES (''bad'', -1)');
		RAISE 'expected an error';
	EXCEPTION WHEN check_violation THEN
	END;
	BEGIN
		PERFORM wrap_error('SELEC 1');
		RAISE 'expected an error';
	EXCEPTION WHEN syntax_error THEN
		ASSERT SQLERRM LIKE 'wrap_error: syntax error at or near "SELEC"%', SQLERRM;
	END;
	ASSERT try_write() LIKE '%not allowed in a non-volatile function%', try_write();
	ASSERT (SELECT count(*) FROM items WHERE name = 'nope') = 0;

	-- nesting: Go -> SPI -> Go, 50 levels deep
	ASSERT depth(50) = 50;

	ASSERT sleep(50) >= 50;
	ASSERT loads() = 1;
	ASSERT define_late() LIKE 'pg: settings must be defined while the library loads%', define_late();
END $$;

-- settings validation
DO $$
BEGIN
	SET inventory.low_stock = -1;
	RAISE 'expected an error';
EXCEPTION WHEN invalid_parameter_value THEN
END $$;
SET inventory.log_actions = on;
SELECT add_item('plum', 7, 1.0);
RESET inventory.log_actions;
SHOW inventory.markup;

-- cancellation: statement_timeout interrupts a long Go loop that checks for
-- interrupts, and PostgreSQL raises its own error
SELECT busy(0.2) > 0 AS uninterrupted;
SET statement_timeout = '300ms';
DO $$
BEGIN
	PERFORM busy(30);
	RAISE 'expected a timeout';
EXCEPTION WHEN query_canceled THEN
	ASSERT SQLERRM = 'canceling statement due to statement timeout', SQLERRM;
END $$;
RESET statement_timeout;

-- the failed statements left no trace; the transaction state is fine
SELECT count(*) = 5 AS five_items FROM items;

-- prepared statements: text arguments converted, NULL, reuse across calls
DO $$
BEGIN
	ASSERT set_qty('apple', '7') = 1;
	ASSERT (SELECT qty FROM items WHERE name = 'apple') = 7;
	ASSERT set_qty('nothing', '1') = 0;
	BEGIN
		PERFORM set_qty('apple', NULL);
		RAISE 'no error';
	EXCEPTION WHEN not_null_violation THEN
	END;
	ASSERT set_qty('apple', '8') = 1;
END $$;
DO $$
DECLARE
	errs text[] := prepare_errors();
BEGIN
	IF cardinality(errs) <> 3 OR errs[1] NOT LIKE '%"nope" does not exist%'
			OR errs[2] NOT LIKE '%parameter $1 of the prepared statement is integer, not bigint%'
			OR errs[3] NOT LIKE '%1 parameters, got 0%' THEN
		RAISE 'prepare_errors: %', errs;
	END IF;
END $$;
