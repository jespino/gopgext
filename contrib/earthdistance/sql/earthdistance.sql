/*
 * earthdistance 1.2: upstream's earthdistance--1.1.sql with
 * earthdistance--1.1--1.2.sql applied. Upstream refers to the cube
 * extension's objects as @extschema:cube@, which PostgreSQL only
 * substitutes from version 16 on; the objects that use cube are created
 * here with its schema found in pg_extension instead, so that the same
 * script works from PostgreSQL 14.
 */

-- earth() returns the radius of the earth in meters. This is the only
-- place you need to change things for the cube base distance functions
-- in order to use different units (or a better value for the Earth's radius).

CREATE FUNCTION earth() RETURNS float8
LANGUAGE SQL IMMUTABLE PARALLEL SAFE
RETURN '6378168'::float8;

-- Astronomers may want to change the earth function so that distances will be
-- returned in degrees. To do this comment out the above definition and
-- uncomment the one below. Note that doing this will break the regression
-- tests.
--
-- CREATE FUNCTION earth() RETURNS float8
-- LANGUAGE SQL IMMUTABLE
-- AS 'SELECT 180/pi()';

CREATE FUNCTION sec_to_gc(float8)
RETURNS float8
LANGUAGE SQL
IMMUTABLE STRICT
PARALLEL SAFE
RETURN CASE
  WHEN $1 < '0'::float8 THEN '0'::float8
  WHEN $1 / ('2'::float8 * earth()) > '1'::float8 THEN pi() * earth()
  ELSE '2'::float8 * earth() * asin($1 / ('2'::float8 * earth()))
END;

CREATE FUNCTION gc_to_sec(float8)
RETURNS float8
LANGUAGE SQL
IMMUTABLE STRICT
PARALLEL SAFE
RETURN CASE
  WHEN $1 < '0'::float8 THEN '0'::float8
  WHEN $1 / earth() > pi() THEN '2'::float8 * earth()
  ELSE '2'::float8 * earth() * sin($1 / ('2'::float8 * earth()))
END;

DO LANGUAGE plpgsql $do$
DECLARE
  -- @extschema:cube@
  c pg_catalog.text := (SELECT pg_catalog.quote_ident(n.nspname)
    FROM pg_catalog.pg_extension e JOIN pg_catalog.pg_namespace n ON n.oid = e.extnamespace
    WHERE e.extname = 'cube');
BEGIN

-- Define domain for locations on the surface of the earth using a cube
-- datatype with constraints. cube provides 3D indexing.
-- The cube is restricted to be a point, no more than 3 dimensions
-- (for less than 3 dimensions 0 is assumed for the missing coordinates)
-- and that the point must be very near the surface of the sphere
-- centered about the origin with the radius of the earth.

EXECUTE pg_catalog.format($sql$
CREATE DOMAIN earth AS %1$s.cube
  CONSTRAINT not_point CHECK(%1$s.cube_is_point(VALUE))
  CONSTRAINT not_3d CHECK(%1$s.cube_dim(VALUE) <= 3)
  CONSTRAINT on_surface CHECK(abs(%1$s.cube_distance(VALUE, '(0)'::%1$s.cube) /
  earth() - '1'::float8) < '10e-7'::float8)
$sql$, c);

EXECUTE pg_catalog.format($sql$
CREATE FUNCTION ll_to_earth(float8, float8)
RETURNS earth
LANGUAGE SQL
IMMUTABLE STRICT
PARALLEL SAFE
RETURN %1$s.cube(%1$s.cube(%1$s.cube(
    earth() * cos(radians($1)) * cos(radians($2))),
    earth() * cos(radians($1)) * sin(radians($2))),
    earth() * sin(radians($1)))::earth
$sql$, c);

EXECUTE pg_catalog.format($sql$
CREATE FUNCTION latitude(earth)
RETURNS float8
LANGUAGE SQL
IMMUTABLE STRICT
PARALLEL SAFE
RETURN CASE
  WHEN %1$s.cube_ll_coord($1, 3) / earth() < '-1'::float8 THEN '-90'::float8
  WHEN %1$s.cube_ll_coord($1, 3) / earth() > '1'::float8 THEN '90'::float8
  ELSE degrees(asin(%1$s.cube_ll_coord($1, 3) / earth()))
END
$sql$, c);

EXECUTE pg_catalog.format($sql$
CREATE FUNCTION longitude(earth)
RETURNS float8
LANGUAGE SQL
IMMUTABLE STRICT
PARALLEL SAFE
RETURN degrees(atan2(%1$s.cube_ll_coord($1, 2),
                     %1$s.cube_ll_coord($1, 1)))
$sql$, c);

EXECUTE pg_catalog.format($sql$
CREATE FUNCTION earth_distance(earth, earth)
RETURNS float8
LANGUAGE SQL
IMMUTABLE STRICT
PARALLEL SAFE
RETURN sec_to_gc(%1$s.cube_distance($1, $2))
$sql$, c);

EXECUTE pg_catalog.format($sql$
CREATE FUNCTION earth_box(earth, float8)
RETURNS %1$s.cube
LANGUAGE SQL
IMMUTABLE STRICT
PARALLEL SAFE
RETURN %1$s.cube_enlarge($1, gc_to_sec($2), 3)
$sql$, c);

END
$do$;

--------------- geo_distance

CREATE FUNCTION geo_distance (point, point)
RETURNS float8
LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE AS 'MODULE_PATHNAME';

--------------- geo_distance as operator <@>

CREATE OPERATOR <@> (
  LEFTARG = point,
  RIGHTARG = point,
  PROCEDURE = geo_distance,
  COMMUTATOR = <@>
);
