package casbinbunadapter

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/casbin/casbin/v2/model"
	"github.com/casbin/casbin/v2/rbac"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

type journalBlockingRoleManager struct {
	rbac.RoleManager
	applied chan struct{}
	resume  chan struct{}
}

func (manager *journalBlockingRoleManager) AddLink(first, second string, domain ...string) error {
	err := manager.RoleManager.AddLink(first, second, domain...)
	if err != nil {
		return err
	}
	close(manager.applied)
	<-manager.resume
	return nil
}

type journalFailingRoleManager struct {
	rbac.RoleManager
}

func (manager *journalFailingRoleManager) AddLink(first, second string, domain ...string) error {
	err := manager.RoleManager.AddLink(first, second, domain...)
	if err != nil {
		return err
	}
	return errors.New("role manager failed after changing a link")
}

func TestPostgresJournalIncrementalReusesViewsAndNamedRoles(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	reader := newJournalTestReader(t, adapter, schema, JournalReaderOptions{})
	first, second := reader.view, reader.spare
	canonical := reader.model["p"]["p"]
	firstRoles := first.enforcer.GetNamedRoleManager("g")
	storageTestExec(t, adapter, `INSERT INTO ?.policies (ptype,v0,v1,v2,v3) VALUES
		('p','readers','data','read','allow'),
		('g','alice','readers',NULL,NULL),
		('p2','editors','data','write','allow'),
		('g2','bob','editors',NULL,NULL)`, bun.Name(adapter.matcher.SchemaName))
	requireJournalStrictDecision(t, reader, true)
	require.Same(t, second, reader.view)
	require.Same(t, canonical, reader.model["p"]["p"])
	storageTestExec(t, adapter, `BEGIN;
		UPDATE ?.policies SET v1='writers' WHERE ptype='g';
		UPDATE ?.policies SET v0='writers',v3='deny' WHERE ptype='p';
		DELETE FROM ?.policies WHERE ptype='g2';
		COMMIT`, bun.Name(adapter.matcher.SchemaName), bun.Name(adapter.matcher.SchemaName), bun.Name(adapter.matcher.SchemaName))
	requireJournalStrictDecision(t, reader, false)
	require.Same(t, first, reader.view)
	require.Same(t, canonical, reader.model["p"]["p"])
	currentRoles := reader.view.enforcer.GetNamedRoleManager("g")
	require.Same(t, firstRoles, currentRoles)
	linked, err := currentRoles.HasLink("alice", "readers")
	require.NoError(t, err)
	require.False(t, linked)
	linked, err = currentRoles.HasLink("alice", "writers")
	require.NoError(t, err)
	require.True(t, linked)
	secondary := reader.view.enforcer.GetNamedRoleManager("g2")
	linked, err = secondary.HasLink("bob", "editors")
	require.NoError(t, err)
	require.False(t, linked)
	found, err := reader.view.enforcer.HasNamedPolicy("p2", "editors", "data", "write", "allow")
	require.NoError(t, err)
	require.True(t, found)

	storageTestExec(t, adapter, `UPDATE ?.policies SET v3='allow' WHERE ptype='p'`, bun.Name(adapter.matcher.SchemaName))
	requireJournalStrictDecision(t, reader, true)
	require.Same(t, second, reader.view)
}

func TestPostgresJournalIncrementalPublishesOnlyCompleteChanges(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	storageTestExec(t, adapter, `INSERT INTO ?.policies (ptype,v0,v1,v2,v3)
		VALUES ('p','readers','data','read','allow')`, bun.Name(adapter.matcher.SchemaName))
	reader := newJournalTestReader(t, adapter, schema, JournalReaderOptions{})
	roles := reader.spare.enforcer.GetNamedRoleManager("g")
	blocker := &journalBlockingRoleManager{RoleManager: roles, applied: make(chan struct{}), resume: make(chan struct{})}
	reader.spare.enforcer.SetNamedRoleManager("g", blocker)
	var resume sync.Once
	t.Cleanup(func() { resume.Do(func() { close(blocker.resume) }) })
	before := reader.Status().Revision
	storageTestExec(t, adapter, `BEGIN;
		INSERT INTO ?.policies (ptype,v0,v1) VALUES ('g','alice','readers');
		INSERT INTO ?.policies (ptype,v0,v1,v2,v3) VALUES ('p','readers','data','read','deny');
		COMMIT`, bun.Name(adapter.matcher.SchemaName), bun.Name(adapter.matcher.SchemaName))
	done := make(chan error, 1)
	go func() { done <- reader.Sync(context.Background()) }()
	select {
	case <-blocker.applied:
	case <-time.After(2 * time.Second):
		t.Fatal("incremental update did not reach the role manager")
	}
	for index := 0; index < 100; index++ {
		requireJournalCachedDecision(t, reader, false)
	}
	require.Equal(t, before, reader.Status().Revision)
	resume.Do(func() { close(blocker.resume) })
	require.NoError(t, <-done)
	require.Equal(t, before+1, reader.Status().Revision)
	requireJournalStrictDecision(t, reader, false)
}

func TestPostgresJournalIncrementalDiscardsFailedPrivateCopy(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	reader := newJournalTestReader(t, adapter, schema, JournalReaderOptions{})
	original := reader.view
	roles := reader.spare.enforcer.GetNamedRoleManager("g")
	reader.spare.enforcer.SetNamedRoleManager("g", &journalFailingRoleManager{RoleManager: roles})
	storageTestExec(t, adapter, `BEGIN;
		INSERT INTO ?.policies (ptype,v0,v1,v2,v3) VALUES ('p','readers','data','read','allow');
		INSERT INTO ?.policies (ptype,v0,v1) VALUES ('g','alice','readers');
		COMMIT`, bun.Name(adapter.matcher.SchemaName), bun.Name(adapter.matcher.SchemaName))
	err := reader.Sync(context.Background())
	require.ErrorContains(t, err, "role manager failed")
	require.Same(t, original, reader.view)
	require.Nil(t, reader.spare)
	require.Zero(t, reader.Status().Revision)
	require.Empty(t, reader.model["p"]["p"].Policy)
	require.Empty(t, original.enforcer.GetModel()["p"]["p"].Policy)
	allowed, err := reader.EnforceCached("alice", "data", "read")
	require.False(t, allowed)
	require.ErrorIs(t, err, ErrJournalNotReady)
	requireJournalStrictDecision(t, reader, true)
	require.EqualValues(t, 1, reader.Status().Revision)
	require.Same(t, original, reader.spare)
}

func TestPostgresJournalIncrementalDiscardsCanceledPrivateCopy(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	reader := newJournalTestReader(t, adapter, schema, JournalReaderOptions{})
	original := reader.view
	roles := reader.spare.enforcer.GetNamedRoleManager("g")
	blocker := &journalBlockingRoleManager{RoleManager: roles, applied: make(chan struct{}), resume: make(chan struct{})}
	reader.spare.enforcer.SetNamedRoleManager("g", blocker)
	var resume sync.Once
	t.Cleanup(func() { resume.Do(func() { close(blocker.resume) }) })
	storageTestExec(t, adapter, `BEGIN;
		INSERT INTO ?.policies (ptype,v0,v1) VALUES ('g','alice','readers');
		INSERT INTO ?.policies (ptype,v0,v1,v2,v3) VALUES ('p','readers','data','read','allow');
		COMMIT`, bun.Name(adapter.matcher.SchemaName), bun.Name(adapter.matcher.SchemaName))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- reader.Sync(ctx) }()
	select {
	case <-blocker.applied:
	case <-time.After(2 * time.Second):
		t.Fatal("incremental update did not reach the role manager")
	}
	cancel()
	resume.Do(func() { close(blocker.resume) })
	require.ErrorIs(t, <-done, context.Canceled)
	require.Same(t, original, reader.view)
	require.Nil(t, reader.spare)
	require.Zero(t, reader.Status().Revision)
	require.Empty(t, reader.model["g"]["g"].Policy)
	requireJournalStrictDecision(t, reader, true)
}

func TestPostgresJournalIncrementalConcurrentChecks(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	storageTestExec(t, adapter, `INSERT INTO ?.policies (ptype,v0,v1,v2,v3) VALUES
		('p','alice','data','read','allow'), ('p','alice','data','read','deny')`, bun.Name(adapter.matcher.SchemaName))
	reader := newJournalTestReader(t, adapter, schema, JournalReaderOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	t.Cleanup(func() { cancel(); workers.Wait() })
	failures := make(chan error, 8)
	for index := 0; index < 8; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for ctx.Err() == nil {
				allowed, err := reader.EnforceCached("alice", "data", "read")
				if err != nil {
					failures <- err
					return
				}
				if allowed {
					failures <- errors.New("a check observed an intermediate grant")
					return
				}
			}
		}()
	}
	for index := 0; index < 30; index++ {
		storageTestExec(t, adapter, `BEGIN;
			DELETE FROM ?.policies WHERE ptype='p' AND v3='deny';
			INSERT INTO ?.policies (ptype,v0,v1,v2,v3) VALUES ('p','alice','data','read','deny');
			COMMIT`, bun.Name(adapter.matcher.SchemaName), bun.Name(adapter.matcher.SchemaName))
		err := reader.Sync(context.Background())
		require.NoError(t, err)
	}
	cancel()
	workers.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	require.Zero(t, reader.view.users.Load())
	require.Zero(t, reader.spare.users.Load())
}

func TestPostgresJournalIncrementalDoesNotReusePinnedView(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	reader := newJournalTestReader(t, adapter, schema, JournalReaderOptions{})
	pinned := reader.view
	pinned.users.Add(1)
	defer pinned.users.Add(-1)
	err := adapter.AddPolicy("p", "p", []string{"alice", "data", "read", "allow"})
	require.NoError(t, err)
	requireJournalStrictDecision(t, reader, true)
	require.Same(t, pinned, reader.spare)
	err = adapter.AddPolicy("p", "p", []string{"alice", "data", "read", "deny"})
	require.NoError(t, err)
	requireJournalStrictDecision(t, reader, false)
	require.NotSame(t, pinned, reader.view)
	require.Empty(t, pinned.enforcer.GetModel()["p"]["p"].Policy)
	allowed, err := pinned.enforcer.Enforce("alice", "data", "read")
	require.NoError(t, err)
	require.False(t, allowed)
}

func TestPostgresJournalIncrementalKeepsSharedRoleLink(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	storageTestExec(t, adapter, `INSERT INTO ?.policies (ptype,v0,v1,v2,v3) VALUES
		('p','readers','data','read','allow'),
		('g','alice','readers',NULL,NULL),
		('g','alice','readers','metadata',NULL)`, bun.Name(adapter.matcher.SchemaName))
	reader := newJournalTestReader(t, adapter, schema, JournalReaderOptions{})
	first := reader.view
	err := adapter.RemovePolicy("g", "g", []string{"alice", "readers"})
	require.NoError(t, err)
	requireJournalStrictDecision(t, reader, true)
	err = adapter.RemovePolicy("g", "g", []string{"alice", "readers", "metadata"})
	require.NoError(t, err)
	requireJournalStrictDecision(t, reader, false)
	require.Same(t, first, reader.view)
}

func TestPostgresJournalIncrementalDomains(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	policyModel, err := model.NewModelFromString(`[request_definition]
r = sub, dom, obj, act
[policy_definition]
p = sub, dom, obj, act, eft
[role_definition]
g = _, _, _
[policy_effect]
e = some(where (p.eft == allow)) && !some(where (p.eft == deny))
[matchers]
m = g(r.sub, p.sub, r.dom) && r.dom == p.dom && r.obj == p.obj && r.act == p.act
`)
	require.NoError(t, err)
	reader, err := adapter.NewJournalReader(context.Background(), schema, policyModel, JournalReaderOptions{})
	require.NoError(t, err)
	first := reader.view
	storageTestExec(t, adapter, `INSERT INTO ?.policies (ptype,v0,v1,v2,v3,v4) VALUES
		('p','readers','one','data','read','allow'),
		('p','readers','two','data','read','allow'),
		('g','alice','readers','one',NULL,NULL),
		('g','alice','readers','two',NULL,NULL)`, bun.Name(adapter.matcher.SchemaName))
	allowed, err := reader.Enforce("alice", "one", "data", "read")
	require.NoError(t, err)
	require.True(t, allowed)
	err = adapter.RemovePolicy("g", "g", []string{"alice", "readers", "one"})
	require.NoError(t, err)
	allowed, err = reader.Enforce("alice", "one", "data", "read")
	require.NoError(t, err)
	require.False(t, allowed)
	allowed, err = reader.Enforce("alice", "two", "data", "read")
	require.NoError(t, err)
	require.True(t, allowed)
	require.Same(t, first, reader.view)
}

func TestPostgresJournalIncrementalPriorityAndFallback(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	policyModel, err := model.NewModelFromString(`[request_definition]
r = sub, obj, act
[policy_definition]
p = priority, sub, obj, act, eft
[policy_effect]
e = priority(p.eft) || deny
[matchers]
m = r.sub == p.sub && r.obj == p.obj && r.act == p.act
`)
	require.NoError(t, err)
	reader, err := adapter.NewJournalReader(context.Background(), schema, policyModel, JournalReaderOptions{})
	require.NoError(t, err)
	first, second := reader.view, reader.spare
	storageTestExec(t, adapter, `INSERT INTO ?.policies (ptype,v0,v1,v2,v3,v4) VALUES
		('p','20','alice','data','read','allow'), ('p','30','alice','data','read','deny')`, bun.Name(adapter.matcher.SchemaName))
	requireJournalStrictDecision(t, reader, true)
	require.Same(t, second, reader.view)
	storageTestExec(t, adapter, `UPDATE ?.policies SET v0='10' WHERE v4='deny'`, bun.Name(adapter.matcher.SchemaName))
	requireJournalStrictDecision(t, reader, false)
	require.Same(t, first, reader.view)
	storageTestExec(t, adapter, `DELETE FROM ?.policies WHERE v4='deny'`, bun.Name(adapter.matcher.SchemaName))
	requireJournalStrictDecision(t, reader, true)
	require.Same(t, second, reader.view)
	storageTestExec(t, adapter, `UPDATE ?.policies SET v0='other'`, bun.Name(adapter.matcher.SchemaName))
	requireJournalStrictDecision(t, reader, true)
	require.False(t, reader.view.incremental)
	require.Nil(t, reader.spare)
	storageTestExec(t, adapter, `UPDATE ?.policies SET v0='5'`, bun.Name(adapter.matcher.SchemaName))
	requireJournalStrictDecision(t, reader, true)
	require.True(t, reader.view.incremental)
	require.NotNil(t, reader.spare)
}

func TestPostgresJournalIncrementalResetsBothViews(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	storageTestExec(t, adapter, `INSERT INTO ?.policies (ptype,v0,v1,v2,v3)
		VALUES ('p','alice','data','read','allow')`, bun.Name(adapter.matcher.SchemaName))
	reader := newJournalTestReader(t, adapter, schema, JournalReaderOptions{})
	first, second := reader.view, reader.spare
	storageTestExec(t, adapter, `BEGIN;
		TRUNCATE ?.policies;
		INSERT INTO ?.policies (ptype,v0,v1,v2,v3) VALUES ('p','readers','data','read','allow');
		COMMIT`, bun.Name(adapter.matcher.SchemaName), bun.Name(adapter.matcher.SchemaName))
	requireJournalStrictDecision(t, reader, false)
	require.NotSame(t, first, reader.view)
	require.NotSame(t, second, reader.view)
	require.NotSame(t, first, reader.spare)
	require.NotSame(t, second, reader.spare)
	err := adapter.AddPolicy("g", "g", []string{"alice", "readers"})
	require.NoError(t, err)
	requireJournalStrictDecision(t, reader, true)
}
