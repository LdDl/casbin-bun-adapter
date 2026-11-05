package casbinbunadapter

const journalTablesSQL = `
	CREATE TABLE %[1]s.state (
		singleton boolean PRIMARY KEY CHECK (singleton),
		revision bigint NOT NULL CHECK (revision >= 0),
		transaction_id xid8
	);
	INSERT INTO %[1]s.state (singleton, revision) VALUES (true, 0);
	CREATE TABLE %[1]s.batches (
		revision bigint PRIMARY KEY CHECK (revision > 0),
		transaction_id xid8 NOT NULL,
		created_at timestamptz NOT NULL DEFAULT pg_catalog.clock_timestamp()
	);
	CREATE TABLE %[1]s.changes (
		id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
		revision bigint NOT NULL REFERENCES %[1]s.batches (revision),
		operation text NOT NULL,
		old_policy jsonb,
		new_policy jsonb,
		CHECK (
			(operation = 'INSERT' AND old_policy IS NULL AND new_policy IS NOT NULL) OR
			(operation = 'UPDATE' AND old_policy IS NOT NULL AND new_policy IS NOT NULL) OR
			(operation = 'DELETE' AND old_policy IS NOT NULL AND new_policy IS NULL) OR
			(operation = 'TRUNCATE' AND old_policy IS NULL AND new_policy IS NULL)
		)
	);
	CREATE INDEX changes_revision_id_idx ON %[1]s.changes (revision, id);
`

const journalLockSQL = `
BEGIN
	IF TG_RELID <> %[1]d::oid OR TG_LEVEL <> 'STATEMENT' OR TG_WHEN <> 'BEFORE' THEN
		RAISE EXCEPTION 'Invalid policy journal trigger target';
	END IF;
	PERFORM revision FROM %[2]s.state WHERE singleton FOR UPDATE;
	IF NOT FOUND THEN
		RAISE EXCEPTION 'Policy journal state is missing';
	END IF;
	RETURN NULL;
END;
`

const journalRecordSQL = `
DECLARE
	previous_policy jsonb;
	next_policy jsonb;
	current_revision bigint;
	previous_transaction xid8;
	current_transaction xid8;
BEGIN
	IF TG_RELID <> %[1]d::oid OR TG_WHEN <> 'AFTER' OR
		(TG_OP = 'TRUNCATE' AND TG_LEVEL <> 'STATEMENT') OR
		(TG_OP <> 'TRUNCATE' AND TG_LEVEL <> 'ROW') THEN
		RAISE EXCEPTION 'Invalid policy journal trigger target';
	END IF;
	IF TG_OP IN ('UPDATE', 'DELETE') THEN
		previous_policy := pg_catalog.jsonb_build_object(%[3]s);
	END IF;
	IF TG_OP IN ('INSERT', 'UPDATE') THEN
		next_policy := pg_catalog.jsonb_build_object(%[4]s);
	END IF;
	IF TG_OP = 'UPDATE' AND previous_policy IS NOT DISTINCT FROM next_policy THEN
		RETURN NULL;
	END IF;
	SELECT revision, transaction_id INTO STRICT current_revision, previous_transaction
		FROM %[2]s.state WHERE singleton FOR UPDATE;
	current_transaction := pg_catalog.pg_current_xact_id();
	IF previous_transaction IS DISTINCT FROM current_transaction THEN
		UPDATE %[2]s.state
			SET revision = revision + 1, transaction_id = current_transaction
			WHERE singleton RETURNING revision INTO current_revision;
		INSERT INTO %[2]s.batches (revision, transaction_id)
			VALUES (current_revision, current_transaction);
	END IF;
	INSERT INTO %[2]s.changes (revision, operation, old_policy, new_policy)
		VALUES (current_revision, TG_OP, previous_policy, next_policy);
	RETURN NULL;
END;
`

const journalPermissionsSQL = `
	REVOKE ALL ON SCHEMA %[1]s FROM PUBLIC;
	REVOKE ALL ON ALL TABLES IN SCHEMA %[1]s FROM PUBLIC;
	REVOKE ALL ON ALL SEQUENCES IN SCHEMA %[1]s FROM PUBLIC;
	REVOKE ALL ON ALL FUNCTIONS IN SCHEMA %[1]s FROM PUBLIC;
`

const journalGrantsSQL = `
	SELECT EXISTS (
		SELECT FROM pg_catalog.pg_namespace n,
			LATERAL pg_catalog.aclexplode(n.nspacl) a
		WHERE n.nspname = ? AND a.grantee <> n.nspowner
		UNION ALL
		SELECT FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace,
			LATERAL pg_catalog.aclexplode(c.relacl) a
		WHERE n.nspname = ? AND a.grantee <> c.relowner
		UNION ALL
		SELECT FROM pg_catalog.pg_proc p
		JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace,
			LATERAL pg_catalog.aclexplode(p.proacl) a
		WHERE n.nspname = ? AND a.grantee <> p.proowner
	)
`
