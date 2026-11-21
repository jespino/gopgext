-- crypt() and gen_salt(): known answers (upstream's regression tests,
-- Drepper's SHA-crypt specification, OpenBSD/Openwall bcrypt vectors),
-- errors, and round trips through gen_salt() for every algorithm.
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE FUNCTION pg_temp.err(q text) RETURNS text AS $$
BEGIN
	EXECUTE q;
	RETURN 'no error';
EXCEPTION WHEN OTHERS THEN
	RETURN SQLSTATE || ': ' || SQLERRM;
END $$ LANGUAGE plpgsql;

DO $$
DECLARE
	t text;
	r int;
	s text;
BEGIN
	-- des, xdes
	ASSERT crypt('', 'NB') = 'NBPx/38Y48kHg';
	ASSERT crypt('foox', 'NB') = 'NB53EGGqrrb5E';
	ASSERT crypt('foox', 'NB53EGGqrrb5E') = 'NB53EGGqrrb5E';
	ASSERT crypt('', '_J9..j2zz') = '_J9..j2zzR/nIRDK3pPc';
	ASSERT crypt('foox', '_J9..j2zz') = '_J9..j2zzAYKMvO2BYRY';
	ASSERT crypt('longlongpassword', '_J9..j2zz') = '_J9..j2zz4BeseiQNwUg';
	ASSERT crypt('password', '_/!!!!!!!') = '_/!!!!!!!zqM49hRzxko';
	-- md5
	ASSERT crypt('', '$1$Szzz0yzz') = '$1$Szzz0yzz$To38XrR3BsbXQW2ZpfKjF1';
	ASSERT crypt('foox', '$1$Szzz0yzz') = '$1$Szzz0yzz$IYL49cd3t9bllsA7Jmz1M1';
	-- bcrypt
	ASSERT crypt('', '$2a$06$RQiOJ.3ELirrXwxIZY8q0O') = '$2a$06$RQiOJ.3ELirrXwxIZY8q0OlGbBEpDmx7IRZlNYvGJ1SHXwNi2cEKK';
	ASSERT crypt('foox', '$2a$06$RQiOJ.3ELirrXwxIZY8q0O') = '$2a$06$RQiOJ.3ELirrXwxIZY8q0OR3CVJrAfda1z26CCHPnB6mmVZD8p0/C';
	ASSERT crypt('U*U', '$2a$05$CCCCCCCCCCCCCCCCCCCCC.') = '$2a$05$CCCCCCCCCCCCCCCCCCCCC.E5YPO9kmyuRGyh0XouQYb4YMJKvyOeW';
	ASSERT crypt('U*U*', '$2a$05$CCCCCCCCCCCCCCCCCCCCC.') = '$2a$05$CCCCCCCCCCCCCCCCCCCCC.VGOzA784oUp/Z0DY336zx7pLYAy0lwK';
	ASSERT crypt('', '$2a$05$CCCCCCCCCCCCCCCCCCCCC.') = '$2a$05$CCCCCCCCCCCCCCCCCCCCC.7uG0VCzI2bS7j6ymqJi9CdcdxiRTWNy';
	-- $2x$ only differs from $2a$ for 8-bit characters
	ASSERT crypt('U*U', '$2x$05$CCCCCCCCCCCCCCCCCCCCC.') = '$2x$05$CCCCCCCCCCCCCCCCCCCCC.E5YPO9kmyuRGyh0XouQYb4YMJKvyOeW';
	ASSERT substr(crypt('ñ', '$2x$05$CCCCCCCCCCCCCCCCCCCCC.'), 8) <> substr(crypt('ñ', '$2a$05$CCCCCCCCCCCCCCCCCCCCC.'), 8);
	-- sha256crypt, sha512crypt
	ASSERT crypt('', '$5$Szzz0yzz') = '$5$Szzz0yzz$cA.ZFZKqblRYjdsbrWtVTYa/qSwPQnt2uh0LBtyYAAD';
	ASSERT crypt('foox', '$5$Szzz0yzz') = '$5$Szzz0yzz$7hI0rUWkO2QdBkzamh.vP.MIPlbZiwSvu2smhSi6064';
	ASSERT crypt('Hello world!', '$5$saltstring') = '$5$saltstring$5B8vYYiY.CVt1RlTTf8KbXBH3hsxY/GNooZaBBGWEc5';
	ASSERT crypt('Hello world!', '$5$rounds=10000$saltstringsaltstring')
		= '$5$rounds=10000$saltstringsaltst$3xv.VbSHBb41AL9AvLeujZkZRBAwqFMz2.opqey6IcA';
	ASSERT crypt('we have a short salt string but not a short password', '$5$rounds=77777$short')
		= '$5$rounds=77777$short$JiO1O3ZpDAxGJeaDIuqCoEFysAe1mZNJRs3pw0KQRd/';
	ASSERT crypt('the minimum number is still observed', '$5$rounds=10$roundstoolow')
		= '$5$rounds=1000$roundstoolow$yfvwcWrQ8l/K0DAWyuPMDNHpIVlTQebY9l/gL972bIC';
	ASSERT crypt('', '$6$Szzz0yzz')
		= '$6$Szzz0yzz$EGj.JLAovFyAtCJx3YD1DXD1yTXoO9gv4qgLyHBsJJ1lkpnLB8ZPHekm1qXjJCOBc/8thCuHpxNN8Y5xzRYU5.';
	ASSERT crypt('Hello world!', '$6$saltstring')
		= '$6$saltstring$svn8UoSVapNtMuq1ukKS4tPQd8iKwSMHWjl/O817G3uBnIFNjnQJuesI68u4OTLiBFdcbYEdFCoEOfaS35inz1';
	ASSERT crypt('This is just a test', '$6$rounds=5000$toolongsaltstring')
		= '$6$rounds=5000$toolongsaltstrin$lQ8jolhgVRVhY4b5pZKaysCLi0QBxGoNeKQzQ3glMhwllF7oGDZxUhx1yxdYcz/e1JSbq3y6JMxxl8audkUEm0';
	-- a full hash is its own salt
	s := crypt('Hello world!', '$6$saltstring');
	ASSERT crypt('Hello world!', s) = s;

	-- errors
	t := pg_temp.err($q$SELECT crypt('password', 'a')$q$);
	ASSERT t = '22023: invalid salt', t;
	t := pg_temp.err($q$SELECT crypt('foox', '_J9..BWH')$q$);
	ASSERT t = '22023: invalid salt', t;
	t := pg_temp.err($q$SELECT crypt('password', '_........')$q$);
	ASSERT t = '39000: crypt(3) returned NULL', t;
	t := pg_temp.err($q$SELECT crypt('password', '$2$05$CCCCCCCCCCCCCCCCCCCCC.')$q$);
	ASSERT t = '39000: crypt(3) returned NULL', t;
	t := pg_temp.err($q$SELECT crypt('foox', '$2a$')$q$);
	ASSERT t = '22023: invalid salt', t;
	t := pg_temp.err($q$SELECT crypt('foox', '$2a$40$RQiOJ.3ELirrXwxIZY8q0O')$q$);
	ASSERT t = '22023: invalid salt', t;
	t := pg_temp.err($q$SELECT crypt('foox', '$2a$00$RQiOJ.3ELirrXwxIZY8q0O')$q$);
	ASSERT t = '22023: invalid salt', t;
	t := pg_temp.err($q$SELECT crypt('foox', '$2a$05$CCCCCCCCCCCCCCCCCCCC!.')$q$);
	ASSERT t = '22023: invalid salt', t;
	t := pg_temp.err($q$SELECT crypt('foox', '$5$rounds=12x$abc')$q$);
	ASSERT t = '42601: could not parse salt options', t;
	t := pg_temp.err($q$SELECT crypt('foox', '$5$ab!c')$q$);
	ASSERT t = '22023: invalid character in salt string: "!"', t;
	t := pg_temp.err($q$SELECT crypt('foox', '$5$abé')$q$);
	ASSERT t = '22023: invalid character in salt string: "é"', t;
	t := pg_temp.err($q$SELECT crypt('foox', '$6$ab$6$c')$q$);
	ASSERT t = 'XX000: bogus magic byte found in salt string', t;
	t := pg_temp.err($q$SELECT gen_salt('foo')$q$);
	ASSERT t = '22023: gen_salt: Unknown salt algorithm', t;
	t := pg_temp.err($q$SELECT gen_salt('bf', 3)$q$);
	ASSERT t = '22023: gen_salt: Incorrect number of rounds', t;
	t := pg_temp.err($q$SELECT gen_salt('xdes', 2)$q$);
	ASSERT t = '22023: gen_salt: Incorrect number of rounds', t;
	t := pg_temp.err($q$SELECT gen_salt('des', 5)$q$);
	ASSERT t = '22023: gen_salt: Incorrect number of rounds', t;
	t := pg_temp.err($q$SELECT gen_salt('sha512crypt', 1000000000)$q$);
	ASSERT t = '22023: gen_salt: Incorrect number of rounds', t;

	-- gen_salt formats, and crypt() verifies with them
	ASSERT gen_salt('des') ~ '^[./0-9A-Za-z]{2}$';
	ASSERT gen_salt('xdes') ~ '^_J9\.\.[./0-9A-Za-z]{4}$';
	ASSERT gen_salt('xdes', 1) ~ '^_/\.\.\.[./0-9A-Za-z]{4}$';
	ASSERT gen_salt('MD5') ~ '^\$1\$[./0-9A-Za-z]{8}$';
	ASSERT gen_salt('bf') ~ '^\$2a\$06\$[./0-9A-Za-z]{22}$';
	ASSERT gen_salt('bf', 4) LIKE '$2a$04$%';
	ASSERT gen_salt('sha256crypt') ~ '^\$5\$rounds=5000\$[./0-9A-Za-z]{16}$';
	ASSERT gen_salt('sha512crypt', 1000) ~ '^\$6\$rounds=1000\$[./0-9A-Za-z]{16}$';
	ASSERT gen_salt('bf') <> gen_salt('bf');
	FOREACH t IN ARRAY ARRAY['des', 'xdes', 'md5', 'bf', 'sha256crypt', 'sha512crypt'] LOOP
		r := CASE t WHEN 'bf' THEN 4 WHEN 'xdes' THEN 25 WHEN 'sha256crypt' THEN 1000 WHEN 'sha512crypt' THEN 1000 ELSE 0 END;
		s := crypt('sekrit', gen_salt(t, r));
		ASSERT crypt('sekrit', s) = s, t;
		ASSERT crypt('sekriT', s) <> s, t;
	END LOOP;
END $$;

-- built-in crypto can be disabled
SET pgcrypto.builtin_crypto_enabled = off;
DO $$
DECLARE
	t text;
BEGIN
	t := pg_temp.err($q$SELECT gen_salt('des')$q$);
	ASSERT t = 'XX000: use of built-in crypto functions is disabled', t;
	t := pg_temp.err($q$SELECT crypt('a', 'NB')$q$);
	ASSERT t = 'XX000: use of built-in crypto functions is disabled', t;
END $$;
RESET pgcrypto.builtin_crypto_enabled;
SELECT crypt('', 'NB') = 'NBPx/38Y48kHg' AS ok;
