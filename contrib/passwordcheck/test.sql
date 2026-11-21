-- passwordcheck: upstream's regression test (sql/passwordcheck.sql) with
-- assertions, plus VALID UNTIL and SCRAM passwords. The library is loaded
-- with LOAD, as upstream's test does.

SET client_min_messages = error; -- no md5 deprecation warnings (18)
LOAD 'passwordcheck';
DROP USER IF EXISTS regress_passwordcheck_user1, regress_passwordcheck_user2;
CREATE USER regress_passwordcheck_user1;

CREATE OR REPLACE FUNCTION pwcheck(pw text, err text DEFAULT NULL, detail text DEFAULT NULL) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
	msg text;
	det text;
	code text;
BEGIN
	EXECUTE format('ALTER USER regress_passwordcheck_user1 PASSWORD %L', pw);
	IF err IS NOT NULL THEN
		RAISE EXCEPTION 'password % accepted, expected: %', pw, err;
	END IF;
EXCEPTION WHEN invalid_parameter_value THEN
	GET STACKED DIAGNOSTICS msg = MESSAGE_TEXT, det = PG_EXCEPTION_DETAIL, code = RETURNED_SQLSTATE;
	IF err IS NULL OR msg <> err OR det IS DISTINCT FROM coalesce(detail, '') THEN
		RAISE EXCEPTION 'password %: got "%" (%), expected "%" (%)', pw, msg, det, err, detail;
	END IF;
END $$;

-- ok
SELECT pwcheck('a_nice_long_password');
-- error: too short
SELECT pwcheck('tooshrt', 'password is too short',
	'password must be at least "passwordcheck.min_password_length" (8) bytes long');
-- ok
SET passwordcheck.min_password_length = 6;
SELECT pwcheck('v_shrt');
-- error: contains user name
SELECT pwcheck('xyzregress_passwordcheck_user1', 'password must not contain user name');
-- error: contains only letters
SELECT pwcheck('alessnicelongpassword', 'password must contain both letters and nonletters');
-- error: only non-letters (non-ASCII bytes are non-letters)
SELECT pwcheck('12345678', 'password must contain both letters and nonletters');
SELECT pwcheck('ééééééé', 'password must contain both letters and nonletters');
SELECT pwcheck('ééééééx');
-- encrypted ok (password is "secret")
SELECT pwcheck('md592350e12ac34e52dd598f90893bb3ae7');
-- error: password is user name
SELECT pwcheck('md507a112732ed9f2087fa90b192d44e358', 'password must not equal user name');
SET password_encryption = 'scram-sha-256';
-- SCRAM: the same check. user2's password is user1's name.
CREATE USER regress_passwordcheck_user2 PASSWORD 'regress_passwordcheck_user1';
SELECT rolpassword AS scram FROM pg_authid WHERE rolname = 'regress_passwordcheck_user2' \gset
SELECT pwcheck(:'scram', 'password must not equal user name');
ALTER USER regress_passwordcheck_user2 PASSWORD 'something_else1';
SELECT rolpassword AS scram FROM pg_authid WHERE rolname = 'regress_passwordcheck_user2' \gset
SELECT pwcheck(:'scram');
-- Plain text passwords are checked before being encrypted.
SELECT pwcheck('abc', 'password is too short',
	'password must be at least "passwordcheck.min_password_length" (6) bytes long');
SELECT pwcheck('hello world');
DO $$ BEGIN
	ASSERT (SELECT rolpassword LIKE 'SCRAM-SHA-256$%' FROM pg_authid WHERE rolname = 'regress_passwordcheck_user1');
END $$;
-- VALID UNTIL is accepted (passwordcheck ignores it).
ALTER USER regress_passwordcheck_user1 PASSWORD 'another_one' VALID UNTIL '2100-01-01';
ALTER USER regress_passwordcheck_user1 PASSWORD 'another_one' VALID UNTIL 'infinity';
DO $$
BEGIN
	CREATE USER regress_passwordcheck_user3 PASSWORD 'short';
	RAISE EXCEPTION 'CREATE USER with a short password accepted';
EXCEPTION WHEN invalid_parameter_value THEN
END $$;

DROP USER regress_passwordcheck_user1, regress_passwordcheck_user2;
DROP FUNCTION pwcheck;
