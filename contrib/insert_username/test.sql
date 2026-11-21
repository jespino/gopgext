-- insert_username.example from PostgreSQL's contrib/spi, as assertions.
CREATE EXTENSION IF NOT EXISTS insert_username;

CREATE TABLE username_test (name text, username text not null);
CREATE TRIGGER insert_usernames BEFORE INSERT OR UPDATE ON username_test
	FOR EACH ROW EXECUTE PROCEDURE insert_username (username);
INSERT INTO username_test VALUES ('nothing');
INSERT INTO username_test VALUES ('null', null);
INSERT INTO username_test VALUES ('empty string', '');
INSERT INTO username_test VALUES ('name', 'name');
DO $$ BEGIN ASSERT (SELECT count(*) FROM username_test WHERE username = current_user) = 4; END $$;

CREATE ROLE gopgext_iu_user;
GRANT ALL ON username_test TO gopgext_iu_user;
SET ROLE gopgext_iu_user;
UPDATE username_test SET username = 'x' WHERE name = 'name';
RESET ROLE;
DO $$ BEGIN ASSERT (SELECT username FROM username_test WHERE name = 'name') = 'gopgext_iu_user'; END $$;

-- errors
CREATE TABLE bad (name varchar);
CREATE TRIGGER t BEFORE INSERT ON bad FOR EACH ROW EXECUTE PROCEDURE insert_username (name);
DO $$
BEGIN
	INSERT INTO bad VALUES ('x');
	RAISE 'expected an error';
EXCEPTION WHEN triggered_action_exception THEN
	ASSERT SQLERRM = 'attribute "name" of "bad" must be type TEXT', SQLERRM;
END $$;
DROP TRIGGER t ON bad;
CREATE TRIGGER t BEFORE DELETE ON bad FOR EACH ROW EXECUTE PROCEDURE insert_username (name);
INSERT INTO bad VALUES ('x');
DO $$
BEGIN
	DELETE FROM bad;
	RAISE 'expected an error';
EXCEPTION WHEN internal_error THEN
	ASSERT SQLERRM = 'insert_username: cannot process DELETE events', SQLERRM;
END $$;
DROP TABLE username_test, bad;
DROP ROLE gopgext_iu_user;
