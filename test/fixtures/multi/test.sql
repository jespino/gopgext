-- Several Go extensions in one backend: each has its own Go runtime and heap,
-- and their symbols don't clash.
CREATE EXTENSION hello;
CREATE EXTENSION types;
CREATE EXTENSION textutil;
DO $$
BEGIN
	ASSERT hello(reverse('dlrow')) = 'Hello, world!';
	ASSERT slugify(hello('X Y')) = 'hello-x-y';
	ASSERT (SELECT sum(allocate(8)) FROM generate_series(1, 5)) > 0;
	ASSERT levenshtein(hello('a'), hello('b')) = 1;
END $$;
