-- Checks from PostgreSQL's regression test for refint (contrib/spi/
-- sql/refint.sql), plus setnull; the upstream test runs with pg_regress,
-- see README.md.
CREATE EXTENSION IF NOT EXISTS refint;

create table pkeys (pkey1 int4 not null, pkey2 text not null);
create table fkeys (fkey1 int4, fkey2 text, fkey3 int);
create table fkeys2 (fkey21 int4, fkey22 text, pkey23 int not null);
insert into pkeys values (10, '1'), (20, '2'), (30, '3'), (40, '4'), (50, '5'), (60, '6');
create unique index pkeys_i on pkeys (pkey1, pkey2);
create trigger check_fkeys_pkey_exist after insert or update on fkeys for each row
	execute function check_primary_key ('fkey1', 'fkey2', 'pkeys', 'pkey1', 'pkey2');
create trigger check_fkeys_pkey2_exist after insert or update on fkeys for each row
	execute function check_primary_key ('fkey3', 'fkeys2', 'pkey23');
create trigger check_fkeys2_pkey_exist after insert or update on fkeys2 for each row
	execute procedure check_primary_key ('fkey21', 'fkey22', 'pkeys', 'pkey1', 'pkey2');
create trigger check_pkeys_fkey_cascade after delete or update on pkeys for each row
	execute procedure check_foreign_key (2, 'cascade', 'pkey1', 'pkey2',
	'fkeys', 'fkey1', 'fkey2', 'fkeys2', 'fkey21', 'fkey22');
create trigger check_fkeys2_fkey_restrict after delete or update on fkeys2 for each row
	execute procedure check_foreign_key (1, 'restrict', 'pkey23', 'fkeys', 'fkey3');

insert into fkeys2 values (10, '1', 1), (30, '3', 2), (40, '4', 5), (50, '5', 3);
insert into fkeys values (10, '1', 2), (30, '3', 3), (40, '4', 2), (50, '5', 2);
-- NULL keys aren't checked
insert into fkeys values (null, '1', null);
DO $$
BEGIN
	insert into fkeys2 values (70, '5', 3);
	RAISE 'expected an error';
EXCEPTION WHEN triggered_action_exception THEN
	ASSERT SQLERRM = 'tuple references non-existent key', SQLERRM;
END $$;
DO $$
DECLARE d text;
BEGIN
	insert into fkeys values (60, '6', 4);
	RAISE 'expected an error';
EXCEPTION WHEN triggered_action_exception THEN
	GET STACKED DIAGNOSTICS d = PG_EXCEPTION_DETAIL;
	ASSERT d = 'Trigger "check_fkeys_pkey2_exist" found tuple referencing non-existent key in "fkeys2".', d;
END $$;
-- cascade into fkeys, then restrict from fkeys2: all rolled back
DO $$
DECLARE c text;
BEGIN
	delete from pkeys where pkey1 = 30 and pkey2 = '3';
	RAISE 'expected an error';
EXCEPTION WHEN triggered_action_exception THEN
	GET STACKED DIAGNOSTICS c = PG_EXCEPTION_CONTEXT;
	ASSERT SQLERRM = '"check_fkeys2_fkey_restrict": tuple is referenced in "fkeys"', SQLERRM;
	ASSERT c LIKE 'SQL statement "delete from fkeys2 where fkey21 = $1 and fkey22 = $2 "%', c;
END $$;
DO $$ BEGIN ASSERT (SELECT count(*) FROM fkeys WHERE fkey1 = 30) = 1; END $$;
delete from pkeys where pkey1 = 40 and pkey2 = '4';
DO $$ BEGIN ASSERT NOT EXISTS (SELECT FROM fkeys WHERE fkey1 = 40) AND NOT EXISTS (SELECT FROM fkeys2 WHERE fkey21 = 40); END $$;
update pkeys set pkey1 = 7, pkey2 = '70' where pkey1 = 50 and pkey2 = '5';
DO $$ BEGIN ASSERT (SELECT (fkey1, fkey2) FROM fkeys WHERE fkey3 = 2 AND fkey1 = 7) = (7, '70'::text); END $$;
DO $$ BEGIN ASSERT (SELECT (fkey21, fkey22) FROM fkeys2 WHERE pkey23 = 3) = (7, '70'::text); END $$;
-- an update keeping the key does nothing
update pkeys set pkey2 = pkey2;

-- setnull, with a quote in a key
create table p (k text);
create table f (fk text);
create trigger p_setnull after delete or update on p for each row
	execute procedure check_foreign_key (1, 'SetNull', 'k', 'f', 'fk');
insert into p values ('it''s'), ('a\b');
insert into f values ('it''s'), ('a\b'), ('a\b');
update p set k = k || '!';
DO $$ BEGIN ASSERT (SELECT count(*) FROM f WHERE fk IS NULL) = 3; END $$;
drop trigger p_setnull on p;
create trigger p_cascade after update on p for each row
	execute procedure check_foreign_key (1, 'cascade', 'k', 'f', 'fk');
update f set fk = 'it''s!';
update p set k = 'it''s?' where k = 'it''s!';
DO $$ BEGIN ASSERT (SELECT count(*) FROM f WHERE fk = 'it''s?') = 3; END $$;
insert into p values ('x\y');
insert into f values ('x\y');
update p set k = 'z''\w' where k = 'x\y';
DO $$ BEGIN ASSERT (SELECT count(*) FROM f WHERE fk = 'z''\w') = 1; END $$;

-- errors
create trigger bad after delete on f for each row execute procedure check_foreign_key (1, 'x', 'fk', 'p', 'k');
DO $$
BEGIN
	delete from f;
	RAISE 'expected an error';
EXCEPTION WHEN internal_error THEN
	ASSERT SQLERRM = 'check_foreign_key: invalid action x', SQLERRM;
END $$;
drop trigger bad on f;
create trigger bad after insert on f for each row execute procedure check_primary_key ('nope', 'p', 'k');
DO $$
BEGIN
	insert into f values ('x');
	RAISE 'expected an error';
EXCEPTION WHEN undefined_column THEN
	ASSERT SQLERRM = 'there is no attribute "nope" in relation "f"', SQLERRM;
END $$;
drop trigger bad on f;
create trigger bad before insert on f for each row execute procedure check_primary_key ('fk', 'p', 'k');
DO $$
BEGIN
	insert into f values ('x');
	RAISE 'expected an error';
EXCEPTION WHEN internal_error THEN
	ASSERT SQLERRM = 'check_primary_key: must be fired by AFTER trigger', SQLERRM;
END $$;
DROP TABLE pkeys, fkeys, fkeys2, p, f;
