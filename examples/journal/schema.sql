CREATE SCHEMA journal_example;

CREATE TABLE journal_example.policies (
	id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	ptype text NOT NULL,
	v0 text,
	v1 text,
	v2 text,
	v3 text,
	v4 text,
	v5 text,
	UNIQUE NULLS NOT DISTINCT (ptype, v0, v1, v2, v3, v4, v5)
);

INSERT INTO journal_example.policies (ptype, v0, v1, v2, v3)
VALUES
	('p', 'readers', 'data1', 'read', 'allow'),
	('g', 'alice', 'readers', NULL, NULL);
