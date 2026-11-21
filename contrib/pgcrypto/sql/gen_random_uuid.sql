-- gen_random_uuid() is PostgreSQL's own (upstream's C function calls it).
CREATE FUNCTION gen_random_uuid()
RETURNS uuid
AS 'gen_random_uuid'
LANGUAGE internal VOLATILE PARALLEL SAFE;
