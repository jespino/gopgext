-- PostgreSQL's regression test for autoinc (contrib/spi/sql/autoinc.sql),
-- with its expected results as assertions; the upstream test runs with
-- pg_regress, see README.md.
CREATE EXTENSION IF NOT EXISTS autoinc;

create sequence aitest_seq increment 10 start 0 minvalue 0;
create table aitest (price_id int4, price_val int4, price_on int4);
create trigger aiserial before insert or update on aitest
	for each row execute procedure autoinc (price_on, aitest_seq);

insert into aitest values (1, 1, null);
insert into aitest values (2, 2, 0);
insert into aitest values (3, 3, 1);
DO $$ BEGIN ASSERT (SELECT array_agg(price_on ORDER BY price_id) FROM aitest) = '{10,20,1}'; END $$;
update aitest set price_on = 11;
DO $$ BEGIN ASSERT (SELECT array_agg(price_on ORDER BY price_id) FROM aitest) = '{11,11,11}'; END $$;
update aitest set price_on = 0;
DO $$ BEGIN ASSERT (SELECT array_agg(price_on ORDER BY price_id) FROM aitest) = '{30,40,50}'; END $$;
update aitest set price_on = null;
DO $$ BEGIN ASSERT (SELECT array_agg(price_on ORDER BY price_id) FROM aitest) = '{60,70,80}'; END $$;

-- autoinc.example: 0 from the sequence is skipped
CREATE SEQUENCE next_id START -2 MINVALUE -2;
CREATE TABLE ids (id int4, idesc text);
CREATE TRIGGER ids_nextid BEFORE INSERT OR UPDATE ON ids
	FOR EACH ROW EXECUTE PROCEDURE autoinc (id, next_id);
INSERT INTO ids VALUES (0, 'first'), (null, 'second');
INSERT INTO ids(idesc) VALUES ('third');
DO $$ BEGIN ASSERT (SELECT array_agg(id ORDER BY idesc) FROM ids) = '{-2,-1,1}'; END $$;

-- errors
CREATE TABLE bad (id int8, n int4);
CREATE TRIGGER t1 BEFORE INSERT ON bad FOR EACH ROW EXECUTE PROCEDURE autoinc (id, next_id);
DO $$
BEGIN
	INSERT INTO bad VALUES (0, 0);
	RAISE 'expected an error';
EXCEPTION WHEN triggered_action_exception THEN
	ASSERT SQLERRM = 'attribute "id" of "bad" must be type INT4', SQLERRM;
END $$;
DROP TRIGGER t1 ON bad;
CREATE TRIGGER t1 BEFORE INSERT ON bad FOR EACH ROW EXECUTE PROCEDURE autoinc (nope, next_id);
DO $$
BEGIN
	INSERT INTO bad VALUES (0, 0);
	RAISE 'expected an error';
EXCEPTION WHEN triggered_action_exception THEN
	ASSERT SQLERRM = '"bad" has no attribute "nope"', SQLERRM;
END $$;
DROP TRIGGER t1 ON bad;
CREATE TRIGGER t1 BEFORE INSERT ON bad FOR EACH ROW EXECUTE PROCEDURE autoinc (n, no_such_seq);
DO $$
BEGIN
	INSERT INTO bad VALUES (0, 0);
	RAISE 'expected an error';
EXCEPTION WHEN undefined_table THEN
END $$;
DROP TRIGGER t1 ON bad;
CREATE TRIGGER t1 BEFORE INSERT ON bad FOR EACH ROW EXECUTE PROCEDURE autoinc (n);
DO $$
BEGIN
	INSERT INTO bad VALUES (0, 0);
	RAISE 'expected an error';
EXCEPTION WHEN internal_error THEN
	ASSERT SQLERRM = 'autoinc (bad): even number gt 0 of arguments was expected', SQLERRM;
END $$;
DROP TABLE aitest, ids, bad;
DROP SEQUENCE aitest_seq, next_id;
