-- Checks from PostgreSQL's own regression test for contrib/isn
-- (expected/isn.out), plus indexes over the Go-implemented types. The full
-- upstream suite runs with pg_regress, see README.md.
CREATE EXTENSION isn;

DO $$
DECLARE
	r record;
BEGIN
	ASSERT '9780123456786'::ean13::text = '978-0-12-345678-6';
	ASSERT '9790123456785'::ean13::text = '979-0-1234-5678-5';
	ASSERT '9771234567898'::ean13::text = '977-1234-567-89-8';
	ASSERT '0123456789012'::ean13::text = '012-345678901-2';
	ASSERT '1234567890128'::ean13::text = '123-456789012-8';
	ASSERT '9780123456786'::isbn::text = '0-12-345678-9';
	ASSERT '123456789X'::isbn::text = '1-234-56789-X';
	ASSERT '9780123456786'::ean13::isbn::text = '0-12-345678-9';
	ASSERT '9791234567896'::isbn::text = '979-123456789-6';
	ASSERT '123456789X'::isbn13::text = '978-1-234-56789-7';
	ASSERT 'M-1234-5678-5'::ismn::text = 'M-1234-5678-5';
	ASSERT 'M123456785'::ismn13::text = '979-0-1234-5678-5';
	ASSERT '12345679'::issn::text = '1234-5679';
	ASSERT '0123456789012'::ean13::upc::text = '123456789012';
	ASSERT '12345679'::issn = '9771234567003'::ean13;
	ASSERT '9791234567896'::ean13 != '123456789X'::isbn;

	BEGIN
		PERFORM '1234567890'::isbn;
		RAISE 'expected an error';
	EXCEPTION WHEN invalid_text_representation THEN
		ASSERT SQLERRM = 'invalid check digit for ISBN number: "1234567890", should be X', SQLERRM;
	END;
	BEGIN
		PERFORM '9790123456785'::isbn;
		RAISE 'expected an error';
	EXCEPTION WHEN invalid_text_representation THEN
		ASSERT SQLERRM = 'cannot cast ISMN to ISBN for number: "9790123456785"', SQLERRM;
	END;

	-- non-error-throwing input (soft errors)
	ASSERT NOT pg_input_is_valid('postgresql...', 'ean13');
	SELECT * INTO r FROM pg_input_error_info('9780123456786', 'upc');
	ASSERT (r.sql_error_code, r.message) = ('22P02', 'cannot cast ISBN to UPC for number: "9780123456786"'), r.message;
	ASSERT pg_input_is_valid('9771234567003', 'issn');

	-- weak mode
	PERFORM set_config('isn.weak', 'on', false);
	ASSERT '2222222222221'::ean13::text = '222-222222222-2!';
	ASSERT NOT is_valid('2222222222221'::ean13);
	ASSERT make_valid('2222222222221'::ean13)::text = '222-222222222-2';
	ASSERT isn_weak() AND NOT isn_weak(false) AND current_setting('isn.weak') = 'off';
END $$;

-- The types work with the upstream btree and hash operator classes.
CREATE TABLE books (id isbn13 PRIMARY KEY, title text);
INSERT INTO books SELECT ((978000000000 + i)::text || '?')::isbn13, 'book ' || i FROM generate_series(1, 20000) i;
CREATE INDEX books_hash ON books USING hash (id);
ANALYZE books;
SET enable_seqscan = off;
DO $$
BEGIN
	ASSERT (SELECT title FROM books WHERE id = '978-0-00-000100-?'::isbn13) = 'book 100';
	ASSERT (SELECT count(*) FROM books WHERE id BETWEEN '978000000100?'::isbn13 AND '978000000199?'::isbn13) = 100;
	ASSERT (SELECT id::text FROM books ORDER BY id LIMIT 1) = '978-0-00-000001-9';  -- isn has no min()
	ASSERT (SELECT count(*) FROM books WHERE id = '978-0-00-019999-?'::isbn) = 1;  -- cross-type, hash
END $$;
