// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package scylla_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/gitstore-dev/gitstore/api/internal/graph/resolver"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/gitstore-dev/gitstore/observability"
	"github.com/gocql/gocql"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/scylladb/gocqlx/v3/table"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	scyllaAddr              string
	scyllaKeyspace          string
	scyllaReplicationFactor = 1
)

func TestMain(m *testing.M) {
	scyllaAddr = os.Getenv("GITSTORE_TEST_SCYLLA_ADDR")
	if scyllaAddr == "" {
		scyllaAddr = "127.0.0.1:9042"
	}
	if raw := os.Getenv("GITSTORE_TEST_SCYLLA_REPLICATION_FACTOR"); raw != "" {
		factor, err := strconv.Atoi(raw)
		if err != nil || factor < 1 {
			panic("GITSTORE_TEST_SCYLLA_REPLICATION_FACTOR must be a positive integer")
		}
		scyllaReplicationFactor = factor
	}
	scyllaKeyspace = fmt.Sprintf("gitstore_scylla_test_%d_%s", os.Getpid(), strings.ReplaceAll(uuid.NewString(), "-", "")[:8])

	fmt.Fprintf(os.Stderr, "Scylla regression keyspace %s requested replication factor %d\n", scyllaKeyspace, scyllaReplicationFactor)
	provisionErr := provisionKeyspace(scyllaAddr, scyllaKeyspace)
	if provisionErr != nil {
		fmt.Fprintf(os.Stderr, "Scylla regression provisioning failed: %v\n", provisionErr)
	}
	clockStarted := time.Now()
	clockStop, clockDone := make(chan struct{}), make(chan struct{})
	var invalidClock atomic.Bool
	go func() {
		defer close(clockDone)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-clockStop:
				return
			case <-ticker.C:
				now := time.Now()
				drift := now.Round(0).Sub(clockStarted.Round(0)) - now.Sub(clockStarted)
				if drift > time.Second || drift < -time.Second {
					if invalidClock.CompareAndSwap(false, true) {
						fmt.Fprintf(os.Stderr, "Scylla regression clock invalid: wall/monotonic drift %s; TTL, lease and CDC evidence cannot pass\n", drift)
					}
				}
			}
		}
	}()
	code := 1
	if provisionErr == nil {
		code = m.Run()
	}
	close(clockStop)
	<-clockDone
	if invalidClock.Load() {
		code = 1
	}
	if err := dropKeyspace(scyllaAddr, scyllaKeyspace); err != nil {
		fmt.Fprintf(os.Stderr, "Scylla regression cleanup failed: %v\n", err)
		code = 1
	}

	os.Exit(code)
}

// contactPointTranslator returns an AddressTranslator that redirects all peer
// addresses to the original contact point. This is needed when Scylla runs in
// a Docker container — its rpc_address is an internal Docker IP, but the host
// connects via a forwarded port on the contact-point address.
func contactPointTranslator(contactHost string, contactPort int) gocql.AddressTranslator {
	contactIP := net.ParseIP(contactHost)
	return gocql.AddressTranslatorFunc(func(_ net.IP, port int) (net.IP, int) {
		if contactPort > 0 {
			port = contactPort
		}
		return contactIP, port
	})
}

// provisionKeyspace creates the keyspace using a temporary no-keyspace session.
// This mirrors what the compose scylla-init service does for local/CI stacks.
// Retries for up to 60 s because ScyllaDB logs "Starting listening for CQL clients"
// slightly before it actually accepts connections.
func provisionKeyspace(addr, keyspace string) error {
	host, portStr, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		host = addr
		portStr = "9042"
	}
	port, _ := strconv.Atoi(portStr)
	cluster := gocql.NewCluster(host)
	if port > 0 {
		cluster.Port = port
	}
	cluster.Consistency = gocql.Quorum
	cluster.ConnectTimeout = 5 * time.Second
	cluster.Timeout = time.Minute
	cluster.DisableShardAwarePort = true
	cluster.IgnorePeerAddr = true
	cluster.AddressTranslator = contactPointTranslator(host, port)

	var session *gocql.Session
	var err error
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		session, err = cluster.CreateSession()
		if err == nil {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		return fmt.Errorf("provisionKeyspace: open session: %w", err)
	}
	defer session.Close()

	stmt := fmt.Sprintf(
		`CREATE KEYSPACE IF NOT EXISTS %s `+
			`WITH replication = {'class': 'NetworkTopologyStrategy', 'replication_factor': '%d'} `+
			`AND durable_writes = true`,
		keyspace, scyllaReplicationFactor,
	)
	if err := session.Query(stmt).Exec(); err != nil {
		return fmt.Errorf("provisionKeyspace: create keyspace: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := session.AwaitSchemaAgreement(ctx); err != nil {
		return fmt.Errorf("provisionKeyspace: await schema agreement: %w", err)
	}
	return nil
}

func dropKeyspace(addr, keyspace string) error {
	session, err := openRootSessionWithTimeout(addr, time.Minute)
	if err != nil {
		return fmt.Errorf("dropKeyspace: open session: %w", err)
	}
	defer session.Close()
	if err := session.Query(fmt.Sprintf(`DROP KEYSPACE IF EXISTS %s`, keyspace)).Exec(); err != nil {
		return fmt.Errorf("dropKeyspace: drop keyspace: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := session.AwaitSchemaAgreement(ctx); err != nil {
		return fmt.Errorf("dropKeyspace: await schema agreement: %w", err)
	}
	return nil
}

func openRootSession(addr string) (*gocql.Session, error) {
	return openRootSessionWithTimeout(addr, 5*time.Second)
}

func openRootSessionWithTimeout(addr string, timeout time.Duration) (*gocql.Session, error) {
	host, portStr, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		host = addr
		portStr = "9042"
	}
	port, _ := strconv.Atoi(portStr)
	cluster := gocql.NewCluster(host)
	if port > 0 {
		cluster.Port = port
	}
	cluster.Consistency = gocql.Quorum
	cluster.ConnectTimeout = 5 * time.Second
	cluster.Timeout = timeout
	cluster.DisableShardAwarePort = true
	cluster.IgnorePeerAddr = true
	cluster.AddressTranslator = contactPointTranslator(host, port)
	return cluster.CreateSession()
}

func newTestStore(t *testing.T) datastore.Datastore {
	return newTestStoreWithWatchBucket(t, 0)
}

func newTestStoreWithWatchBucket(t *testing.T, watchBucketSize int) datastore.Datastore {
	t.Helper()
	store, err := scylla.New(testScyllaConfig(t), zap.NewNop(), watchBucketSize)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testScyllaConfig(t *testing.T) config.ScyllaConfig {
	t.Helper()
	host, portStr, splitErr := net.SplitHostPort(scyllaAddr)
	if splitErr != nil {
		host = scyllaAddr
		portStr = "9042"
	}
	port, _ := strconv.Atoi(portStr)
	return config.ScyllaConfig{
		AutoMigrate:           true,
		Hosts:                 []string{scyllaAddr},
		Keyspace:              scyllaKeyspace,
		DisableShardAwarePort: true,
		IgnorePeerAddr:        true,
		AddressTranslator:     contactPointTranslator(host, port),
	}
}

func newTestStores(t *testing.T) (datastore.Datastore, datastore.Datastore) {
	t.Helper()
	return newTestStore(t), newTestStore(t)
}

func TestServiceAccountClusterConcurrentIdentityAndRepair(t *testing.T) {
	first, second := newTestStores(t)
	raw := newRawSession(t)
	ctx := t.Context()
	for _, sameUID := range []bool{false, true} {
		name, uid := "sa-"+newID(), newID()
		var wg sync.WaitGroup
		results := make(chan error, 12)
		for i := range 12 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				n, u := name, newID()
				if sameUID {
					n, u = "sa-"+newID(), uid
				}
				store := first
				if i%2 != 0 {
					store = second
				}
				results <- store.CreateServiceAccount(ctx, &datastore.ServiceAccount{UID: u, Name: n, ResourceVersion: "1", CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond)})
			}()
		}
		wg.Wait()
		close(results)
		winners := 0
		for err := range results {
			if err == nil {
				winners++
			} else {
				require.ErrorIs(t, err, datastore.ErrAlreadyExists)
			}
		}
		require.Equal(t, 1, winners, "exactly one API may reserve a global name or UID")
	}
	name, uid := "interrupted-"+newID(), gocql.TimeUUID()
	created := time.Now().UTC().Truncate(time.Millisecond)
	require.NoError(t, raw.Query("INSERT INTO service_accounts_by_cluster_name (name,uid,creation_timestamp,resource_version,public_keys,enrollment_complete) VALUES (?,?,?,?,?,?)", name, uid, created, "1", "[]", false).Exec())
	_, err := first.GetServiceAccountBySubject(ctx, name)
	require.ErrorIs(t, err, datastore.ErrNotFound, "incomplete create must not authenticate")
	repair, err := scylla.OpenProjectionRepairService(testScyllaConfig(t))
	require.NoError(t, err)
	defer repair.Close()
	plan, err := repair.Audit(ctx)
	require.NoError(t, err)
	require.Len(t, plan.ServiceAccountEnrollments, 1)
	_, err = repair.Apply(ctx, plan)
	require.NoError(t, err)
	account, err := second.GetServiceAccountByUID(ctx, uid.String())
	require.NoError(t, err)
	require.Equal(t, name, account.Name)
	// An interrupted delete leaves projections behind, but neither can authenticate.
	require.NoError(t, raw.Query("DELETE FROM service_accounts_by_cluster_name WHERE name=?", name).Exec())
	_, err = second.GetServiceAccountByUID(ctx, uid.String())
	require.ErrorIs(t, err, datastore.ErrNotFound)
	replacement := &datastore.ServiceAccount{UID: newID(), Name: name, ResourceVersion: "1", CreationTimestamp: created.Add(time.Second)}
	require.NoError(t, first.CreateServiceAccount(ctx, replacement))
	plan, err = repair.Audit(ctx)
	require.NoError(t, err)
	_, err = repair.Apply(ctx, plan)
	require.NoError(t, err)
	_, err = second.GetServiceAccountByUID(ctx, uid.String())
	require.ErrorIs(t, err, datastore.ErrNotFound)
	got, err := second.GetServiceAccountByUID(ctx, replacement.UID)
	require.NoError(t, err)
	require.Equal(t, name, got.Name)
	// Stale listing rows must not turn a nonempty collection into an empty page.
	listed := &datastore.ServiceAccount{UID: newID(), Name: "listed-" + newID(), ResourceVersion: "1", CreationTimestamp: created.Add(2 * time.Hour)}
	require.NoError(t, first.CreateServiceAccount(ctx, listed))
	for i := range 4 {
		ts := listed.CreationTimestamp.Add(time.Duration(i+1) * time.Second)
		require.NoError(t, raw.Query("INSERT INTO service_accounts_by_cluster_bucket (bucket,creation_timestamp,uid,name) VALUES (?,?,?,?)", ts.Format("2006-01"), ts, gocql.TimeUUID(), "dangling").Exec())
	}
	page, err := second.ListServiceAccounts(ctx, datastore.PageParams{First: 1})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, listed.UID, page.Items[0].UID)
}

func TestServiceAccountDeleteReportsProjectionCleanupFailure(t *testing.T) {
	for _, test := range []struct {
		name                          string
		failListing, cancelUIDRelease bool
	}{
		{"listing", true, false},
		{"cancel after listing reply", false, true},
		{"listing fault and cancel after reply", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			deletionCtx, cancelDeletion := context.WithCancel(ctx)
			defer cancelDeletion()
			type deletionKey struct{}
			deletionCtx = context.WithValue(deletionCtx, deletionKey{}, true)
			listingResult := make(chan error, 1)
			var observed sync.Once
			cfg := testScyllaConfig(t)
			cfg.QueryObserver = namespaceReservationObserver(func(queryCtx context.Context, query gocql.ObservedQuery) {
				if queryCtx.Value(deletionKey{}) == true &&
					strings.HasPrefix(query.Statement, "DELETE FROM service_accounts_by_cluster_bucket ") {
					observed.Do(func() {
						listingResult <- query.Err
						if test.cancelUIDRelease {
							cancelDeletion()
						}
					})
				}
			})
			first, err := scylla.New(cfg, zap.NewNop())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, first.Close()) })
			second := newTestStore(t)
			raw := newRawSession(t)
			require.True(t, strings.HasPrefix(scyllaKeyspace, "gitstore_scylla_test_"), "faults must only alter the generated test keyspace")
			account := &datastore.ServiceAccount{
				UID: newID(), Name: "cleanup-" + newID(), ResourceVersion: "1",
				CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
			}
			require.NoError(t, first.CreateServiceAccount(ctx, account))
			renamed := false
			restore := func() error {
				if !renamed {
					return nil
				}
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if err := raw.Query("ALTER TABLE service_accounts_by_cluster_bucket RENAME cleanup_test_uid TO uid").WithContext(cleanupCtx).Exec(); err != nil {
					return err
				}
				renamed = false
				return raw.AwaitSchemaAgreement(cleanupCtx)
			}
			t.Cleanup(func() { require.NoError(t, restore()) })
			if test.failListing {
				require.NoError(t, raw.Query("ALTER TABLE service_accounts_by_cluster_bucket RENAME uid TO cleanup_test_uid").WithContext(ctx).Exec())
				renamed = true
				require.NoError(t, raw.AwaitSchemaAgreement(ctx))
			}
			deletionErr := first.DeleteServiceAccount(deletionCtx, account.UID)
			require.NoError(t, restore())
			require.ErrorIs(t, deletionErr, datastore.ErrRepairRequired, "partial cleanup must not report successful deletion")
			select {
			case listingErr := <-listingResult:
				if test.failListing {
					require.Error(t, listingErr)
					require.Contains(t, deletionErr.Error(), "listing projection")
					if !test.cancelUIDRelease {
						var repairErr *datastore.RepairRequiredError
						require.ErrorAs(t, deletionErr, &repairErr)
						var joined interface{ Unwrap() []error }
						require.ErrorAs(t, repairErr.Primary, &joined)
						causes := joined.Unwrap()
						require.Len(t, causes, 1)
						cause := causes[0]
						for errors.Unwrap(cause) != nil {
							cause = errors.Unwrap(cause)
						}
						require.Equal(t, listingErr.Error(), cause.Error(), "the actual server failure must remain reachable through cleanup wrappers")
					}
				} else {
					require.NoError(t, listingErr)
				}
			default:
				t.Fatal("the native listing operation must complete before injecting routing cancellation")
			}
			if test.cancelUIDRelease {
				require.ErrorIs(t, deletionErr, context.Canceled)
				require.Contains(t, deletionErr.Error(), "UID reservation")
			}
			if test.failListing && test.cancelUIDRelease {
				var repairErr *datastore.RepairRequiredError
				require.ErrorAs(t, deletionErr, &repairErr)
				var joined interface{ Unwrap() []error }
				require.ErrorAs(t, repairErr.Primary, &joined)
				causes := joined.Unwrap()
				require.Len(t, causes, 2, "both failed cleanup operations must remain in the error chain")
				require.Contains(t, causes[0].Error(), "listing projection")
				require.Contains(t, causes[1].Error(), "UID reservation")
			}
			for _, store := range []datastore.Datastore{first, second} {
				_, err := store.GetServiceAccountByUID(ctx, account.UID)
				require.ErrorIs(t, err, datastore.ErrNotFound, "deleted authority must never authenticate through either leftover projection")
			}
			var reservedName string
			err = raw.Query("SELECT name FROM service_accounts_by_cluster_uid WHERE uid=?", account.UID).WithContext(ctx).Scan(&reservedName)
			if test.cancelUIDRelease {
				require.NoError(t, err)
				require.Equal(t, account.Name, reservedName)
			} else {
				require.ErrorIs(t, err, gocql.ErrNotFound, "listing failure must not skip the replacement-safe routing release")
			}
			var listingName string
			err = raw.Query("SELECT name FROM service_accounts_by_cluster_bucket WHERE bucket=? AND creation_timestamp=? AND uid=?",
				account.CreationTimestamp.Format("2006-01"), account.CreationTimestamp, account.UID).WithContext(ctx).Scan(&listingName)
			if test.failListing {
				require.NoError(t, err)
				require.Equal(t, account.Name, listingName)
			} else {
				require.ErrorIs(t, err, gocql.ErrNotFound)
			}
			replacement := &datastore.ServiceAccount{
				UID: newID(), Name: account.Name, ResourceVersion: "1",
				CreationTimestamp: account.CreationTimestamp.Add(time.Second),
			}
			require.NoError(t, second.CreateServiceAccount(ctx, replacement))
			repair, err := scylla.OpenProjectionRepairService(testScyllaConfig(t))
			require.NoError(t, err)
			defer repair.Close()
			plan, err := repair.Audit(ctx)
			require.NoError(t, err)
			found := false
			for _, action := range plan.Actions {
				if action.UID == account.UID {
					found = true
					require.Equal(t, scylla.RepairDelete, action.Type)
					require.True(t, action.RequireAbsentResource)
				}
			}
			require.True(t, found, "partial cleanup must remain visible to the bounded repair audit")
			_, err = repair.Apply(ctx, plan)
			require.NoError(t, err)
			got, err := first.GetServiceAccountByUID(ctx, replacement.UID)
			require.NoError(t, err)
			require.Equal(t, replacement.Name, got.Name, "old-account repair must preserve the name's replacement UID")
			_, err = second.GetServiceAccountByUID(ctx, account.UID)
			require.ErrorIs(t, err, datastore.ErrNotFound)
			err = raw.Query("SELECT name FROM service_accounts_by_cluster_uid WHERE uid=?", account.UID).WithContext(ctx).Scan(&reservedName)
			require.ErrorIs(t, err, gocql.ErrNotFound, "repair must remove the old routing reservation")
			err = raw.Query("SELECT name FROM service_accounts_by_cluster_bucket WHERE bucket=? AND creation_timestamp=? AND uid=?",
				account.CreationTimestamp.Format("2006-01"), account.CreationTimestamp, account.UID).WithContext(ctx).Scan(&listingName)
			require.ErrorIs(t, err, gocql.ErrNotFound, "repair must remove the old listing projection")
		})
	}
}

func TestFileDeleteReportsNameCleanupFailureAndRepairsByUID(t *testing.T) {
	first, second := newTestStores(t)
	ctx := t.Context()
	raw := newRawSession(t)
	require.True(t, strings.HasPrefix(scyllaKeyspace, "gitstore_scylla_test_"), "faults must only alter the generated test keyspace")
	ns := &datastore.Namespace{
		UID: newID(), Name: "file-cleanup-" + newID(), ResourceVersion: "1", Generation: 1,
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
	require.NoError(t, first.CreateNamespace(ctx, ns))
	repository := newRepository()
	repository.Namespace = ns.Name
	repository.NamespaceID, repository.ResourceVersion, repository.Generation = ns.UID, "1", 1
	require.NoError(t, first.CreateRepository(ctx, repository))
	file := &datastore.File{
		UID: newID(), Namespace: ns.Name, Name: "fixture", RepositoryID: repository.UID,
		ResourceVersion: "1", CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
	require.NoError(t, first.CreateFile(ctx, file))
	require.ErrorIs(t, first.DeleteFileWithResourceVersion(ctx, file.UID, "stale"), datastore.ErrConflict)
	for _, store := range []datastore.Datastore{first, second} {
		got, err := store.GetFileByName(ctx, ns.Name, file.Name)
		require.NoError(t, err)
		require.Equal(t, file.UID, got.UID, "stale deletion must not mutate lookup projections")
	}
	present, err := second.HasCatalogResources(ctx, repository.UID)
	require.NoError(t, err)
	require.True(t, present, "a rejected deletion must retain repository membership")
	require.NoError(t, raw.Query("ALTER TABLE files_by_name RENAME name TO cleanup_test_name").WithContext(ctx).Exec())
	renamed := true
	restore := func() error {
		if !renamed {
			return nil
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := raw.Query("ALTER TABLE files_by_name RENAME cleanup_test_name TO name").WithContext(cleanupCtx).Exec(); err != nil {
			return err
		}
		renamed = false
		return raw.AwaitSchemaAgreement(cleanupCtx)
	}
	t.Cleanup(func() { require.NoError(t, restore()) })
	require.NoError(t, raw.AwaitSchemaAgreement(ctx))
	deletionErr := first.DeleteFileWithResourceVersion(ctx, file.UID, file.ResourceVersion)
	require.NoError(t, restore())
	require.ErrorIs(t, deletionErr, datastore.ErrRepairRequired, "committed deletion must not hide name cleanup failure")
	require.Contains(t, deletionErr.Error(), "files_by_name")
	for _, store := range []datastore.Datastore{first, second} {
		_, err := store.GetFile(ctx, file.UID)
		require.ErrorIs(t, err, datastore.ErrNotFound)
		_, err = store.GetFileByName(ctx, ns.Name, file.Name)
		require.ErrorIs(t, err, datastore.ErrNotFound, "stale name lookup must validate authoritative absence")
	}
	var routedNamespace string
	err = raw.Query("SELECT namespace FROM files_by_uid WHERE uid=?", file.UID).WithContext(ctx).Scan(&routedNamespace)
	require.ErrorIs(t, err, gocql.ErrNotFound, "name cleanup failure must not skip conditional old-UID release")
	var namedUID gocql.UUID
	require.NoError(t, raw.Query("SELECT uid FROM files_by_name WHERE namespace=? AND name=?", ns.Name, file.Name).WithContext(ctx).Scan(&namedUID))
	require.Equal(t, file.UID, namedUID.String())
	present, err = second.HasCatalogResources(ctx, repository.UID)
	require.NoError(t, err)
	require.False(t, present, "confirmed authoritative removal can clear repository membership without hiding cleanup failure")
	replacement := *file
	replacement.UID = newID()
	replacement.CreationTimestamp = file.CreationTimestamp.Add(time.Second)
	var pending int64
	require.NoError(t, raw.Query("SELECT pending_repository_creations FROM namespaces_by_uid WHERE uid=?", ns.UID).WithContext(ctx).Scan(&pending))
	require.EqualValues(t, 1, pending, "partial cleanup must retain its conservative namespace fence")
	blockedCtx, cancelBlocked := context.WithTimeout(ctx, 200*time.Millisecond)
	require.ErrorIs(t, second.CreateFile(blockedCtx, &replacement), context.DeadlineExceeded, "admission must not pass a retained repair fence")
	cancelBlocked()
	repair, err := scylla.OpenProjectionRepairService(testScyllaConfig(t))
	require.NoError(t, err)
	defer repair.Close()
	plan, err := repair.Audit(ctx)
	require.NoError(t, err)
	found := false
	for _, action := range plan.Actions {
		if action.UID == file.UID && action.Before != nil && action.Before.Table == "files_by_name" {
			found = true
			require.Equal(t, scylla.RepairDelete, action.Type)
			require.True(t, action.RequireAbsentResource)
		}
	}
	require.True(t, found, "orphaned name cleanup must remain visible to bounded repair")
	_, err = repair.Apply(ctx, plan)
	require.NoError(t, err)
	require.NoError(t, raw.Query("SELECT pending_repository_creations FROM namespaces_by_uid WHERE uid=?", ns.UID).WithContext(ctx).Scan(&pending))
	require.Zero(t, pending, "verified quiescent repair must release the retained fence")
	require.NoError(t, second.CreateFile(ctx, &replacement))
	_, err = repair.Apply(ctx, plan)
	require.ErrorContains(t, err, "conditional mutation was not applied", "stale repair must fail closed without removing replacement reservations")
	got, err := first.GetFileByName(ctx, ns.Name, file.Name)
	require.NoError(t, err)
	require.Equal(t, replacement.UID, got.UID)
	_, err = second.GetFile(ctx, file.UID)
	require.ErrorIs(t, err, datastore.ErrNotFound)
	present, err = first.HasCatalogResources(ctx, repository.UID)
	require.NoError(t, err)
	require.True(t, present, "replacement admission must remain a repository deletion blocker")
}

type sessionRevocationStore interface {
	RevokeSession(context.Context, string, time.Time) error
	IsSessionRevoked(context.Context, string) (bool, error)
	ConsumeSession(context.Context, string, time.Time) (bool, error)
}

func TestSessionRevocationsAreSharedAcrossReplicas(t *testing.T) {
	firstStore, secondStore := newTestStores(t)
	first := firstStore.(sessionRevocationStore)
	second := secondStore.(sessionRevocationStore)
	ctx := context.Background()
	jti := "logout-" + newID()
	require.NoError(t, first.RevokeSession(ctx, jti, time.Now().Add(time.Hour)))
	revoked, err := second.IsSessionRevoked(ctx, jti)
	require.NoError(t, err)
	assert.True(t, revoked)

	refreshJTI := "refresh-" + newID()
	consumed, err := first.ConsumeSession(ctx, refreshJTI, time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.True(t, consumed)
	consumed, err = second.ConsumeSession(ctx, refreshJTI, time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.False(t, consumed)
}

func newID() string { return uuid.New().String() }

func newProduct(namespace, name string) *datastore.Product {
	return &datastore.Product{
		UID:               newID(),
		Namespace:         namespace,
		Name:              name,
		APIVersion:        "catalog.gitstore.dev/v1beta1",
		Kind:              "Product",
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
}

func TestCategoryProductMembershipRetryRepairsInterruptedForwardWrite(t *testing.T) {
	first, second := newTestStores(t)
	raw := newRawSession(t)
	ctx := t.Context()
	product := newProduct("membership-"+newID(), "product")
	firstIndex := first.(datastore.CategoryProductIndex)
	secondIndex := second.(datastore.CategoryProductIndex)
	product.ResourceVersion = "1"
	product.Status = json.RawMessage(`{}`)
	require.NoError(t, first.CreateProduct(ctx, product))
	categoryUID := newID()
	require.NoError(t, firstIndex.ReplaceCategoryProductMembership(ctx, product, []string{categoryUID}))

	var shard int
	require.NoError(t, raw.Query("SELECT shard FROM category_products_by_product WHERE namespace=? AND product_uid=? AND category_uid=?",
		product.Namespace, uuid.MustParse(product.UID), categoryUID).Scan(&shard))
	require.NoError(t, raw.Query("DELETE FROM category_products_by_category WHERE namespace=? AND category_uid=? AND shard=? AND creation_timestamp=? AND product_uid=?",
		product.Namespace, categoryUID, shard, product.CreationTimestamp, uuid.MustParse(product.UID)).Exec())

	// A second replica retries the same version after the reverse write survived.
	require.NoError(t, secondIndex.ReplaceCategoryProductMembership(ctx, product, []string{categoryUID}))
	var version string
	err := raw.Query("SELECT resource_version FROM category_products_by_category WHERE namespace=? AND category_uid=? AND shard=? AND creation_timestamp=? AND product_uid=?",
		product.Namespace, categoryUID, shard, product.CreationTimestamp, uuid.MustParse(product.UID)).Scan(&version)
	require.NoError(t, err, "same-version retry must complete the interrupted forward write")
	require.Equal(t, product.ResourceVersion, version)

	require.NoError(t, raw.Query("UPDATE category_products_by_product SET resource_version=? WHERE namespace=? AND product_uid=? AND category_uid=?",
		"2", product.Namespace, uuid.MustParse(product.UID), categoryUID).Exec())
	require.NoError(t, raw.Query("DELETE FROM category_products_by_category WHERE namespace=? AND category_uid=? AND shard=? AND creation_timestamp=? AND product_uid=?",
		product.Namespace, categoryUID, shard, product.CreationTimestamp, uuid.MustParse(product.UID)).Exec())
	require.NoError(t, firstIndex.ReplaceCategoryProductMembership(ctx, product, []string{categoryUID}))
	err = raw.Query("SELECT resource_version FROM category_products_by_category WHERE namespace=? AND category_uid=? AND shard=? AND creation_timestamp=? AND product_uid=?",
		product.Namespace, categoryUID, shard, product.CreationTimestamp, uuid.MustParse(product.UID)).Scan(&version)
	require.ErrorIs(t, err, gocql.ErrNotFound, "older retries must not restore superseded membership")
}

func TestCategoryProductDeletionCleansBothDirectionsAndFencesStaleRetry(t *testing.T) {
	first, second := newTestStores(t)
	raw := newRawSession(t)
	ctx := t.Context()
	product := newProduct("membership-deletion-"+newID(), "product")
	product.ResourceVersion = "1"
	product.Status = json.RawMessage(`{}`)
	categoryUID := newID()
	index := first.(datastore.CategoryProductIndex)
	require.NoError(t, first.CreateProduct(ctx, product))
	require.NoError(t, index.ReplaceCategoryProductMembership(ctx, product, []string{categoryUID}))
	var shard int
	require.NoError(t, raw.Query("SELECT shard FROM category_products_by_product WHERE namespace=? AND product_uid=? AND category_uid=?",
		product.Namespace, uuid.MustParse(product.UID), categoryUID).Scan(&shard))
	require.NoError(t, second.DeleteProductWithResourceVersion(ctx, product.UID, product.ResourceVersion))
	assertAbsent := func() {
		t.Helper()
		var version string
		err := raw.Query("SELECT resource_version FROM category_products_by_product WHERE namespace=? AND product_uid=? AND category_uid=?",
			product.Namespace, uuid.MustParse(product.UID), categoryUID).Scan(&version)
		require.ErrorIs(t, err, gocql.ErrNotFound, "deleted Product reverse membership must be removed")
		err = raw.Query("SELECT resource_version FROM category_products_by_category WHERE namespace=? AND category_uid=? AND shard=? AND creation_timestamp=? AND product_uid=?",
			product.Namespace, categoryUID, shard, product.CreationTimestamp, uuid.MustParse(product.UID)).Scan(&version)
		require.ErrorIs(t, err, gocql.ErrNotFound, "deleted Product forward membership must be removed")
	}
	assertAbsent()
	require.NoError(t, index.ReplaceCategoryProductMembership(ctx, product, []string{categoryUID}))
	assertAbsent()
}

func TestScyllaCatalogListsHonorRequestCancellation(t *testing.T) {
	store := newTestStore(t)
	for name, list := range map[string]func(context.Context) error{
		"Product": func(ctx context.Context) error {
			_, err := store.ListProducts(ctx, "cancelled", datastore.PageParams{First: 1})
			return err
		},
		"CategoryTaxonomy": func(ctx context.Context) error {
			_, err := store.ListCategoryTaxonomies(ctx, "cancelled", datastore.PageParams{First: 1})
			return err
		},
		"Collection": func(ctx context.Context) error {
			_, err := store.ListCollections(ctx, "cancelled", datastore.PageParams{First: 1})
			return err
		},
		"ProductVariant": func(ctx context.Context) error {
			_, err := store.ListProductVariants(ctx, "cancelled", datastore.PageParams{First: 1})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			require.ErrorIs(t, list(ctx), context.Canceled)
		})
	}
}

func TestScyllaCatalogPointsHonorRequestCancellation(t *testing.T) {
	store := newTestStore(t)
	uid := newID()
	for name, read := range map[string]func(context.Context) error{
		"Product/UID": func(ctx context.Context) error { _, err := store.GetProduct(ctx, uid); return err },
		"Product/name": func(ctx context.Context) error {
			_, err := store.GetProductByName(ctx, "cancelled", "product")
			return err
		},
		"CategoryTaxonomy/UID": func(ctx context.Context) error { _, err := store.GetCategoryTaxonomy(ctx, uid); return err },
		"CategoryTaxonomy/name": func(ctx context.Context) error {
			_, err := store.GetCategoryTaxonomyByName(ctx, "cancelled", "category")
			return err
		},
		"Collection/UID": func(ctx context.Context) error { _, err := store.GetCollection(ctx, uid); return err },
		"Collection/name": func(ctx context.Context) error {
			_, err := store.GetCollectionByName(ctx, "cancelled", "collection")
			return err
		},
		"ProductVariant/UID": func(ctx context.Context) error { _, err := store.GetProductVariant(ctx, uid); return err },
		"ProductVariant/name": func(ctx context.Context) error {
			_, err := store.GetProductVariantByName(ctx, "cancelled", "variant")
			return err
		},
		"ProductVariant/SKU": func(ctx context.Context) error {
			_, err := store.GetProductVariantBySKU(ctx, "cancelled", "sku")
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			require.ErrorIs(t, read(ctx), context.Canceled)
		})
	}
}

func TestCategoryProductsProjectionReadinessFailsClosedAcrossClients(t *testing.T) {
	first, second := newTestStores(t)
	raw := newRawSession(t)
	var originalReady bool
	originalErr := raw.Query("SELECT ready FROM category_product_projection_state WHERE projection=?",
		"category-products").Scan(&originalReady)
	require.True(t, originalErr == nil || errors.Is(originalErr, gocql.ErrNotFound))
	t.Cleanup(func() {
		if errors.Is(originalErr, gocql.ErrNotFound) {
			require.NoError(t, raw.Query("DELETE FROM category_product_projection_state WHERE projection=?",
				"category-products").Exec())
		} else {
			require.NoError(t, raw.Query("INSERT INTO category_product_projection_state (projection,ready) VALUES (?,?)",
				"category-products", originalReady).Exec())
		}
	})
	product := newProduct("category-ready-"+newID(), "product")
	product.ResourceVersion = "1"
	product.Status = json.RawMessage(`{}`)
	categoryUID := newID()
	require.NoError(t, first.CreateProduct(t.Context(), product))
	require.NoError(t, first.(datastore.CategoryProductIndex).ReplaceCategoryProductMembership(t.Context(), product, []string{categoryUID}))
	indexes := []datastore.CategoryProductIndex{
		first.(datastore.CategoryProductIndex), second.(datastore.CategoryProductIndex),
	}
	assertReady := func() {
		t.Helper()
		for _, index := range indexes {
			page, err := index.ListCategoryProducts(t.Context(), product.Namespace, categoryUID, datastore.PageParams{First: 1})
			require.NoError(t, err)
			require.NotNil(t, page)
			require.Len(t, page.Items, 1)
			require.Equal(t, product.UID, page.Items[0].UID)
		}
	}
	setReady := func(ready bool) {
		t.Helper()
		require.NoError(t, raw.Query("INSERT INTO category_product_projection_state (projection,ready) VALUES (?,?)",
			"category-products", ready).Exec())
	}
	setReady(true)
	assertReady()
	for _, remove := range []bool{false, true} {
		if remove {
			require.NoError(t, raw.Query("DELETE FROM category_product_projection_state WHERE projection=?",
				"category-products").Exec())
		} else {
			setReady(false)
		}
		for _, index := range indexes {
			page, err := index.ListCategoryProducts(t.Context(), product.Namespace, categoryUID, datastore.PageParams{First: 1})
			require.ErrorIs(t, err, datastore.ErrProjectionNotReady)
			require.Nil(t, page, "a warmed client must not serve a stale-ready or success-shaped empty page")
		}
		setReady(true)
		assertReady()
	}
	func() {
		require.NoError(t, raw.Query("ALTER TABLE category_product_projection_state RENAME projection TO fixture_projection_key").Exec())
		defer func() {
			require.NoError(t, raw.Query("ALTER TABLE category_product_projection_state RENAME fixture_projection_key TO projection").Exec())
		}()
		for _, index := range indexes {
			page, err := index.ListCategoryProducts(t.Context(), product.Namespace, categoryUID, datastore.PageParams{First: 1})
			require.Error(t, err)
			require.NotErrorIs(t, err, datastore.ErrProjectionNotReady, "storage failure is not confirmed rollout unreadiness")
			require.ErrorContains(t, err, "read category product projection state")
			require.Nil(t, page)
		}
	}()
	assertReady()
	for _, index := range indexes {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		page, err := index.ListCategoryProducts(ctx, product.Namespace, categoryUID, datastore.PageParams{First: 1})
		require.ErrorIs(t, err, context.Canceled)
		require.Nil(t, page)
	}
}

func TestCategoryProductsPagesRefillDanglingHotShard(t *testing.T) {
	first := newTestStore(t)
	cfg := testScyllaConfig(t)
	cfg.QueryObserver = capacityQueryObserver{}
	second, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, second.Close()) })
	raw := newRawSession(t)
	ctx := t.Context()
	namespace, categoryUID := "category-page-"+newID(), newID()
	created := time.Now().UTC().Truncate(time.Millisecond)
	index := first.(datastore.CategoryProductIndex)
	var products []*datastore.Product
	t.Cleanup(func() {
		for shard := range 32 {
			require.NoError(t, raw.Query("DELETE FROM category_products_by_category WHERE namespace=? AND category_uid=? AND shard=?",
				namespace, categoryUID, shard).Exec())
		}
		for _, product := range products {
			require.NoError(t, raw.Query("DELETE FROM category_products_by_product WHERE namespace=? AND product_uid=?",
				namespace, uuid.MustParse(product.UID)).Exec())
			require.NoError(t, first.DeleteProduct(context.Background(), product.UID))
		}
	})
	for i := range 5 {
		product := newProduct(namespace, fmt.Sprintf("product-%d", i))
		product.CreationTimestamp = created.Add(-time.Duration(i+1) * time.Second)
		product.ResourceVersion = "1"
		product.Status = json.RawMessage(`{}`)
		require.NoError(t, first.CreateProduct(ctx, product))
		require.NoError(t, index.ReplaceCategoryProductMembership(ctx, product, []string{categoryUID}))
		products = append(products, product)
	}
	for i := range 12 {
		require.NoError(t, raw.Query("INSERT INTO category_products_by_category (namespace,category_uid,shard,creation_timestamp,product_uid,resource_version) VALUES (?,?,?,?,?,?)",
			namespace, categoryUID, 0, created.Add(time.Duration(i)*time.Second), uuid.New(), "1").Exec())
	}
	require.NoError(t, raw.Query("INSERT INTO category_product_projection_state (projection,ready) VALUES (?,?)",
		"category-products", true).Exec())
	list := second.(datastore.CategoryProductIndex)
	page, err := list.ListCategoryProducts(ctx, namespace, categoryUID, datastore.PageParams{First: 2})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	require.True(t, page.HasNext)
	require.Equal(t, products[0].UID, page.Items[0].UID)
	require.Equal(t, products[1].UID, page.Items[1].UID)
	next, err := list.ListCategoryProducts(ctx, namespace, categoryUID,
		datastore.PageParams{First: 2, After: pageCursor(products[1].CreationTimestamp, products[1].UID)})
	require.NoError(t, err)
	require.Len(t, next.Items, 2)
	require.Equal(t, products[2].UID, next.Items[0].UID)
	previous, err := list.ListCategoryProducts(ctx, namespace, categoryUID,
		datastore.PageParams{Last: 2, Before: pageCursor(products[2].CreationTimestamp, products[2].UID)})
	require.NoError(t, err)
	require.Equal(t, page.Items, previous.Items)
	require.False(t, previous.HasPrevious, "dangling projection rows cannot advertise live membership")
	assertCapacityDatastorePage(t, "Category.products/dangling-hot-shard", 2,
		func(ctx context.Context, params datastore.PageParams) (*datastore.PageResult[datastore.Product], error) {
			return list.ListCategoryProducts(ctx, namespace, categoryUID, params)
		}, func(product *datastore.Product) capacityPageKey {
			return capacityPageKey{uid: product.UID, created: product.CreationTimestamp}
		})
}

func newCollection(namespace, name string) *datastore.Collection {
	return &datastore.Collection{
		UID:               newID(),
		Namespace:         namespace,
		Name:              name,
		APIVersion:        "catalog.gitstore.dev/v1beta1",
		Kind:              "Collection",
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
}

func newProductVariant(namespace, name, sku, productRefName string) *datastore.ProductVariant {
	return &datastore.ProductVariant{
		UID:               newID(),
		Namespace:         namespace,
		Name:              name,
		APIVersion:        "catalog.gitstore.dev/v1beta1",
		Kind:              "ProductVariant",
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
		SKU:               sku,
		ProductRefName:    productRefName,
	}
}

func newRepository() *datastore.Repository {
	now := time.Now().UTC().Truncate(time.Millisecond)
	uid := newID()
	namespace := "namespace-" + newID()[:8]
	return &datastore.Repository{
		UID:               uid,
		ID:                uid,
		Namespace:         namespace,
		NamespaceID:       namespace,
		Name:              "repo-" + newID()[:8],
		DefaultBranch:     "main",
		StorageClass:      "default",
		CreationTimestamp: now,
		CreationActor:     "test",
		UpdateTimestamp:   now,
		UpdateActor:       "test",
	}
}

func setRepositoryContractFields(
	t *testing.T,
	repository *datastore.Repository,
	generation int64,
	resourceVersion string,
	status json.RawMessage,
) {
	t.Helper()
	value := reflect.ValueOf(repository).Elem()

	generationField := value.FieldByName("Generation")
	if !generationField.IsValid() {
		t.Fatal("datastore.Repository is missing Generation")
	}
	require.Equal(t, reflect.Int64, generationField.Kind())
	generationField.SetInt(generation)

	resourceVersionField := value.FieldByName("ResourceVersion")
	if !resourceVersionField.IsValid() {
		t.Fatal("datastore.Repository is missing ResourceVersion")
	}
	require.Equal(t, reflect.String, resourceVersionField.Kind())
	resourceVersionField.SetString(resourceVersion)

	statusField := value.FieldByName("Status")
	if !statusField.IsValid() {
		t.Fatal("datastore.Repository is missing Status")
	}
	require.Equal(t, reflect.TypeOf(json.RawMessage{}), statusField.Type())
	statusField.Set(reflect.ValueOf(status))
}

func repositoryContractFields(t *testing.T, repository *datastore.Repository) (int64, string, json.RawMessage) {
	t.Helper()
	value := reflect.ValueOf(repository).Elem()

	generationField := value.FieldByName("Generation")
	if !generationField.IsValid() {
		t.Fatal("datastore.Repository is missing Generation")
	}
	require.Equal(t, reflect.Int64, generationField.Kind())

	resourceVersionField := value.FieldByName("ResourceVersion")
	if !resourceVersionField.IsValid() {
		t.Fatal("datastore.Repository is missing ResourceVersion")
	}
	require.Equal(t, reflect.String, resourceVersionField.Kind())

	statusField := value.FieldByName("Status")
	if !statusField.IsValid() {
		t.Fatal("datastore.Repository is missing Status")
	}
	require.Equal(t, reflect.TypeOf(json.RawMessage{}), statusField.Type())

	return generationField.Int(), resourceVersionField.String(), statusField.Interface().(json.RawMessage)
}

// ── Repository ───────────────────────────────────────────────────────────────

func TestScylla_RepositoryResourceContractRoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	repository := newRepository()
	require.NoError(t, store.CreateNamespace(ctx, &datastore.Namespace{UID: newID(), Name: repository.Namespace}))
	initialStatus := json.RawMessage(`{
		"observedGeneration": 4,
		"lastAppliedRevision": "abc123",
		"conditions": [{
			"type": "Ready",
			"status": "True",
			"reason": "Reconciled",
			"message": "repository is ready",
			"lastTransitionTime": "2026-08-16T20:00:00Z",
			"observedGeneration": 4
		}]
	}`)
	setRepositoryContractFields(t, repository, 5, "12", initialStatus)

	require.NoError(t, store.CreateRepository(ctx, repository))

	created, err := store.GetRepository(ctx, repository.ID)
	require.NoError(t, err)
	generation, resourceVersion, status := repositoryContractFields(t, created)
	assert.Equal(t, int64(5), generation)
	assert.Equal(t, "12", resourceVersion)
	assert.JSONEq(t, string(initialStatus), string(status))

	updatedStatus := json.RawMessage(`{
		"observedGeneration": 6,
		"lastAppliedRevision": "def456",
		"conditions": [{
			"type": "Ready",
			"status": "False",
			"reason": "Reconciling",
			"message": "repository update is pending",
			"lastTransitionTime": "2026-08-16T20:30:00Z",
			"observedGeneration": 6
		}]
	}`)
	setRepositoryContractFields(t, created, 6, "13", updatedStatus)
	require.NoError(t, store.UpdateRepository(ctx, created, "12"))

	updated, err := store.GetRepository(ctx, repository.ID)
	require.NoError(t, err)
	generation, resourceVersion, status = repositoryContractFields(t, updated)
	assert.Equal(t, int64(6), generation)
	assert.Equal(t, "13", resourceVersion)
	assert.JSONEq(t, string(updatedStatus), string(status))
}

func TestScylla_RepositoryResourceContractLegacyNormalization(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	legacyRepository := newRepository()

	require.NoError(t, store.CreateRepository(ctx, legacyRepository))

	got, err := store.GetRepository(ctx, legacyRepository.ID)
	require.NoError(t, err)
	generation, resourceVersion, status := repositoryContractFields(t, got)
	assert.Equal(t, int64(1), generation)
	assert.Equal(t, "1", resourceVersion)
	assert.JSONEq(t, `{"observedGeneration":0,"conditions":[]}`, string(status))
}

func TestScylla_RepositoryVersionTransitionsPersist(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	repository := newRepository()
	require.NoError(t, store.CreateNamespace(ctx, &datastore.Namespace{UID: newID(), Name: repository.Namespace}))
	require.NoError(t, store.CreateRepository(ctx, repository))

	expectedResourceVersion := repository.ResourceVersion
	datastore.AdvanceRepositorySpecVersion(repository)
	require.NoError(t, store.UpdateRepository(ctx, repository, expectedResourceVersion))

	afterSpec, err := store.GetRepository(ctx, repository.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), afterSpec.Generation)
	assert.Equal(t, "2", afterSpec.ResourceVersion)
	assert.JSONEq(t, `{"observedGeneration":0,"conditions":[]}`, string(afterSpec.Status))

	afterSpec.Status = json.RawMessage(`{
		"observedGeneration": 2,
		"lastAppliedRevision": "main@sha1:abc",
		"conditions": [{
			"type": "Ready",
			"status": "True",
			"observedGeneration": 2,
			"lastTransitionTime": "2026-08-16T20:00:00Z"
		}]
	}`)
	expectedResourceVersion = afterSpec.ResourceVersion
	datastore.AdvanceRepositorySystemVersion(afterSpec)
	require.NoError(t, store.UpdateRepository(ctx, afterSpec, expectedResourceVersion))

	afterSystem, err := store.GetRepository(ctx, repository.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), afterSystem.Generation)
	assert.Equal(t, "3", afterSystem.ResourceVersion)
	assert.JSONEq(t, string(afterSpec.Status), string(afterSystem.Status))
}

func TestScylla_RepositoryPushPolicyRoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	repository := newRepository()
	require.NoError(t, store.CreateNamespace(ctx, &datastore.Namespace{UID: newID(), Name: repository.Namespace}))
	repository.MaxPackSizeBytes = 64 * 1024 * 1024
	repository.MaxFileSizeBytes = 8 * 1024 * 1024
	require.NoError(t, store.CreateRepository(ctx, repository))

	created, err := store.GetRepository(ctx, repository.ID)
	require.NoError(t, err)
	assert.Equal(t, repository.MaxPackSizeBytes, created.MaxPackSizeBytes)
	assert.Equal(t, repository.MaxFileSizeBytes, created.MaxFileSizeBytes)

	expectedResourceVersion := created.ResourceVersion
	created.MaxPackSizeBytes *= 2
	created.MaxFileSizeBytes *= 2
	datastore.AdvanceRepositorySpecVersion(created)
	require.NoError(t, store.UpdateRepository(ctx, created, expectedResourceVersion))

	updated, err := store.GetRepository(ctx, repository.ID)
	require.NoError(t, err)
	assert.Equal(t, created.MaxPackSizeBytes, updated.MaxPackSizeBytes)
	assert.Equal(t, created.MaxFileSizeBytes, updated.MaxFileSizeBytes)
}

func TestScylla_UpdateRepositoryRejectsStaleResourceVersion(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	repository := newRepository()
	require.NoError(t, store.CreateNamespace(ctx, &datastore.Namespace{UID: newID(), Name: repository.Namespace}))
	require.NoError(t, store.CreateRepository(ctx, repository))

	first, err := store.GetRepository(ctx, repository.ID)
	require.NoError(t, err)
	stale, err := store.GetRepository(ctx, repository.ID)
	require.NoError(t, err)

	first.Name = "first-writer"
	datastore.AdvanceRepositorySpecVersion(first)
	require.NoError(t, store.UpdateRepository(ctx, first, "1"))

	stale.Name = "stale-writer"
	datastore.AdvanceRepositorySpecVersion(stale)
	require.ErrorIs(t, store.UpdateRepository(ctx, stale, "1"), datastore.ErrConflict)

	got, err := store.GetRepository(ctx, repository.ID)
	require.NoError(t, err)
	assert.Equal(t, "first-writer", got.Name)
	assert.Equal(t, "2", got.ResourceVersion)
}

func TestScylla_FileRoundTripAndIndexedLookup(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	file := &datastore.File{
		UID: newID(), Namespace: "test-ns", Name: "hero-" + newID()[:8],
		APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File",
		Generation: 1, ResourceVersion: "1", CreationTimestamp: time.Now().UTC(),
		Spec: json.RawMessage(`{"ContentType":"image/jpeg","Source":{"Type":"s3","URI":"s3://bucket/hero"}}`),
		Body: "alt text", OwnerReferences: json.RawMessage(`[{"kind":"Repository","name":"repo","uid":"owner"}]`),
		Status: json.RawMessage(`{"conditions":[]}`),
	}
	require.NoError(t, store.CreateFile(ctx, file))
	got, err := store.GetFileByName(ctx, file.Namespace, file.Name)
	require.NoError(t, err)
	assert.Equal(t, file.UID, got.UID)
	assert.JSONEq(t, string(file.Spec), string(got.Spec))
	assert.JSONEq(t, string(file.OwnerReferences), string(got.OwnerReferences))
	assert.Equal(t, file.Body, got.Body)
}

func TestScylla_FilePointReadsPreserveAuthoritativeReadback(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		require.NoError(t, provider.Shutdown(context.Background()))
	})
	first, second := newTestStores(t)
	readers := []datastore.Datastore{first, second}
	readFiles := func(store datastore.Datastore, uids []string) []*datastore.File {
		t.Helper()
		files := make([]*datastore.File, len(uids))
		for i, uid := range uids {
			file, err := store.GetFile(t.Context(), uid)
			if errors.Is(err, datastore.ErrNotFound) {
				continue
			}
			require.NoError(t, err)
			files[i] = file
		}
		return files
	}
	files := make([]*datastore.File, 7)
	for i := range files {
		namespace := "file-snapshots-" + strconv.Itoa(i%2) + "-" + newID()[:8]
		if i >= 2 {
			namespace = files[i%2].Namespace
		} else {
			require.NoError(t, first.CreateNamespace(t.Context(), &datastore.Namespace{UID: newID(), Name: namespace}))
		}
		files[i] = &datastore.File{UID: newID(), Namespace: namespace, Name: "file-" + strconv.Itoa(i),
			Kind: "File", APIVersion: "storage.gitstore.dev/v1beta1", ResourceVersion: "1",
			CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
			Annotations:       map[string]string{"sequence": "1"}, Spec: json.RawMessage(`{"contentType":"image/jpeg"}`)}
		require.NoError(t, first.CreateFile(t.Context(), files[i]))
	}
	uids := []string{files[6].UID, "invalid", files[3].UID, newID(), files[0].UID, files[3].UID, files[1].UID, files[5].UID}
	for _, locator := range []string{"files_by_uid", "files_by_name"} {
		t.Run(locator, func(t *testing.T) {
			ctx, parent := provider.Tracer("test").Start(t.Context(), "file_snapshot_read")
			var snapshots []*datastore.File
			var err error
			if locator == "files_by_uid" {
				snapshots, err = second.GetFileSnapshots(ctx, []string{files[0].UID})
			} else {
				snapshots, err = second.GetFileSnapshotsByName(ctx, files[0].Namespace, []string{files[0].Name})
			}
			parent.End()
			require.NoError(t, err)
			require.Len(t, snapshots, 1)
			require.Equal(t, files[0].UID, snapshots[0].UID)
			collections := make(map[string]int)
			exchanges := make(map[string]bool)
			var attempts []sdktrace.ReadOnlySpan
			for _, span := range recorder.Ended() {
				if span.SpanContext().TraceID() != parent.SpanContext().TraceID() {
					continue
				}
				if span.Name() == "scylla.query" {
					attempts = append(attempts, span)
				}
				if span.Name() != "scylla.exchange" {
					continue
				}
				require.Equal(t, parent.SpanContext().SpanID(), span.Parent().SpanID())
				exchanges[span.SpanContext().SpanID().String()] = true
				for _, attr := range span.Attributes() {
					if attr.Key == "db.collection.name" {
						collections[attr.Value.AsString()]++
					}
					if attr.Key == "gitstore.db.stream.completed" {
						require.Positive(t, attr.Value.AsInt64())
					}
					if attr.Key == "gitstore.db.stream.overflow" {
						require.Zero(t, attr.Value.AsInt64())
					}
				}
			}
			require.Equal(t, map[string]int{locator: 1, "files_by_namespace": 1}, collections)
			require.Len(t, attempts, 2, "tracing must not add CQL queries")
			for _, attempt := range attempts {
				require.True(t, exchanges[attempt.Parent().SpanID().String()], "each attempt must belong to its exact read exchange")
			}
		})
	}
	for _, reader := range readers {
		snapshots := readFiles(reader, uids)
		bulk, err := reader.GetFileSnapshots(t.Context(), uids)
		require.NoError(t, err)
		require.Equal(t, snapshots, bulk)
		bulk[2].Annotations["sequence"] = "mutated-clone"
		bulk[2].Spec[0] = '!'
		require.Equal(t, "1", bulk[5].Annotations["sequence"])
		require.JSONEq(t, `{"contentType":"image/jpeg"}`, string(bulk[5].Spec))
		require.Len(t, snapshots, len(uids))
		for i, snapshot := range snapshots {
			if i == 1 || i == 3 {
				require.Nil(t, snapshot)
				continue
			}
			require.NotNil(t, snapshot)
			require.Equal(t, uids[i], snapshot.UID)
			point, err := second.GetFile(t.Context(), uids[i])
			require.NoError(t, err)
			require.Equal(t, point, snapshot)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err = reader.GetFile(ctx, uids[0])
		require.ErrorIs(t, err, context.Canceled)
		_, err = reader.GetFileSnapshots(ctx, uids)
		require.ErrorIs(t, err, context.Canceled)
	}
	updated, err := second.GetFile(t.Context(), files[3].UID)
	require.NoError(t, err)
	updated.ResourceVersion, updated.Annotations["sequence"] = "2", "2"
	require.NoError(t, second.UpdateFile(t.Context(), updated, "1"))
	snapshots := readFiles(readers[0], uids)
	require.Equal(t, "2", snapshots[2].ResourceVersion)
	require.Equal(t, "2", snapshots[5].Annotations["sequence"])
	bulk, err := first.GetFileSnapshots(t.Context(), uids)
	require.NoError(t, err)
	require.Equal(t, snapshots, bulk)

	require.NoError(t, first.DeleteFile(t.Context(), files[3].UID))
	replacement := *files[3]
	replacement.UID, replacement.CreationTimestamp = newID(), time.Now().UTC().Truncate(time.Millisecond)
	require.NoError(t, second.CreateFile(t.Context(), &replacement))
	snapshots = readFiles(readers[0], []string{files[3].UID, replacement.UID})
	require.Nil(t, snapshots[0])
	require.Equal(t, replacement.UID, snapshots[1].UID)
	bulk, err = first.GetFileSnapshots(t.Context(), []string{files[3].UID, replacement.UID})
	require.NoError(t, err)
	require.Equal(t, snapshots, bulk)

	raw := newRawSession(t)
	parsedUID, err := gocql.ParseUUID(files[0].UID)
	require.NoError(t, err)
	require.NoError(t, raw.Query("UPDATE files_by_uid SET namespace=? WHERE uid=?", "", parsedUID).Exec())
	bulk, err = second.GetFileSnapshots(t.Context(), []string{files[1].UID, files[0].UID})
	require.ErrorIs(t, err, datastore.ErrRepairRequired)
	require.Nil(t, bulk, "a corrupt locator cannot return partially authorized snapshots")
	require.NoError(t, raw.Query("UPDATE files_by_uid SET namespace=? WHERE uid=?", files[0].Namespace, parsedUID).Exec())
	require.NoError(t, raw.Query("DELETE FROM files_by_namespace WHERE namespace=? AND creation_timestamp=? AND uid=?",
		files[0].Namespace, files[0].CreationTimestamp, parsedUID).Exec())
	snapshots = readFiles(readers[1], []string{files[0].UID})
	require.Nil(t, snapshots[0], "a retained UID locator is not authoritative resource data")
	bulk, err = second.GetFileSnapshots(t.Context(), []string{files[0].UID})
	require.NoError(t, err)
	require.Equal(t, snapshots, bulk)
}

func TestScylla_FileUpdateUsesNarrowFreshProjectionReadback(t *testing.T) {
	for _, transition := range []string{"none", "stale", "renamed", "deleted", "rebound", "canceled"} {
		t.Run(transition, func(t *testing.T) {
			type updateKey struct{}
			peer := newTestStore(t)
			raw := newRawSession(t)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			ns := &datastore.Namespace{UID: newID(), Name: "file-projection-" + newID()[:8]}
			require.NoError(t, peer.CreateNamespace(ctx, ns))
			repo := &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: "catalog"}
			require.NoError(t, peer.CreateRepositoryInActiveNamespace(ctx, repo))
			other := &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: "other"}
			require.NoError(t, peer.CreateRepositoryInActiveNamespace(ctx, other))
			file := &datastore.File{UID: newID(), Namespace: ns.Name, Name: "asset",
				RepositoryID: repo.UID, ResourceVersion: "1",
				CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
				Spec:              json.RawMessage(`{"contentType":"text/plain"}`), Body: strings.Repeat("before", 1024)}
			require.NoError(t, peer.CreateFile(ctx, file))
			uid, err := gocql.ParseUUID(file.UID)
			require.NoError(t, err)
			otherUID, err := gocql.ParseUUID(other.UID)
			require.NoError(t, err)
			var mu sync.Mutex
			var reads []string
			var membership []any
			var membershipNames []string
			var locatorReads, writes atomic.Int64
			var injected sync.Once
			cfg := testScyllaConfig(t)
			cfg.QueryObserver = namespaceReservationObserver(func(queryCtx context.Context, query gocql.ObservedQuery) {
				if queryCtx.Value(updateKey{}) != true || query.Err != nil {
					return
				}
				if strings.HasPrefix(query.Statement, "SELECT ") {
					if strings.Contains(query.Statement, " FROM files_by_uid WHERE ") {
						locatorReads.Add(1)
					}
					if strings.Contains(query.Statement, " FROM files_by_namespace WHERE ") {
						mu.Lock()
						reads = append(reads, query.Statement)
						mu.Unlock()
					}
				}
				if strings.HasPrefix(query.Statement, "INSERT INTO resources_by_repository ") {
					mu.Lock()
					membership = append([]any(nil), query.Values...)
					membershipNames = append(membershipNames, query.Values[5].(string))
					mu.Unlock()
				}
				if !strings.HasPrefix(query.Statement, "UPDATE files_by_namespace SET ") {
					return
				}
				writes.Add(1)
				injected.Do(func() {
					switch transition {
					case "renamed":
						require.NoError(t, raw.Query("UPDATE files_by_namespace SET name=? WHERE namespace=? AND creation_timestamp=? AND uid=?",
							"peer-name", ns.Name, file.CreationTimestamp, uid).Exec())
					case "deleted":
						require.NoError(t, raw.Query("DELETE FROM files_by_namespace WHERE namespace=? AND creation_timestamp=? AND uid=?",
							ns.Name, file.CreationTimestamp, uid).Exec())
					case "rebound":
						mu.Lock()
						values := append([]any(nil), membership...)
						mu.Unlock()
						require.Len(t, values, 7)
						values[0] = otherUID
						require.NoError(t, raw.Query("INSERT INTO resources_by_repository (repository_id,shard,kind,uid,namespace,name,creation_timestamp) VALUES (?,?,?,?,?,?,?)",
							values...).Exec())
						require.NoError(t, raw.Query("UPDATE files_by_namespace SET repository_id=? WHERE namespace=? AND creation_timestamp=? AND uid=?",
							otherUID, ns.Name, file.CreationTimestamp, uid).Exec())
					case "canceled":
						cancel()
					}
				})
			})
			store, err := scylla.New(cfg, zap.NewNop())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Close()) })
			desired := *file
			desired.ResourceVersion, desired.Body = "2", strings.Repeat("after", 1024)
			desired.CreationTimestamp = file.CreationTimestamp.Add(time.Hour)
			expected := file.ResourceVersion
			if transition == "stale" {
				expected = "stale"
			}
			err = store.UpdateFile(context.WithValue(ctx, updateKey{}, true), &desired, expected)
			switch transition {
			case "stale":
				require.ErrorIs(t, err, datastore.ErrConflict)
				require.Zero(t, writes.Load())
			case "canceled":
				require.ErrorIs(t, err, context.Canceled)
			default:
				require.NoError(t, err)
				require.EqualValues(t, 1, writes.Load())
			}
			require.EqualValues(t, 1, locatorReads.Load(), "the first fresh row still requires the authoritative UID locator")
			if transition != "canceled" {
				mu.Lock()
				queries, names := append([]string(nil), reads...), append([]string(nil), membershipNames...)
				mu.Unlock()
				require.Len(t, queries, 2, "readback remains a fresh authoritative query, not a cached projection")
				require.Contains(t, queries[0], "body", "the update and ambiguity checks still receive the full initial envelope")
				require.Equal(t, "SELECT namespace, creation_timestamp, uid, name, repository_id FROM files_by_namespace WHERE namespace=? AND creation_timestamp=? AND uid=?",
					queries[1], "post-write membership repair must not reload the File payload")
				if transition == "renamed" {
					require.Equal(t, []string{file.Name, "peer-name"}, names)
				}
			}
			current, readErr := peer.GetFile(t.Context(), file.UID)
			if transition == "deleted" {
				require.ErrorIs(t, readErr, datastore.ErrNotFound)
			} else {
				require.NoError(t, readErr)
				require.Equal(t, file.CreationTimestamp, current.CreationTimestamp)
				if transition == "stale" {
					require.Equal(t, file.ResourceVersion, current.ResourceVersion)
					require.Equal(t, file.Body, current.Body)
				} else {
					require.Equal(t, desired.ResourceVersion, current.ResourceVersion)
					require.Equal(t, desired.Body, current.Body)
				}
				if transition == "renamed" {
					require.Equal(t, "peer-name", current.Name)
				}
				if transition == "rebound" {
					require.Equal(t, other.UID, current.RepositoryID)
					blocked, err := peer.HasCatalogResources(t.Context(), other.UID)
					require.NoError(t, err)
					require.True(t, blocked, "readback must retain the peer's new membership")
				}
			}
			blocked, err := peer.HasCatalogResources(t.Context(), repo.UID)
			require.NoError(t, err)
			require.Equal(t, transition != "deleted" && transition != "rebound", blocked)
		})
	}
}

func TestScylla_FileMutationSnapshotsAreFreshSingleUseAndBounded(t *testing.T) {
	type snapshotBatcher interface {
		WithFileMutationBatchSnapshots(context.Context, string, string, []string, []string, func(context.Context) error) error
	}
	for _, transition := range []string{"success", "stale", "missing", "repeated", "nested", "escaped", "canceled", "corrupt", "bounds"} {
		t.Run(transition, func(t *testing.T) {
			fixtureStarted := time.Now()
			type batchKey struct{}
			var cancel context.CancelFunc
			var batchLocators, batchRows, pointLocators, narrowRows atomic.Int64
			cfg := testScyllaConfig(t)
			cfg.QueryObserver = namespaceReservationObserver(func(queryCtx context.Context, query gocql.ObservedQuery) {
				if queryCtx.Value(batchKey{}) != true || query.Err != nil {
					return
				}
				switch {
				case strings.Contains(query.Statement, " FROM files_by_uid WHERE uid IN ("):
					batchLocators.Add(1)
					require.LessOrEqual(t, len(query.Values), 3)
					if transition == "canceled" {
						cancel()
					}
				case strings.Contains(query.Statement, " FROM files_by_uid WHERE uid="):
					pointLocators.Add(1)
				case strings.HasPrefix(query.Statement, "SELECT namespace, creation_timestamp, uid, name, repository_id FROM files_by_namespace "):
					narrowRows.Add(int64((len(query.Values) - 1) / 2))
				case strings.Contains(query.Statement, " FROM files_by_namespace WHERE namespace=? AND (creation_timestamp,uid) IN ("):
					batchRows.Add(1)
					require.Contains(t, query.Statement, "body")
					require.LessOrEqual(t, len(query.Values), 7)
				}
			})
			store, err := scylla.New(cfg, zap.NewNop())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Close()) })
			peer := newTestStore(t)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			ns := &datastore.Namespace{UID: newID(), Name: "file-in-fence-" + newID()[:8]}
			require.NoError(t, peer.CreateNamespace(ctx, ns))
			repo := &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: "catalog"}
			require.NoError(t, peer.CreateRepositoryInActiveNamespace(ctx, repo))
			files := make([]*datastore.File, 3)
			names, uids := make([]string, len(files)), make([]string, len(files))
			for i := range files {
				file := &datastore.File{UID: newID(), Namespace: ns.Name, Name: fmt.Sprintf("asset-%d", i),
					RepositoryID: repo.UID, ResourceVersion: "1", Body: strings.Repeat("before", 1024),
					CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond)}
				require.NoError(t, peer.CreateFile(ctx, file))
				files[i], names[i], uids[i] = file, file.Name, file.UID
			}
			// Change the authoritative version after the caller obtained its facts.
			fresh := *files[0]
			fresh.ResourceVersion, fresh.Body = "2", "peer update"
			require.NoError(t, peer.UpdateFile(ctx, &fresh, "1"))
			if transition == "missing" {
				raw := newRawSession(t)
				uid, err := gocql.ParseUUID(files[0].UID)
				require.NoError(t, err)
				require.NoError(t, raw.Query("DELETE FROM files_by_namespace WHERE namespace=? AND creation_timestamp=? AND uid=?",
					ns.Name, files[0].CreationTimestamp, uid).Exec())
			}
			if transition == "corrupt" {
				raw := newRawSession(t)
				uid, err := gocql.ParseUUID(files[0].UID)
				require.NoError(t, err)
				require.NoError(t, raw.Query("UPDATE files_by_uid SET namespace=? WHERE uid=?", "", uid).Exec())
			}
			t.Logf("File snapshot fixture %s prepared in %s; caller context error: %v", transition, time.Since(fixtureStarted), ctx.Err())
			batcher, ok := store.(snapshotBatcher)
			require.True(t, ok, "File updates need bounded fresh UID snapshots inside the reservation")
			if transition == "bounds" {
				for _, ids := range [][]string{append(append([]string(nil), uids...), newID()), append(append([]string(nil), uids...), newID(), newID())} {
					err := batcher.WithFileMutationBatchSnapshots(context.WithValue(ctx, batchKey{}, true), ns.Name, repo.UID, names, ids,
						func(context.Context) error { t.Error("invalid batch invoked its callback"); return nil })
					require.ErrorIs(t, err, datastore.ErrInvalidArgument)
				}
				require.Zero(t, batchLocators.Load())
				require.Zero(t, pointLocators.Load())
				return
			}
			var escaped context.Context
			var results [3]error
			err = batcher.WithFileMutationBatchSnapshots(context.WithValue(ctx, batchKey{}, true), ns.Name, repo.UID, names, uids,
				func(batchCtx context.Context) error {
					escaped = batchCtx
					if transition == "nested" {
						require.NoError(t, batcher.WithFileMutationBatchSnapshots(batchCtx, ns.Name, repo.UID, names[:2], uids[:2],
							func(nestedCtx context.Context) error {
								desired := *files[0]
								desired.ResourceVersion = "3"
								return store.UpdateFile(nestedCtx, &desired, "2")
							}))
					}
					var updates sync.WaitGroup
					for i, file := range files {
						if i == 0 && transition == "escaped" {
							continue
						}
						updates.Go(func() {
							desired := *file
							expected := "1"
							desired.ResourceVersion = "2"
							if i == 0 && transition != "stale" {
								expected = "2"
								desired.ResourceVersion = "3"
							}
							if i == 0 && transition == "nested" {
								expected, desired.ResourceVersion = "3", "4"
							}
							desired.Body = "desired"
							desired.CreationTimestamp = file.CreationTimestamp.Add(time.Hour)
							results[i] = store.UpdateFile(batchCtx, &desired, expected)
						})
					}
					updates.Wait()
					if transition == "repeated" {
						desired := *files[0]
						desired.ResourceVersion, desired.Body = "4", "second update"
						require.NoError(t, store.UpdateFile(batchCtx, &desired, "3"),
							"a consumed initial snapshot must never be reused for another update")
					}
					return errors.Join(results[:]...)
				})
			if transition == "canceled" {
				require.ErrorIs(t, err, context.Canceled)
				for _, file := range files[1:] {
					current, err := peer.GetFile(t.Context(), file.UID)
					require.NoError(t, err)
					require.Equal(t, "1", current.ResourceVersion)
				}
				return
			}
			if transition == "corrupt" {
				require.ErrorIs(t, err, datastore.ErrRepairRequired)
				require.EqualValues(t, 1, batchLocators.Load())
				require.Zero(t, batchRows.Load())
				require.Zero(t, pointLocators.Load(), "failed snapshot reads cannot silently fall back to point reads")
				require.Zero(t, narrowRows.Load())
				for i, file := range files {
					require.ErrorIs(t, results[i], datastore.ErrRepairRequired)
					current, err := peer.GetFileByName(t.Context(), ns.Name, file.Name)
					require.NoError(t, err)
					want := "1"
					if i == 0 {
						want = "2"
					}
					require.Equal(t, want, current.ResourceVersion)
				}
				return
			}
			switch transition {
			case "stale":
				require.ErrorIs(t, err, datastore.ErrConflict)
			case "missing":
				require.ErrorIs(t, err, datastore.ErrNotFound)
			default:
				require.NoError(t, err)
			}
			require.NoError(t, results[1])
			require.NoError(t, results[2])
			wantBatches := 1
			if transition == "nested" {
				wantBatches = 2
			}
			require.EqualValues(t, wantBatches, batchLocators.Load())
			require.EqualValues(t, wantBatches, batchRows.Load())
			if transition != "missing" {
				wantReadbacks, wantPoints := 3, 0
				if transition == "repeated" || transition == "nested" {
					wantReadbacks, wantPoints = 4, 1
				} else if transition == "escaped" {
					wantReadbacks = 2
				}
				require.EqualValues(t, wantReadbacks, narrowRows.Load(),
					"each attempted update retains a fresh authoritative key read, including coalesced queries")
				require.EqualValues(t, wantPoints, pointLocators.Load())
			}
			for i, file := range files {
				current, err := peer.GetFile(t.Context(), file.UID)
				if i == 0 && transition == "missing" {
					require.ErrorIs(t, err, datastore.ErrNotFound)
					continue
				}
				require.NoError(t, err)
				require.Equal(t, file.CreationTimestamp, current.CreationTimestamp)
				want := "2"
				if i == 0 {
					switch transition {
					case "success":
						want = "3"
					case "repeated", "nested":
						want = "4"
					}
				}
				require.Equal(t, want, current.ResourceVersion)
			}
			if transition == "escaped" {
				desired := *files[0]
				desired.ResourceVersion = "3"
				require.NoError(t, peer.UpdateFile(t.Context(), &desired, "2"))
				desired.ResourceVersion = "4"
				require.NoError(t, store.UpdateFile(escaped, &desired, "3"), "closed reservations invalidate their unused snapshots")
				require.EqualValues(t, 1, pointLocators.Load())
			}
		})
	}
}

func TestScylla_DisjointFileCohortSharesFreshSnapshotReads(t *testing.T) {
	type snapshotKey struct{}
	peer := newTestStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	ns := &datastore.Namespace{UID: newID(), Name: "cohort-snapshots-" + newID()[:8]}
	require.NoError(t, peer.CreateNamespace(ctx, ns))
	repo := &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: "catalog"}
	require.NoError(t, peer.CreateRepositoryInActiveNamespace(ctx, repo))
	var files [4][3]*datastore.File
	for i := range files {
		for j := range files[i] {
			file := &datastore.File{UID: newID(), Namespace: ns.Name, Name: fmt.Sprintf("asset-%d-%d", i, j),
				RepositoryID: repo.UID, ResourceVersion: "1", CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond)}
			require.NoError(t, peer.CreateFile(ctx, file))
			files[i][j] = file
			fresh := *file
			fresh.ResourceVersion, fresh.Body = "2", "fresh peer facts"
			require.NoError(t, peer.UpdateFile(ctx, &fresh, "1"))
		}
	}
	var locators, authoritative, points, parents atomic.Int64
	parentObserved, snapshotObserved := make(chan struct{}), make(chan struct{})
	var observedParent, observedSnapshot sync.Once
	cfg := testScyllaConfig(t)
	cfg.QueryObserver = namespaceReservationObserver(func(queryCtx context.Context, query gocql.ObservedQuery) {
		if queryCtx.Value(snapshotKey{}) != true || query.Err != nil {
			return
		}
		switch {
		case strings.Contains(query.Statement, " FROM repositories_by_uid WHERE uid="):
			parents.Add(1)
			observedParent.Do(func() { close(parentObserved) })
			select {
			case <-snapshotObserved:
			case <-ctx.Done():
				assert.Fail(t, "fresh snapshot did not overlap the parent lookup")
			}
		case strings.Contains(query.Statement, " FROM files_by_uid WHERE uid IN ("):
			locators.Add(1)
			assert.LessOrEqual(t, len(query.Values), 16)
		case strings.Contains(query.Statement, " FROM files_by_namespace WHERE namespace=? AND (creation_timestamp,uid) IN (") &&
			strings.Contains(query.Statement, "body"):
			authoritative.Add(1)
			assert.Contains(t, query.Statement, "body")
			assert.LessOrEqual(t, len(query.Values), 33)
			observedSnapshot.Do(func() { close(snapshotObserved) })
			select {
			case <-parentObserved:
			case <-ctx.Done():
				assert.Fail(t, "parent lookup did not overlap the fresh snapshot")
			}
		case strings.Contains(query.Statement, " FROM files_by_uid WHERE uid="):
			points.Add(1)
		}
	})
	store, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	started, resume := make(chan struct{}), make(chan struct{})
	var released sync.Once
	release := func() { released.Do(func() { close(resume) }) }
	var workers sync.WaitGroup
	t.Cleanup(func() { release(); workers.Wait() })
	initial := make(chan error, 1)
	workers.Go(func() {
		initial <- store.(datastore.CatalogMutationBatcher).WithCatalogMutationBatch(ctx, ns.Name, func(context.Context) error {
			close(started)
			<-resume
			return nil
		})
	})
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("initial reservation did not start")
	}
	results := make([]chan error, len(files))
	for i := range files {
		results[i] = make(chan error, 1)
		workers.Go(func() {
			names, uids := make([]string, len(files[i])), make([]string, len(files[i]))
			for j, file := range files[i] {
				names[j], uids[j] = file.Name, file.UID
			}
			results[i] <- store.(datastore.FileMutationSnapshotBatcher).WithFileMutationBatchSnapshots(
				context.WithValue(ctx, snapshotKey{}, true), ns.Name, repo.UID, names, uids,
				func(batchCtx context.Context) error {
					for _, file := range files[i] {
						desired := *file
						desired.ResourceVersion, desired.Body = "3", "cohort update"
						if err := store.UpdateFile(batchCtx, &desired, "2"); err != nil {
							return err
						}
					}
					return nil
				})
		})
		require.Eventually(t, func() bool {
			return scylla.QueuedNamespaceCatalogWritesForTest(store, ns.UID) == i+2
		}, time.Second, time.Millisecond)
	}
	release()
	require.NoError(t, <-initial)
	for _, result := range results {
		require.NoError(t, <-result)
	}
	require.EqualValues(t, 1, locators.Load(), "already-selected disjoint callbacks must share one bounded locator read")
	require.EqualValues(t, 1, authoritative.Load(), "already-selected disjoint callbacks must share one fresh authoritative read")
	require.EqualValues(t, 1, parents.Load(), "successful parent validation remains shared by the held reservation")
	require.Zero(t, points.Load(), "each update must consume its own fresh in-reservation snapshot")
	for _, group := range files {
		for _, file := range group {
			current, err := peer.GetFile(ctx, file.UID)
			require.NoError(t, err)
			require.Equal(t, "3", current.ResourceVersion)
			require.Equal(t, "cohort update", current.Body)
		}
	}
	require.Zero(t, scylla.QueuedNamespaceCatalogWritesForTest(store, ns.UID))
}

func TestScylla_FileCohortCoalescesFreshProjectionReads(t *testing.T) {
	type waveKey struct{}
	peer := newTestStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	ns := &datastore.Namespace{UID: newID(), Name: "projection-reads-" + newID()[:8]}
	require.NoError(t, peer.CreateNamespace(ctx, ns))
	repositories := []*datastore.Repository{
		{UID: newID(), Namespace: ns.Name, Name: "before"},
		{UID: newID(), Namespace: ns.Name, Name: "after"},
	}
	for _, repository := range repositories {
		require.NoError(t, peer.CreateRepositoryInActiveNamespace(ctx, repository))
	}
	files := make([]*datastore.File, 4)
	names, uids := make([]string, len(files)), make([]string, len(files))
	for i := range files {
		files[i] = &datastore.File{UID: newID(), Namespace: ns.Name, Name: fmt.Sprintf("asset-%d", i),
			RepositoryID: repositories[0].UID, ResourceVersion: "1"}
		require.NoError(t, peer.CreateFile(ctx, files[i]))
		names[i], uids[i] = files[i].Name, files[i].UID
	}
	var points, batches [2]atomic.Int64
	started := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
	resume := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
	var released [2]sync.Once
	release := func(i int) { released[i].Do(func() { close(resume[i]) }) }
	t.Cleanup(func() { release(0); release(1) })
	cfg := testScyllaConfig(t)
	cfg.QueryObserver = namespaceReservationObserver(func(queryCtx context.Context, query gocql.ObservedQuery) {
		wave, _ := queryCtx.Value(waveKey{}).(int)
		if wave < 1 || wave > 2 || !strings.HasPrefix(query.Statement, "SELECT namespace, creation_timestamp, uid, name, repository_id FROM files_by_namespace ") {
			return
		}
		i := wave - 1
		if strings.Contains(query.Statement, "(creation_timestamp,uid) IN (") {
			batches[i].Add(1)
			assert.Len(t, query.Values, 7, "one namespace and three exact clustering keys")
			assert.NotContains(t, query.Statement, "body")
			assert.NotContains(t, query.Statement, "ALLOW FILTERING")
		} else if points[i].Add(1) == 1 {
			close(started[i])
			select {
			case <-resume[i]:
			case <-queryCtx.Done():
			}
		}
	})
	store, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	registry := prometheus.NewRegistry()
	store = datastore.NewInstrumentedDatastoreWithRegistry(store, "scylla", zap.NewNop(), registry)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	err = store.(datastore.FileMutationSnapshotBatcher).WithFileMutationBatchSnapshots(
		ctx, ns.Name, repositories[0].UID, names, uids, func(batchCtx context.Context) error {
			for wave := range 2 {
				waveCtx := context.WithValue(batchCtx, waveKey{}, wave+1)
				results := make(chan error, len(files))
				var workers sync.WaitGroup
				for _, file := range files {
					workers.Go(func() {
						desired := *file
						desired.ResourceVersion = fmt.Sprint(wave + 2)
						desired.RepositoryID = repositories[wave].UID
						results <- store.UpdateFile(waveCtx, &desired, fmt.Sprint(wave+1))
					})
				}
				select {
				case <-started[wave]:
				case <-ctx.Done():
					release(wave)
					workers.Wait()
					return ctx.Err()
				}
				ready := assert.Eventually(t, func() bool {
					return scylla.QueuedFileProjectionReadsForTest(waveCtx) == len(files)-1
				}, 5*time.Second, time.Millisecond, "all remaining fresh reads must queue behind physical work")
				release(wave)
				workers.Wait()
				close(results)
				var errs []error
				for err := range results {
					errs = append(errs, err)
				}
				if err := errors.Join(errs...); err != nil {
					return err
				}
				if !ready {
					return errors.New("fresh projection reads did not coalesce")
				}
				require.EqualValues(t, 1, points[wave].Load())
				require.EqualValues(t, 1, batches[wave].Load())
				for _, file := range files {
					current, err := peer.GetFile(ctx, file.UID)
					require.NoError(t, err)
					require.Equal(t, fmt.Sprint(wave+2), current.ResourceVersion)
					require.Equal(t, repositories[wave].UID, current.RepositoryID)
				}
			}
			return nil
		})
	require.NoError(t, err)
	counts := datastorePhaseCounts(t, registry)
	require.EqualValues(t, 8, counts["FileProjectionReadQueue"])
	require.EqualValues(t, 2, counts["FileProjectionReadBatch1"])
	require.EqualValues(t, 2, counts["FileProjectionReadBatch3"])
	for size := 2; size <= 16; size++ {
		if size != 3 {
			require.Zero(t, counts[fmt.Sprintf("FileProjectionReadBatch%d", size)])
		}
	}
}

func TestScylla_FileSnapshotsOverlapBoundedLocatorBatches(t *testing.T) {
	type readKey struct{}
	firstWave, release := make(chan struct{}), make(chan struct{})
	canceledRead := make(chan struct{})
	var cancelOnce sync.Once
	var locators, authoritative, active, peak atomic.Int64
	var invalidQuery atomic.Bool
	cfg := testScyllaConfig(t)
	cfg.QueryObserver = namespaceReservationObserver(func(ctx context.Context, query gocql.ObservedQuery) {
		mode, _ := ctx.Value(readKey{}).(string)
		if mode == "" {
			return
		}
		if strings.Contains(query.Statement, " FROM files_by_namespace WHERE namespace=? AND (creation_timestamp,uid) IN (") {
			if mode == "cancel" || active.Load() != 0 || locators.Load() != 16 {
				invalidQuery.Store(true)
			}
			authoritative.Add(1)
			return
		}
		if !strings.Contains(query.Statement, " FROM files_by_uid WHERE uid IN (") ||
			len(query.Values) == 0 || len(query.Values) > 16 || strings.Contains(query.Statement, "ALLOW FILTERING") {
			invalidQuery.Store(true)
			return
		}
		current := active.Add(1)
		defer active.Add(-1)
		for previous := peak.Load(); current > previous; previous = peak.Load() {
			if peak.CompareAndSwap(previous, current) {
				break
			}
		}
		if mode == "cancel" {
			cancelOnce.Do(func() { close(canceledRead) })
			<-ctx.Done()
			return
		}
		if query.Err != nil {
			invalidQuery.Store(true)
		}
		if locators.Add(1) == 8 {
			close(firstWave)
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
	})
	store, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	uids := make([]string, datastore.FileSnapshotBatchLimit)
	for i := range uids {
		uids[i] = newID()
	}
	for _, index := range []int{0, len(uids) - 1} {
		namespace := "snapshot-overlap-" + newID()[:8]
		require.NoError(t, store.CreateNamespace(t.Context(), &datastore.Namespace{UID: newID(), Name: namespace}))
		require.NoError(t, store.CreateFile(t.Context(), &datastore.File{
			UID: uids[index], Namespace: namespace, Name: "file", ResourceVersion: "1",
			CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
			Annotations:       map[string]string{"sequence": "1"},
		}))
	}
	uids[17], uids[18] = uids[0], "invalid"
	type readResult struct {
		files []*datastore.File
		err   error
	}
	completed := make(chan readResult, 1)
	ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), readKey{}, "overlap"), 5*time.Second)
	defer cancel()
	go func() {
		files, err := store.GetFileSnapshots(ctx, uids)
		completed <- readResult{files, err}
	}()
	select {
	case <-firstWave:
	case <-ctx.Done():
	}
	close(release)
	result := <-completed
	require.NoError(t, result.err, "eight bounded locator reads must overlap before the first wave is released")
	require.EqualValues(t, 8, peak.Load())
	require.EqualValues(t, 16, locators.Load(), "deduplicate IDs without changing the sixteen-key query bound")
	require.EqualValues(t, 2, authoritative.Load(), "fetch each exact Namespace tuple group after every locator read finishes")
	require.False(t, invalidQuery.Load())
	require.Len(t, result.files, len(uids))
	for i, file := range result.files {
		if i == 0 || i == 17 || i == len(uids)-1 {
			require.NotNil(t, file)
			require.Equal(t, uids[i], file.UID)
		} else {
			require.Nil(t, file)
		}
	}
	result.files[0].Annotations["sequence"] = "mutated"
	require.Equal(t, "1", result.files[17].Annotations["sequence"])

	ctx, cancelRead := context.WithCancel(context.WithValue(t.Context(), readKey{}, "cancel"))
	defer cancelRead()
	go func() {
		files, err := store.GetFileSnapshots(ctx, uids)
		completed <- readResult{files, err}
	}()
	select {
	case <-canceledRead:
	case <-time.After(5 * time.Second):
		cancelRead()
		<-completed
		t.Fatal("locator query did not reach the cancellation barrier")
	}
	cancelRead()
	result = <-completed
	require.ErrorIs(t, result.err, context.Canceled)
	require.Nil(t, result.files, "cancellation must not expose partial snapshots")
	require.Zero(t, active.Load(), "join every locator reader before returning")
	require.EqualValues(t, 8, peak.Load())
	require.EqualValues(t, 2, authoritative.Load(), "cancellation must not start authoritative reads")
	require.False(t, invalidQuery.Load())
}

func TestScylla_FileSnapshotsOverlapBoundedAuthoritativeBatches(t *testing.T) {
	type readKey struct{}
	firstWave, release, canceledRead := make(chan struct{}), make(chan struct{}), make(chan struct{})
	partialFullWave, partialReady, partialRelease := make(chan struct{}), make(chan struct{}), make(chan struct{})
	mixedReady, mixedRelease := make(chan struct{}), make(chan struct{})
	var cancelOnce sync.Once
	var locators, authoritative, active, peak atomic.Int64
	var partialFullReads, partialReads atomic.Int64
	var mixedReads atomic.Int64
	var invalidQuery atomic.Bool
	cfg := testScyllaConfig(t)
	cfg.QueryObserver = namespaceReservationObserver(func(ctx context.Context, query gocql.ObservedQuery) {
		mode, _ := ctx.Value(readKey{}).(string)
		if mode == "" {
			return
		}
		if strings.Contains(query.Statement, " FROM files_by_uid WHERE uid IN (") {
			if len(query.Values) == 0 || len(query.Values) > 16 ||
				(query.Err != nil && (mode != "cancel" || ctx.Err() == nil)) {
				invalidQuery.Store(true)
			}
			locators.Add(1)
			return
		}
		if !strings.Contains(query.Statement, " FROM files_by_namespace WHERE namespace=? AND (creation_timestamp,uid) IN (") ||
			len(query.Values) < 3 || len(query.Values) > 33 || (len(query.Values)-1)%2 != 0 ||
			strings.Contains(query.Statement, "ALLOW FILTERING") {
			invalidQuery.Store(true)
		}
		current := active.Add(1)
		defer active.Add(-1)
		for previous := peak.Load(); current > previous; previous = peak.Load() {
			if peak.CompareAndSwap(previous, current) {
				break
			}
		}
		if mode == "cancel" {
			cancelOnce.Do(func() { close(canceledRead) })
			<-ctx.Done()
			return
		}
		if query.Err != nil {
			invalidQuery.Store(true)
		}
		if mode == "mixed" {
			if len(query.Values) != 17 {
				invalidQuery.Store(true)
			}
			if mixedReads.Add(1) == 2 {
				close(mixedReady)
			}
			select {
			case <-mixedRelease:
			case <-ctx.Done():
			}
			return
		}
		if mode == "partial" {
			if len(query.Values) == 5 {
				if partialReads.Add(1) == 1 {
					close(partialReady)
				}
			} else {
				if len(query.Values) != 33 {
					invalidQuery.Store(true)
				}
				if partialFullReads.Add(1) == 3 {
					close(partialFullWave)
				}
				select {
				case <-partialRelease:
				case <-ctx.Done():
				}
			}
			return
		}
		if authoritative.Add(1) == 8 {
			close(firstWave)
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
	})
	store, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	namespaces := []string{"snapshot-rows-" + newID()[:8], "snapshot-rows-" + newID()[:8]}
	for _, namespace := range namespaces {
		require.NoError(t, store.CreateNamespace(t.Context(), &datastore.Namespace{UID: newID(), Name: namespace}))
	}
	uids := make([]string, datastore.FileSnapshotBatchLimit)
	for i := range uids {
		uids[i] = newID()
		namespace := namespaces[0]
		if i >= 200 {
			namespace = namespaces[1]
		}
		require.NoError(t, store.CreateFile(t.Context(), &datastore.File{
			UID: uids[i], Namespace: namespace, Name: fmt.Sprintf("file-%d", i), ResourceVersion: "1",
			CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
			Annotations:       map[string]string{"sequence": "1"},
		}))
	}
	uids[17], uids[18] = uids[0], "invalid"
	type readResult struct {
		files []*datastore.File
		err   error
	}
	completed := make(chan readResult, 1)
	ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), readKey{}, "overlap"), 5*time.Second)
	defer cancel()
	go func() {
		files, err := store.GetFileSnapshots(ctx, uids)
		completed <- readResult{files, err}
	}()
	select {
	case <-firstWave:
	case <-ctx.Done():
	}
	locatorsAtFirstAuthoritativeWave := locators.Load()
	close(release)
	result := <-completed
	require.NoError(t, result.err, "eight bounded authoritative reads must overlap across exact Namespace tuple batches")
	require.EqualValues(t, 8, locatorsAtFirstAuthoritativeWave,
		"full single-Namespace batches must start fresh reads without waiting for unrelated locator batches")
	require.EqualValues(t, 8, peak.Load(), "the reader ceiling is global across Namespace groups")
	require.EqualValues(t, 16, locators.Load())
	require.EqualValues(t, 17, authoritative.Load(), "198 and 50 exact tuple keys need thirteen and four bounded reads")
	require.False(t, invalidQuery.Load(), "each authoritative query must retain the sixteen-key bound")
	require.Len(t, result.files, len(uids))
	for i, file := range result.files {
		if i == 18 {
			require.Nil(t, file)
		} else {
			require.NotNil(t, file)
			require.Equal(t, uids[i], file.UID)
		}
	}
	result.files[0].Annotations["sequence"] = "changed"
	require.Equal(t, "1", result.files[17].Annotations["sequence"])

	partialCtx, cancelPartial := context.WithTimeout(context.WithValue(t.Context(), readKey{}, "partial"), 5*time.Second)
	defer cancelPartial()
	go func() {
		files, err := store.GetFileSnapshots(partialCtx, uids[20:70])
		completed <- readResult{files, err}
	}()
	select {
	case <-partialFullWave:
	case <-partialCtx.Done():
	}
	partialStarted := false
	select {
	case <-partialReady:
		partialStarted = true
	case <-time.After(time.Second):
	}
	close(partialRelease)
	result = <-completed
	require.NoError(t, result.err)
	require.True(t, partialStarted, "partial reads must not wait for unrelated authoritative batches after all locators finish")
	require.EqualValues(t, 3, partialFullReads.Load())
	require.EqualValues(t, 1, partialReads.Load(), "fifty same-Namespace keys retain four authoritative queries")
	require.Len(t, result.files, 50)
	for i, file := range result.files {
		require.NotNil(t, file)
		require.Equal(t, uids[20+i], file.UID)
	}
	require.Zero(t, active.Load())

	mixedUIDs := append([]string{}, uids[20:28]...)
	mixedUIDs = append(mixedUIDs, uids[200:208]...)
	mixedCtx, cancelMixed := context.WithTimeout(context.WithValue(t.Context(), readKey{}, "mixed"), 5*time.Second)
	defer cancelMixed()
	go func() {
		files, err := store.GetFileSnapshots(mixedCtx, mixedUIDs)
		completed <- readResult{files, err}
	}()
	mixedStarted := false
	select {
	case <-mixedReady:
		mixedStarted = true
	case <-time.After(time.Second):
	}
	close(mixedRelease)
	result = <-completed
	require.NoError(t, result.err)
	require.True(t, mixedStarted, "one locator batch must not serialize independent mixed-Namespace authoritative reads")
	require.EqualValues(t, 2, mixedReads.Load())
	require.Len(t, result.files, len(mixedUIDs))
	for i, file := range result.files {
		require.NotNil(t, file)
		require.Equal(t, mixedUIDs[i], file.UID)
	}
	require.Zero(t, active.Load())

	peer := newTestStore(t)
	file, err := peer.GetFile(t.Context(), uids[0])
	require.NoError(t, err)
	file.ResourceVersion, file.Body = "2", "fresh"
	require.NoError(t, peer.UpdateFile(t.Context(), file, "1"))
	fresh, err := store.GetFileSnapshots(t.Context(), uids)
	require.NoError(t, err)
	require.Equal(t, "2", fresh[0].ResourceVersion)
	require.Equal(t, "fresh", fresh[17].Body)

	ctx, cancelRead := context.WithCancel(context.WithValue(t.Context(), readKey{}, "cancel"))
	defer cancelRead()
	go func() {
		files, err := store.GetFileSnapshots(ctx, uids)
		completed <- readResult{files, err}
	}()
	select {
	case <-canceledRead:
	case <-time.After(5 * time.Second):
		cancelRead()
		<-completed
		t.Fatal("authoritative query did not reach the cancellation barrier")
	}
	cancelRead()
	result = <-completed
	require.ErrorIs(t, result.err, context.Canceled)
	require.Nil(t, result.files, "cancellation must not expose partial authoritative snapshots")
	require.Zero(t, active.Load(), "every authoritative reader must finish before returning")
	require.EqualValues(t, 8, peak.Load())
	require.False(t, invalidQuery.Load())
}

func TestScylla_FileNameSnapshotsUseTwoPartitionReadsAndFreshAuthoritativeFacts(t *testing.T) {
	type readKey struct{}
	var queries atomic.Int64
	var invalidQuery atomic.Bool
	cfg := testScyllaConfig(t)
	cfg.QueryObserver = namespaceReservationObserver(func(ctx context.Context, query gocql.ObservedQuery) {
		if ctx.Value(readKey{}) == true && strings.HasPrefix(query.Statement, "SELECT ") {
			if query.Err != nil || strings.Contains(query.Statement, "ALLOW FILTERING") ||
				!(strings.Contains(query.Statement, " FROM files_by_name WHERE namespace=? AND name IN (") ||
					strings.Contains(query.Statement, " FROM files_by_namespace WHERE namespace=? AND (creation_timestamp,uid) IN (")) {
				invalidQuery.Store(true)
			}
			queries.Add(1)
		}
	})
	first, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, first.Close()) })
	second := newTestStore(t)
	namespace := "file-name-snapshots-" + newID()[:8]
	require.NoError(t, first.CreateNamespace(t.Context(), &datastore.Namespace{UID: newID(), Name: namespace}))
	files := make([]*datastore.File, datastore.FileNameSnapshotBatchLimit)
	names := make([]string, len(files))
	for i := range files {
		names[i] = fmt.Sprintf("file-%d", i)
		files[i] = &datastore.File{UID: newID(), Namespace: namespace, Name: names[i],
			ResourceVersion: "1", CreationActor: "alice", CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
			Annotations: map[string]string{"sequence": "1"}, Spec: json.RawMessage(`{"contentType":"image/jpeg"}`)}
		require.NoError(t, first.CreateFile(t.Context(), files[i]))
	}
	ctx := context.WithValue(t.Context(), readKey{}, true)
	for _, count := range []int{2, 3, 4} {
		before := queries.Load()
		bulk, err := first.GetFileSnapshotsByName(ctx, namespace, names[:count])
		require.NoError(t, err)
		require.Len(t, bulk, count)
		require.EqualValues(t, 2, queries.Load()-before, "two sequential same-partition queries, independent of File count")
		require.False(t, invalidQuery.Load())
		for i, file := range bulk {
			point, err := second.GetFileByName(t.Context(), namespace, names[i])
			require.NoError(t, err)
			require.Equal(t, point, file)
		}
	}
	input := []string{names[3], names[0], "absent", names[0]}
	for _, reader := range []datastore.Datastore{first, second} {
		bulk, err := reader.GetFileSnapshotsByName(t.Context(), namespace, input)
		require.NoError(t, err)
		require.Len(t, bulk, len(input))
		require.Equal(t, files[3].UID, bulk[0].UID)
		require.Nil(t, bulk[2])
		bulk[1].Annotations["sequence"], bulk[1].Spec[0] = "mutated", '!'
		require.Equal(t, "1", bulk[3].Annotations["sequence"])
		require.JSONEq(t, `{"contentType":"image/jpeg"}`, string(bulk[3].Spec))
	}
	updated, err := second.GetFile(t.Context(), files[0].UID)
	require.NoError(t, err)
	updated.ResourceVersion, updated.Body = "2", "fresh"
	updated.Annotations[datastore.OwnerAnnotationKey] = "charlie"
	require.NoError(t, second.UpdateFile(t.Context(), updated, "1"))
	bulk, err := first.GetFileSnapshotsByName(t.Context(), namespace, input)
	require.NoError(t, err)
	require.Equal(t, "2", bulk[1].ResourceVersion)
	require.Equal(t, "charlie", bulk[1].EffectiveOwnerSub())
	require.Equal(t, "alice", bulk[1].CreationActor)
	require.Equal(t, "fresh", bulk[1].Body)
	require.NoError(t, second.DeleteFile(t.Context(), files[0].UID))
	replacement := *files[0]
	replacement.UID, replacement.CreationTimestamp = newID(), time.Now().UTC().Truncate(time.Millisecond)
	require.NoError(t, second.CreateFile(t.Context(), &replacement))
	bulk, err = first.GetFileSnapshotsByName(t.Context(), namespace, input)
	require.NoError(t, err)
	require.Equal(t, replacement.UID, bulk[1].UID)
	require.Equal(t, replacement.UID, bulk[3].UID)

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	bulk, err = first.GetFileSnapshotsByName(cancelled, namespace, names)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, bulk)
	for _, invalid := range []struct {
		namespace string
		names     []string
	}{{namespace, append(names, "extra")}, {"", names}, {namespace, []string{""}}} {
		bulk, err = first.GetFileSnapshotsByName(t.Context(), invalid.namespace, invalid.names)
		require.ErrorIs(t, err, datastore.ErrInvalidArgument)
		require.Nil(t, bulk)
	}
	bulk, err = first.GetFileSnapshotsByName(t.Context(), namespace, nil)
	require.NoError(t, err)
	require.Empty(t, bulk)

	raw := newRawSession(t)
	uid, err := gocql.ParseUUID(files[3].UID)
	require.NoError(t, err)
	require.NoError(t, raw.Query("DELETE FROM files_by_namespace WHERE namespace=? AND creation_timestamp=? AND uid=?",
		namespace, files[3].CreationTimestamp, uid).Exec())
	bulk, err = second.GetFileSnapshotsByName(t.Context(), namespace, input)
	require.NoError(t, err)
	require.Nil(t, bulk[0], "a retained name locator cannot substitute for an authoritative row")
	otherUID, err := gocql.ParseUUID(files[2].UID)
	require.NoError(t, err)
	require.NoError(t, raw.Query("UPDATE files_by_name SET uid=?,creation_timestamp=? WHERE namespace=? AND name=?",
		otherUID, files[2].CreationTimestamp, namespace, names[1]).Exec())
	bulk, err = first.GetFileSnapshotsByName(t.Context(), namespace, []string{names[1]})
	require.ErrorIs(t, err, datastore.ErrRepairRequired, "a wrong-name authoritative row must fail closed")
	require.Nil(t, bulk)
}

func TestScylla_FileOwnerReferenceProjectionUsesOwnerRepositoryScope(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	file := &datastore.File{
		UID: newID(), Namespace: "file-owner-" + newID()[:8], Name: "owned-" + newID()[:8],
		APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File",
		Generation: 1, ResourceVersion: "1", CreationTimestamp: time.Now().UTC(),
		RepositoryID:    newID(),
		OwnerReferences: json.RawMessage(`[{"kind":"Repository","uid":"owner","repositoryID":"00000000-0000-0000-0000-000000000003","blockOwnerDeletion":true}]`),
	}
	require.NoError(t, store.CreateNamespace(ctx, &datastore.Namespace{UID: newID(), Name: file.Namespace}))
	require.NoError(t, store.CreateRepository(ctx, &datastore.Repository{UID: file.RepositoryID, Namespace: file.Namespace, Name: "source"}))
	require.NoError(t, store.CreateFile(ctx, file))
	owners := store.(datastore.OwnerReferenceStore)
	blocked, err := owners.HasBlockingOwnerDependents(ctx, datastore.OwnerReferenceScope{Namespace: file.Namespace, RepositoryID: "00000000-0000-0000-0000-000000000003"}, "owner")
	require.NoError(t, err)
	assert.True(t, blocked)
}

// ── Product ───────────────────────────────────────────────────────────────────

func TestScylla_CreateGetProduct(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	p := newProduct("test-ns", "widget-"+newID()[:8])
	require.NoError(t, store.CreateProduct(ctx, p))

	got, err := store.GetProduct(ctx, p.UID)
	require.NoError(t, err)
	assert.Equal(t, p.UID, got.UID)
	assert.Equal(t, p.Name, got.Name)
	assert.Equal(t, p.Namespace, got.Namespace)
}

func TestScylla_CreateProduct_DuplicateUID(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	p := newProduct("test-ns", "dup-uid-"+newID()[:8])
	require.NoError(t, store.CreateProduct(ctx, p))
	err := store.CreateProduct(ctx, p)
	require.ErrorIs(t, err, datastore.ErrAlreadyExists)
}

func TestScylla_CreateProduct_DuplicateName(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	name := "dup-name-" + newID()[:8]
	p1 := newProduct("test-ns", name)
	require.NoError(t, store.CreateProduct(ctx, p1))
	p2 := newProduct("test-ns", name)
	err := store.CreateProduct(ctx, p2)
	require.ErrorIs(t, err, datastore.ErrAlreadyExists)
}

func TestScylla_GetProduct_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.GetProduct(context.Background(), newID())
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_GetProductByName(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	name := "findable-" + newID()[:8]
	p := newProduct("test-ns", name)
	require.NoError(t, store.CreateProduct(ctx, p))

	got, err := store.GetProductByName(ctx, "test-ns", name)
	require.NoError(t, err)
	assert.Equal(t, p.UID, got.UID)
}

func TestScylla_GetProductByName_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.GetProductByName(context.Background(), "test-ns", "no-such-product-"+newID()[:8])
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_ListProducts(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ns := "list-ns-" + newID()[:8]
	p1 := newProduct(ns, "p1-"+newID()[:8])
	p2 := newProduct(ns, "p2-"+newID()[:8])
	p3 := newProduct(ns, "p3-"+newID()[:8])

	require.NoError(t, store.CreateProduct(ctx, p1))
	require.NoError(t, store.CreateProduct(ctx, p2))
	require.NoError(t, store.CreateProduct(ctx, p3))

	result, err := store.ListProducts(ctx, ns, datastore.PageParams{First: 100})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(result.Items), 3)
}

func TestScylla_UpdateProductVariant_UpdatesSKUIndex(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ns := "variant-ns-" + newID()[:8]
	name := "variant-" + newID()[:8]
	oldSKU := "sku-old-" + newID()[:8]
	newSKU := "sku-new-" + newID()[:8]

	v := newProductVariant(ns, name, oldSKU, "product-a")
	v.ResourceVersion = "1"
	require.NoError(t, store.CreateProductVariant(ctx, v))

	v.SKU = newSKU
	v.Generation = 2
	v.ResourceVersion = "2"
	require.NoError(t, store.UpdateProductVariant(ctx, v, "1"))

	_, err := store.GetProductVariantBySKU(ctx, ns, oldSKU)
	require.ErrorIs(t, err, datastore.ErrNotFound)

	got, err := store.GetProductVariantBySKU(ctx, ns, newSKU)
	require.NoError(t, err)
	assert.Equal(t, name, got.Name)
	assert.Equal(t, newSKU, got.SKU)
}

func TestScylla_UpdateProduct(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	p := newProduct("test-ns", "upd-"+newID()[:8])
	require.NoError(t, store.CreateProduct(ctx, p))
	p.GitRef = "main"
	expected := p.ResourceVersion
	datastore.AdvanceProductSystemVersion(p)
	require.NoError(t, store.UpdateProduct(ctx, p, expected))

	got, err := store.GetProduct(ctx, p.UID)
	require.NoError(t, err)
	assert.Equal(t, "main", got.GitRef)
}

func TestScylla_UpdateProduct_NotFound(t *testing.T) {
	store := newTestStore(t)
	p := newProduct("test-ns", "ghost-"+newID()[:8])
	p.UID = newID() // does not exist
	err := store.UpdateProduct(context.Background(), p, "1")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_DeleteProduct(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	p := newProduct("test-ns", "del-"+newID()[:8])
	require.NoError(t, store.CreateProduct(ctx, p))
	require.NoError(t, store.DeleteProduct(ctx, p.UID))

	_, err := store.GetProduct(ctx, p.UID)
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_DeleteProduct_NotFound(t *testing.T) {
	store := newTestStore(t)
	err := store.DeleteProduct(context.Background(), newID())
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

// ── CategoryTaxonomy ──────────────────────────────────────────────────────────

func newCategoryTaxonomy(ns, name string) *datastore.CategoryTaxonomy {
	return &datastore.CategoryTaxonomy{
		UID:             newID(),
		Namespace:       ns,
		Name:            name,
		APIVersion:      "catalog.gitstore.dev/v1beta1",
		Kind:            "CategoryTaxonomy",
		Generation:      1,
		ResourceVersion: "1",
	}
}

func TestScylla_CreateGetCategoryTaxonomy(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	c := newCategoryTaxonomy("test-ns", "cat-"+newID()[:8])
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, c))

	got, err := store.GetCategoryTaxonomyByName(ctx, c.Namespace, c.Name)
	require.NoError(t, err)
	assert.Equal(t, c.UID, got.UID)

	gotByUID, err := store.GetCategoryTaxonomy(ctx, c.UID)
	require.NoError(t, err)
	assert.Equal(t, c.Name, gotByUID.Name)
}

func TestScylla_CreateCategoryTaxonomy_DuplicateName(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	name := "dup-cat-" + newID()[:8]
	c1 := newCategoryTaxonomy("test-ns", name)
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, c1))
	c2 := newCategoryTaxonomy("test-ns", name)
	err := store.CreateCategoryTaxonomy(ctx, c2)
	require.ErrorIs(t, err, datastore.ErrAlreadyExists)
}

func TestScylla_GetCategoryTaxonomy_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.GetCategoryTaxonomyByName(context.Background(), "test-ns", "no-such-cat-"+newID()[:8])
	require.ErrorIs(t, err, datastore.ErrNotFound)

	_, err = store.GetCategoryTaxonomy(context.Background(), newID())
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_ListCategoryTaxonomies(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	before, err := store.ListCategoryTaxonomies(ctx, "test-ns", datastore.PageParams{First: 100})
	require.NoError(t, err)

	c1 := newCategoryTaxonomy("test-ns", "catls1-"+newID()[:8])
	c2 := newCategoryTaxonomy("test-ns", "catls2-"+newID()[:8])
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, c1))
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, c2))

	after, err := store.ListCategoryTaxonomies(ctx, "test-ns", datastore.PageParams{First: 100})
	require.NoError(t, err)
	assert.Equal(t, len(before.Items)+2, len(after.Items))
}

func TestScylla_UpdateCategoryTaxonomy(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	c := newCategoryTaxonomy("test-ns", "upd-cat-"+newID()[:8])
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, c))
	c.AncestorPath = "electronics"
	expected := c.ResourceVersion
	datastore.AdvanceCategoryTaxonomySystemVersion(c)
	require.NoError(t, store.UpdateCategoryTaxonomy(ctx, c, expected))

	got, err := store.GetCategoryTaxonomyByName(ctx, c.Namespace, c.Name)
	require.NoError(t, err)
	assert.Equal(t, "electronics", got.AncestorPath)
}

func TestScylla_UpdateCategoryTaxonomy_NotFound(t *testing.T) {
	store := newTestStore(t)
	c := newCategoryTaxonomy("test-ns", "ghost-cat-"+newID()[:8])
	err := store.UpdateCategoryTaxonomy(context.Background(), c, "1")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

// ── Collection ────────────────────────────────────────────────────────────────

func TestScylla_CreateGetCollection(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	c := newCollection("test-ns", "col-"+newID()[:8])
	require.NoError(t, store.CreateCollection(ctx, c))

	got, err := store.GetCollection(ctx, c.UID)
	require.NoError(t, err)
	assert.Equal(t, c.UID, got.UID)
	assert.Equal(t, c.Name, got.Name)
	assert.Equal(t, c.Namespace, got.Namespace)
}

func TestScylla_CreateCollection_DuplicateUID(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	c := newCollection("test-ns", "dup-uid-"+newID()[:8])
	require.NoError(t, store.CreateCollection(ctx, c))
	err := store.CreateCollection(ctx, c)
	require.ErrorIs(t, err, datastore.ErrAlreadyExists)
}

func TestScylla_CreateCollection_DuplicateName(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	name := "dup-col-" + newID()[:8]
	c1 := newCollection("test-ns", name)
	require.NoError(t, store.CreateCollection(ctx, c1))
	c2 := newCollection("test-ns", name)
	err := store.CreateCollection(ctx, c2)
	require.ErrorIs(t, err, datastore.ErrAlreadyExists)
}

func TestScylla_GetCollection_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.GetCollection(context.Background(), newID())
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_GetCollectionByName(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	name := "find-col-" + newID()[:8]
	c := newCollection("test-ns", name)
	require.NoError(t, store.CreateCollection(ctx, c))

	got, err := store.GetCollectionByName(ctx, "test-ns", name)
	require.NoError(t, err)
	assert.Equal(t, c.UID, got.UID)
}

func TestScylla_GetCollectionByName_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.GetCollectionByName(context.Background(), "test-ns", "no-col-"+newID()[:8])
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_ListCollections(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	before, err := store.ListCollections(ctx, "test-ns", datastore.PageParams{First: 100})
	require.NoError(t, err)

	c1 := newCollection("test-ns", "colls1-"+newID()[:8])
	c2 := newCollection("test-ns", "colls2-"+newID()[:8])
	require.NoError(t, store.CreateCollection(ctx, c1))
	require.NoError(t, store.CreateCollection(ctx, c2))

	after, err := store.ListCollections(ctx, "test-ns", datastore.PageParams{First: 100})
	require.NoError(t, err)
	assert.Equal(t, len(before.Items)+2, len(after.Items))
}

func TestScylla_UpdateCollection(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	c := newCollection("test-ns", "upd-col-"+newID()[:8])
	c.Body = "Before"
	require.NoError(t, store.CreateCollection(ctx, c))
	c.Body = "After"
	expected := c.ResourceVersion
	datastore.AdvanceCollectionSystemVersion(c)
	require.NoError(t, store.UpdateCollection(ctx, c, expected))

	got, err := store.GetCollection(ctx, c.UID)
	require.NoError(t, err)
	assert.Equal(t, "After", got.Body)
}

func TestScylla_UpdateCollection_NotFound(t *testing.T) {
	store := newTestStore(t)
	err := store.UpdateCollection(context.Background(), newCollection("test-ns", "ghost-col-"+newID()[:8]), "1")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

// ── Product: three-table schema tests (016-product-spec-hydration) ────────────

func productCursor(p *datastore.Product) string {
	return pageCursor(p.CreationTimestamp, p.UID)
}

func pageCursor(created time.Time, uid string) string {
	raw := fmt.Sprintf("keyset|%s|%s",
		created.UTC().Format(time.RFC3339Nano), uid)
	return base64Encode(raw)
}

func base64Encode(s string) string {
	const enc = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	src := []byte(s)
	dst := make([]byte, (len(src)+2)/3*4)
	n := 0
	for i := 0; i < len(src); i += 3 {
		var b [3]byte
		b[0] = src[i]
		if i+1 < len(src) {
			b[1] = src[i+1]
		}
		if i+2 < len(src) {
			b[2] = src[i+2]
		}
		remaining := len(src) - i
		dst[n] = enc[(b[0]>>2)&0x3f]
		dst[n+1] = enc[((b[0]&0x03)<<4)|((b[1]>>4)&0x0f)]
		if remaining > 1 {
			dst[n+2] = enc[((b[1]&0x0f)<<2)|((b[2]>>6)&0x03)]
		} else {
			dst[n+2] = '='
		}
		if remaining > 2 {
			dst[n+3] = enc[b[2]&0x3f]
		} else {
			dst[n+3] = '='
		}
		n += 4
	}
	return string(dst[:n])
}

func TestScylla_GetProductByName_RoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ns := "rt-ns-" + newID()[:8]
	p := newProduct(ns, "findable-"+newID()[:8])
	require.NoError(t, store.CreateProduct(ctx, p))

	got, err := store.GetProductByName(ctx, ns, p.Name)
	require.NoError(t, err)
	assert.Equal(t, p.UID, got.UID)
	assert.Equal(t, p.Name, got.Name)
	assert.Equal(t, ns, got.Namespace)
}

func TestScylla_GetProductByUID_RoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ns := "uid-rt-ns-" + newID()[:8]
	p := newProduct(ns, "uid-rt-"+newID()[:8])
	require.NoError(t, store.CreateProduct(ctx, p))

	got, err := store.GetProduct(ctx, p.UID)
	require.NoError(t, err)
	assert.Equal(t, p.UID, got.UID)
	assert.Equal(t, p.Name, got.Name)
	assert.Equal(t, ns, got.Namespace)
}

func TestScylla_UpdateProduct_BatchFanOut(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ns := "upd-fan-" + newID()[:8]
	p := newProduct(ns, "widget-"+newID()[:8])
	require.NoError(t, store.CreateProduct(ctx, p))

	p.GitRef = "main"
	p.GitCommitSHA = "abc123"
	expected := p.ResourceVersion
	datastore.AdvanceProductSystemVersion(p)
	require.NoError(t, store.UpdateProduct(ctx, p, expected))

	byUID, err := store.GetProduct(ctx, p.UID)
	require.NoError(t, err)
	assert.Equal(t, "main", byUID.GitRef)
	assert.Equal(t, "abc123", byUID.GitCommitSHA)

	byName, err := store.GetProductByName(ctx, ns, p.Name)
	require.NoError(t, err)
	assert.Equal(t, "main", byName.GitRef)

	page, err := store.ListProducts(ctx, ns, datastore.PageParams{First: 100})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "main", page.Items[0].GitRef)
}

func TestScylla_DeleteProduct_BatchFanOut(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ns := "del-fan-" + newID()[:8]
	p := newProduct(ns, "del-widget-"+newID()[:8])
	require.NoError(t, store.CreateProduct(ctx, p))
	require.NoError(t, store.DeleteProduct(ctx, p.UID))

	_, errUID := store.GetProduct(ctx, p.UID)
	assert.ErrorIs(t, errUID, datastore.ErrNotFound)

	_, errName := store.GetProductByName(ctx, ns, p.Name)
	assert.ErrorIs(t, errName, datastore.ErrNotFound)

	page, err := store.ListProducts(ctx, ns, datastore.PageParams{First: 100})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
}

func TestScylla_Product_SpecStatus_RoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ns := "spec-rt-" + newID()[:8]
	p := newProduct(ns, "spec-"+newID()[:8])
	p.Spec = []byte(`{"title":"Widget Pro","tags":["sale"]}`)
	p.Status = []byte(`{"observedGeneration":1,"conditions":[{"type":"READY","status":"TRUE","lastTransitionTime":"2026-01-01T00:00:00Z"}]}`)
	require.NoError(t, store.CreateProduct(ctx, p))

	got, err := store.GetProduct(ctx, p.UID)
	require.NoError(t, err)
	assert.Equal(t, string(p.Spec), string(got.Spec))
	assert.Equal(t, string(p.Status), string(got.Status))
}

func TestScylla_ListProducts_ThreePageForwardCursor(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ns := "paginate-" + newID()[:8]
	total := 25
	for i := 0; i < total; i++ {
		p := newProduct(ns, fmt.Sprintf("item-%03d-%s", i, newID()[:6]))
		p.CreationTimestamp = time.Now().UTC().Add(time.Duration(i) * time.Millisecond).Truncate(time.Millisecond)
		require.NoError(t, store.CreateProduct(ctx, p))
	}

	const pageSize = 10
	seen := make(map[string]bool)

	r1, err := store.ListProducts(ctx, ns, datastore.PageParams{First: pageSize})
	require.NoError(t, err)
	require.Len(t, r1.Items, 10)
	assert.True(t, r1.HasNext)
	for _, p := range r1.Items {
		require.False(t, seen[p.UID], "duplicate on page 1: %s", p.UID)
		seen[p.UID] = true
	}

	cursor1 := productCursor(r1.Items[len(r1.Items)-1])
	r2, err := store.ListProducts(ctx, ns, datastore.PageParams{First: pageSize, After: cursor1})
	require.NoError(t, err)
	require.Len(t, r2.Items, 10)
	assert.True(t, r2.HasNext)
	for _, p := range r2.Items {
		require.False(t, seen[p.UID], "duplicate on page 2: %s", p.UID)
		seen[p.UID] = true
	}

	cursor2 := productCursor(r2.Items[len(r2.Items)-1])
	r3, err := store.ListProducts(ctx, ns, datastore.PageParams{First: pageSize, After: cursor2})
	require.NoError(t, err)
	require.Len(t, r3.Items, 5)
	assert.False(t, r3.HasNext)
	for _, p := range r3.Items {
		require.False(t, seen[p.UID], "duplicate on page 3: %s", p.UID)
		seen[p.UID] = true
	}

	assert.Len(t, seen, total)
}

func TestScylla_ListProducts_BackwardCursor(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ns := "back-page-" + newID()[:8]
	for i := 0; i < 15; i++ {
		p := newProduct(ns, fmt.Sprintf("bitem-%03d-%s", i, newID()[:6]))
		p.CreationTimestamp = time.Now().UTC().Add(time.Duration(i) * time.Millisecond).Truncate(time.Millisecond)
		require.NoError(t, store.CreateProduct(ctx, p))
	}

	r1, err := store.ListProducts(ctx, ns, datastore.PageParams{Last: 5})
	require.NoError(t, err)
	require.Len(t, r1.Items, 5)
	assert.True(t, r1.HasPrevious)

	seen := make(map[string]bool)
	for _, p := range r1.Items {
		seen[p.UID] = true
	}

	cursorBefore := productCursor(r1.Items[0])
	r2, err := store.ListProducts(ctx, ns, datastore.PageParams{Last: 5, Before: cursorBefore})
	require.NoError(t, err)
	require.Len(t, r2.Items, 5)
	for _, p := range r2.Items {
		require.False(t, seen[p.UID], "duplicate on backward page 2: %s", p.UID)
		seen[p.UID] = true
	}
}

func TestScylla_ListProducts_AfterLastCursor_ReturnsEmpty(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ns := "empty-page-" + newID()[:8]
	for i := 0; i < 3; i++ {
		p := newProduct(ns, fmt.Sprintf("ep-%03d-%s", i, newID()[:6]))
		p.CreationTimestamp = time.Now().UTC().Add(time.Duration(i) * time.Millisecond).Truncate(time.Millisecond)
		require.NoError(t, store.CreateProduct(ctx, p))
	}

	r1, err := store.ListProducts(ctx, ns, datastore.PageParams{First: 10})
	require.NoError(t, err)
	require.Len(t, r1.Items, 3)
	assert.False(t, r1.HasNext)

	lastCursor := productCursor(r1.Items[len(r1.Items)-1])
	r2, err := store.ListProducts(ctx, ns, datastore.PageParams{First: 10, After: lastCursor})
	require.NoError(t, err)
	assert.Empty(t, r2.Items)
	assert.False(t, r2.HasNext)
}

func TestScylla_NamespaceDirectUID_FullEnvelopeAndBodyRoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	deletedAt := now.Add(time.Hour)
	uid := newID()
	namespace := &datastore.Namespace{
		APIVersion:        "gitstore.dev/v1beta1",
		Kind:              "Namespace",
		UID:               uid,
		ID:                uid,
		Name:              "namespace-envelope-" + newID()[:8],
		Generation:        7,
		ResourceVersion:   "11",
		Revision:          "main@sha1:namespace",
		CreationTimestamp: now,
		CreationActor:     "alice",
		UpdateTimestamp:   now.Add(time.Minute),
		UpdateActor:       "bob",
		Labels:            map[string]string{"team": "catalog"},
		Annotations:       map[string]string{"gitstore.dev/note": "namespace"},
		OwnerReferences:   json.RawMessage(`[{"apiVersion":"gitstore.dev/v1beta1","kind":"Namespace","name":"owner","uid":"00000000-0000-0000-0000-000000000001"}]`),
		Finalizers:        []string{"gitstore.dev/test"},
		DeletionTimestamp: &deletedAt,
		SourcePath:        "namespaces/example.md",
		GitCommitSHA:      "namespace-sha",
		GitRef:            "refs/heads/main",
		Spec:              json.RawMessage(`{"title":"Canonical namespace","tier":"USER"}`),
		Body:              "# Namespace\n\nRaw **Markdown** body.\n",
		Status:            json.RawMessage(`{"observedGeneration":7,"conditions":[]}`),
		Title:             "Canonical namespace",
		Tier:              datastore.NamespaceTierUser,
	}

	require.NoError(t, store.CreateNamespace(ctx, namespace))

	got, err := store.GetNamespace(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, namespace, got)

	byName, err := store.GetNamespaceByName(ctx, namespace.Name)
	require.NoError(t, err)
	assert.Equal(t, namespace, byName)
	assert.Equal(t, uid, got.UID)
	assert.Equal(t, namespace.Body, got.Body)
	assert.JSONEq(t, string(namespace.OwnerReferences), string(got.OwnerReferences))
}

func TestScylla_RepositoryCatalogProjectionUpgradeRepair(t *testing.T) {
	ctx := context.Background()
	cfg := testScyllaConfig(t)
	cfg.Keyspace += "_catalog_repair"
	t.Cleanup(func() { require.NoError(t, dropKeyspace(scyllaAddr, cfg.Keyspace)) })
	require.NoError(t, provisionKeyspace(scyllaAddr, cfg.Keyspace))
	store, err := scylla.New(cfg, zap.NewNop(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ns := &datastore.Namespace{UID: newID(), Name: "upgrade", CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond)}
	require.NoError(t, store.CreateNamespace(ctx, ns))
	repository := &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: "catalog", CreationTimestamp: ns.CreationTimestamp}
	require.NoError(t, store.CreateRepository(ctx, repository))
	product := &datastore.Product{UID: newID(), Namespace: ns.Name, Name: "product", RepositoryID: repository.UID, ResourceVersion: "1"}
	require.NoError(t, store.CreateProduct(ctx, product))
	session, err := openRootSession(scyllaAddr)
	require.NoError(t, err)
	t.Cleanup(session.Close)
	repositoryUID, err := gocql.ParseUUID(repository.UID)
	require.NoError(t, err)
	for shard := int8(0); shard < 32; shard++ {
		require.NoError(t, session.Query("DELETE FROM "+cfg.Keyspace+".resources_by_repository WHERE repository_id=? AND shard=?", repositoryUID, shard).Exec())
	}
	require.NoError(t, session.Query("DELETE FROM "+cfg.Keyspace+".repository_catalog_projection_state WHERE repository_id=?", repositoryUID).Exec())
	_, err = store.HasCatalogResources(ctx, repository.UID)
	require.ErrorIs(t, err, datastore.ErrProjectionNotReady)
	repair, err := scylla.OpenProjectionRepairService(cfg)
	require.NoError(t, err)
	t.Cleanup(repair.Close)
	plan, err := repair.Audit(ctx)
	require.NoError(t, err)
	require.Len(t, plan.Actions, 1)
	_, err = repair.Apply(ctx, plan)
	require.NoError(t, err)
	blocked, err := store.HasCatalogResources(ctx, repository.UID)
	require.NoError(t, err)
	require.True(t, blocked)
	require.NoError(t, store.DeleteProduct(ctx, product.UID))
	blocked, err = store.HasCatalogResources(ctx, repository.UID)
	require.NoError(t, err)
	require.False(t, blocked)
}

func TestScylla_ProjectionRepairPreservesProductResolvedPaths(t *testing.T) {
	ctx := t.Context()
	cfg := testScyllaConfig(t)
	cfg.Keyspace += "_paths"
	t.Cleanup(func() { require.NoError(t, dropKeyspace(scyllaAddr, cfg.Keyspace)) })
	require.NoError(t, provisionKeyspace(scyllaAddr, cfg.Keyspace))
	store, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	created := time.Now().UTC().Truncate(time.Millisecond)
	namespace := &datastore.Namespace{UID: newID(), Name: "paths", CreationTimestamp: created}
	require.NoError(t, store.CreateNamespace(ctx, namespace))
	repository := &datastore.Repository{UID: newID(), Namespace: namespace.Name, Name: "catalog", CreationTimestamp: created}
	require.NoError(t, store.CreateRepository(ctx, repository))
	root := &datastore.CategoryTaxonomy{
		UID: newID(), Namespace: namespace.Name, Name: "root", RepositoryID: repository.UID,
		ResourceVersion: "1", CreationTimestamp: created,
		Status: json.RawMessage(`{"resolved":{"path":["root"]}}`),
	}
	leaf := &datastore.CategoryTaxonomy{
		UID: newID(), Namespace: namespace.Name, Name: "leaf", RepositoryID: repository.UID,
		ResourceVersion: "1", CreationTimestamp: created.Add(time.Millisecond),
		Status: json.RawMessage(`{"resolved":{"path":["root","leaf"]}}`),
	}
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, root))
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, leaf))
	repair, err := scylla.OpenProjectionRepairService(cfg)
	require.NoError(t, err)
	t.Cleanup(repair.Close)
	for _, test := range []struct {
		name string
		path []string
		want map[string]bool
	}{
		{name: "direct", path: []string{"leaf"}, want: map[string]bool{leaf.UID: true}},
		{name: "ancestors", path: []string{"root", "leaf"}, want: map[string]bool{root.UID: true, leaf.UID: true}},
		{name: "pending-path", want: map[string]bool{leaf.UID: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			status, err := json.Marshal(map[string]any{
				"resolved": map[string]any{"category": map[string]any{"name": leaf.Name, "path": test.path}},
			})
			require.NoError(t, err)
			product := &datastore.Product{
				UID: newID(), Namespace: namespace.Name, Name: test.name, RepositoryID: repository.UID,
				ResourceVersion: "1", CreationTimestamp: created, Status: status,
			}
			require.NoError(t, store.CreateProduct(ctx, product))
			membership, ok := store.(datastore.CategoryProductIndex)
			require.True(t, ok)
			require.NoError(t, membership.ReplaceCategoryProductMembership(ctx, product, nil))
			plan, err := repair.Audit(ctx)
			require.NoError(t, err)
			got := map[string]bool{}
			for _, action := range plan.Actions {
				if action.UID == product.UID && action.After != nil && action.After.CategoryUID != "" {
					require.Equal(t, scylla.RepairInsert, action.Type)
					got[action.After.CategoryUID] = true
				}
			}
			require.Equal(t, test.want, got, "repair must not borrow the category's current ancestor path")
		})
	}
}

func TestRepositoryMembershipFiveKindsUseBoundedExactPartitionChecks(t *testing.T) {
	writer := newTestStore(t)
	cfg := testScyllaConfig(t)
	cfg.QueryObserver = capacityQueryObserver{}
	reader, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reader.Close()) })
	namespace := &datastore.Namespace{
		UID: newID(), Name: "membership-matrix-" + newID(), ResourceVersion: "1", Generation: 1,
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
	require.NoError(t, writer.CreateNamespace(t.Context(), namespace))
	repository := newRepository()
	repository.Namespace = namespace.Name
	repository.NamespaceID, repository.ResourceVersion, repository.Generation = namespace.UID, "1", 1
	require.NoError(t, writer.CreateRepository(t.Context(), repository))
	check := func(want bool) int {
		t.Helper()
		evidence := &capacityQueryEvidence{tables: map[string]int{}}
		ctx := context.WithValue(t.Context(), capacityQueryKey{}, evidence)
		started := time.Now()
		present, err := reader.HasCatalogResources(ctx, repository.UID)
		elapsed := time.Since(started)
		require.NoError(t, err)
		require.Equal(t, want, present)
		evidence.mu.Lock()
		defer evidence.mu.Unlock()
		require.Equal(t, 1, evidence.tables["repository_catalog_projection_state"])
		require.GreaterOrEqual(t, evidence.tables["resources_by_repository"], 1)
		require.LessOrEqual(t, evidence.tables["resources_by_repository"], 32)
		if !want {
			require.Equal(t, 32, evidence.tables["resources_by_repository"])
		}
		require.False(t, evidence.filtered)
		require.False(t, evidence.membershipUnbounded, "membership queries must retain full partition restrictions and LIMIT 1")
		require.Equal(t, evidence.tables["resources_by_repository"]+1, evidence.attempts)
		require.Zero(t, evidence.retries)
		t.Logf("membership present=%t partition_queries=%d attempts=%d fetched_rows=%d elapsed=%s",
			present, evidence.tables["resources_by_repository"], evidence.attempts, evidence.rows, elapsed)
		return evidence.tables["resources_by_repository"]
	}
	check(false)
	for _, kind := range []string{"Product", "ProductVariant", "CategoryTaxonomy", "Collection", "File"} {
		t.Run(kind, func(t *testing.T) {
			uid := newID()
			when := time.Now().UTC().Truncate(time.Millisecond)
			var remove func(context.Context) error
			switch kind {
			case "Product":
				product := newProduct(namespace.Name, "product")
				product.UID, product.RepositoryID, product.ResourceVersion = uid, repository.UID, "1"
				require.NoError(t, writer.CreateProduct(t.Context(), product))
				remove = func(ctx context.Context) error { return writer.DeleteProduct(ctx, uid) }
			case "ProductVariant":
				variant := newProductVariant(namespace.Name, "variant", "sku", "parent")
				variant.UID, variant.RepositoryID, variant.ResourceVersion = uid, repository.UID, "1"
				require.NoError(t, writer.CreateProductVariant(t.Context(), variant))
				remove = func(ctx context.Context) error { return writer.DeleteProductVariant(ctx, uid) }
			case "CategoryTaxonomy":
				require.NoError(t, writer.CreateCategoryTaxonomy(t.Context(), &datastore.CategoryTaxonomy{
					UID: uid, Namespace: namespace.Name, Name: "category", RepositoryID: repository.UID,
					ResourceVersion: "1", CreationTimestamp: when,
				}))
				remove = func(ctx context.Context) error { return writer.DeleteCategoryTaxonomy(ctx, uid) }
			case "Collection":
				collection := newCollection(namespace.Name, "collection")
				collection.UID, collection.RepositoryID, collection.ResourceVersion = uid, repository.UID, "1"
				require.NoError(t, writer.CreateCollection(t.Context(), collection))
				remove = func(ctx context.Context) error { return writer.DeleteCollection(ctx, uid) }
			case "File":
				require.NoError(t, writer.CreateFile(t.Context(), &datastore.File{
					UID: uid, Namespace: namespace.Name, Name: "file", RepositoryID: repository.UID,
					ResourceVersion: "1", CreationTimestamp: when,
				}))
				remove = func(ctx context.Context) error { return writer.DeleteFile(ctx, uid) }
			}
			check(true)
			require.NoError(t, remove(t.Context()))
			check(false)
		})
	}
	t.Run("mixed-last-shard-churn", func(t *testing.T) {
		raw := newRawSession(t)
		repositoryUID, err := gocql.ParseUUID(repository.UID)
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, raw.Query("DELETE FROM resources_by_repository WHERE repository_id=? AND shard=?",
				repositoryUID, int8(31)).Exec())
		})
		type member struct {
			kind string
			uid  gocql.UUID
		}
		members := make([]member, 0, 2560)
		// Raw projection rows isolate query behavior from admission and UID hashing.
		for _, kind := range []string{"CategoryTaxonomy", "Collection", "File", "Product", "ProductVariant"} {
			for range 512 {
				uid, err := gocql.ParseUUID(fmt.Sprintf("00000000-0000-4000-8000-%012x", len(members)+1))
				require.NoError(t, err)
				require.NoError(t, raw.Query(
					"INSERT INTO resources_by_repository (repository_id,shard,kind,uid,namespace,name,creation_timestamp) VALUES (?,?,?,?,?,?,?)",
					repositoryUID, int8(31), kind, uid, namespace.Name, "member", namespace.CreationTimestamp).Exec())
				members = append(members, member{kind: kind, uid: uid})
			}
		}
		require.Equal(t, 32, check(true))
		for _, member := range members[:len(members)-1] {
			require.NoError(t, raw.Query(
				"DELETE FROM resources_by_repository WHERE repository_id=? AND shard=? AND kind=? AND uid=?",
				repositoryUID, int8(31), member.kind, member.uid).Exec())
		}
		require.Equal(t, 32, check(true), "a late surviving kind must still block deletion after churn")
		last := members[len(members)-1]
		require.NoError(t, raw.Query(
			"DELETE FROM resources_by_repository WHERE repository_id=? AND shard=? AND kind=? AND uid=?",
			repositoryUID, int8(31), last.kind, last.uid).Exec())
		require.Equal(t, 32, check(false))
	})
}

func TestCatalogDeleteConcurrentVersionChangeDoesNotMutateProjections(t *testing.T) {
	for _, kind := range []string{"Product", "ProductVariant", "CategoryTaxonomy", "Collection", "File", "CategoryCompletion"} {
		t.Run(kind, func(t *testing.T) {
			ctx := t.Context()
			type deletionKey struct{}
			deletionCtx := context.WithValue(ctx, deletionKey{}, true)
			tableName := map[string]string{
				"Product": "products", "ProductVariant": "product_variants", "CategoryTaxonomy": "category_taxonomies",
				"Collection": "collections", "File": "files", "CategoryCompletion": "category_taxonomies",
			}[kind]
			raw := newRawSession(t)
			uid := newID()
			when := time.Now().UTC().Truncate(time.Millisecond)
			ns := &datastore.Namespace{
				UID: newID(), Name: "stale-delete-" + newID(), ResourceVersion: "1", Generation: 1, CreationTimestamp: when,
			}
			expectedVersion, updatedVersion := "1", "2"
			if kind == "CategoryCompletion" {
				expectedVersion, updatedVersion = "2", "3"
			}
			var injected sync.Once
			var projectionDeletes atomic.Int64
			raceResult := make(chan error, 1)
			cfg := testScyllaConfig(t)
			cfg.QueryObserver = namespaceReservationObserver(func(queryCtx context.Context, query gocql.ObservedQuery) {
				if queryCtx.Value(deletionKey{}) != true {
					return
				}
				if query.Err == nil && strings.HasPrefix(query.Statement, "SELECT ") &&
					strings.Contains(query.Statement, " FROM "+tableName+"_by_namespace WHERE ") {
					injected.Do(func() {
						applied, err := raw.Query("UPDATE "+tableName+"_by_namespace SET resource_version=? WHERE namespace=? AND creation_timestamp=? AND uid=? IF resource_version=?",
							updatedVersion, ns.Name, when, uid, expectedVersion).WithContext(ctx).MapScanCAS(map[string]any{})
						if err == nil && !applied {
							err = errors.New("peer version CAS was not applied")
						}
						raceResult <- err
					})
				}
				if strings.HasPrefix(query.Statement, "DELETE FROM ") &&
					!strings.HasPrefix(query.Statement, "DELETE FROM "+tableName+"_by_namespace ") {
					projectionDeletes.Add(1)
				}
			})
			first, err := scylla.New(cfg, zap.NewNop())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, first.Close()) })
			second := newTestStore(t)
			require.NoError(t, first.CreateNamespace(ctx, ns))
			repository := newRepository()
			repository.Namespace, repository.NamespaceID = ns.Name, ns.UID
			repository.ResourceVersion, repository.Generation = "1", 1
			require.NoError(t, first.CreateRepository(ctx, repository))
			var remove func(context.Context) error
			var read func(datastore.Datastore) (string, string, error)
			switch kind {
			case "Product":
				resource := newProduct(ns.Name, "fixture")
				resource.UID, resource.CreationTimestamp, resource.RepositoryID, resource.ResourceVersion = uid, when, repository.UID, "1"
				resource.Status = json.RawMessage(`{}`)
				require.NoError(t, first.CreateProduct(ctx, resource))
				remove = func(ctx context.Context) error { return first.DeleteProduct(ctx, uid) }
				read = func(store datastore.Datastore) (string, string, error) {
					got, err := store.GetProductByName(ctx, ns.Name, resource.Name)
					if err != nil {
						return "", "", err
					}
					return got.UID, got.ResourceVersion, nil
				}
			case "ProductVariant":
				resource := newProductVariant(ns.Name, "fixture", "sku", "parent")
				resource.UID, resource.CreationTimestamp, resource.RepositoryID, resource.ResourceVersion = uid, when, repository.UID, "1"
				require.NoError(t, first.CreateProductVariant(ctx, resource))
				remove = func(ctx context.Context) error { return first.DeleteProductVariant(ctx, uid) }
				read = func(store datastore.Datastore) (string, string, error) {
					got, err := store.GetProductVariantByName(ctx, ns.Name, resource.Name)
					if err != nil {
						return "", "", err
					}
					return got.UID, got.ResourceVersion, nil
				}
			case "CategoryTaxonomy", "CategoryCompletion":
				resource := &datastore.CategoryTaxonomy{
					UID: uid, Namespace: ns.Name, Name: "fixture", CreationTimestamp: when, RepositoryID: repository.UID, ResourceVersion: "1",
				}
				require.NoError(t, first.CreateCategoryTaxonomy(ctx, resource))
				remove = func(ctx context.Context) error { return first.DeleteCategoryTaxonomy(ctx, uid) }
				if kind == "CategoryCompletion" {
					deletions := first.(datastore.CategoryTaxonomyDeletionStore)
					marked, err := deletions.MarkCategoryTaxonomyDeletion(ctx, ns.Name, resource.Name, "1", uid, when.Add(time.Second))
					require.NoError(t, err)
					require.Equal(t, expectedVersion, marked.ResourceVersion)
					remove = func(ctx context.Context) error {
						_, err := deletions.CompleteCategoryTaxonomyDeletion(ctx, ns.Name, resource.Name, expectedVersion, uid)
						return err
					}
				}
				read = func(store datastore.Datastore) (string, string, error) {
					got, err := store.GetCategoryTaxonomyByName(ctx, ns.Name, resource.Name)
					if err != nil {
						return "", "", err
					}
					return got.UID, got.ResourceVersion, nil
				}
			case "Collection":
				resource := newCollection(ns.Name, "fixture")
				resource.UID, resource.CreationTimestamp, resource.RepositoryID, resource.ResourceVersion = uid, when, repository.UID, "1"
				require.NoError(t, first.CreateCollection(ctx, resource))
				remove = func(ctx context.Context) error { return first.DeleteCollection(ctx, uid) }
				read = func(store datastore.Datastore) (string, string, error) {
					got, err := store.GetCollectionByName(ctx, ns.Name, resource.Name)
					if err != nil {
						return "", "", err
					}
					return got.UID, got.ResourceVersion, nil
				}
			case "File":
				resource := &datastore.File{
					UID: uid, Namespace: ns.Name, Name: "fixture", CreationTimestamp: when, RepositoryID: repository.UID, ResourceVersion: "1",
				}
				require.NoError(t, first.CreateFile(ctx, resource))
				remove = func(ctx context.Context) error { return first.DeleteFile(ctx, uid) }
				read = func(store datastore.Datastore) (string, string, error) {
					got, err := store.GetFileByName(ctx, ns.Name, resource.Name)
					if err != nil {
						return "", "", err
					}
					return got.UID, got.ResourceVersion, nil
				}
			}
			deletionErr := remove(deletionCtx)
			select {
			case err := <-raceResult:
				require.NoError(t, err, "the peer must commit the version change after the stale read")
			default:
				t.Fatal("the real stale-read race was not exercised")
			}
			require.ErrorIs(t, deletionErr, datastore.ErrConflict)
			require.Zero(t, projectionDeletes.Load(), "a rejected authoritative CAS must never attempt projection deletion")
			for _, store := range []datastore.Datastore{first, second} {
				gotUID, gotVersion, err := read(store)
				require.NoError(t, err)
				require.Equal(t, uid, gotUID)
				require.Equal(t, updatedVersion, gotVersion)
				present, err := store.HasCatalogResources(ctx, repository.UID)
				require.NoError(t, err)
				require.True(t, present, "the peer's surviving resource must remain a deletion blocker")
			}
		})
	}
}

func TestScylla_RepositoryMembershipDoesNotEnumerateKinds(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	ns := &datastore.Namespace{UID: newID(), Name: "future-kinds-" + newID()[:8], CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond)}
	require.NoError(t, store.CreateNamespace(ctx, ns))
	repo := &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: "catalog", CreationTimestamp: ns.CreationTimestamp}
	require.NoError(t, store.CreateRepositoryInActiveNamespace(ctx, repo))
	session, err := openRootSession(scyllaAddr)
	require.NoError(t, err)
	t.Cleanup(session.Close)
	repoUID, err := gocql.ParseUUID(repo.UID)
	require.NoError(t, err)
	memberUID, err := gocql.ParseUUID(newID())
	require.NoError(t, err)
	// Distinct future kinds may share a UID without colliding in this index.
	for i := range 40 {
		require.NoError(t, session.Query("INSERT INTO "+scyllaKeyspace+".resources_by_repository (repository_id,shard,kind,uid) VALUES (?,?,?,?)",
			repoUID, int8(31), fmt.Sprintf("FutureKind%d", i), memberUID).Exec())
	}
	blocked, err := store.HasCatalogResources(ctx, repo.UID)
	require.NoError(t, err)
	require.True(t, blocked)
	for i := range 39 {
		require.NoError(t, session.Query("DELETE FROM "+scyllaKeyspace+".resources_by_repository WHERE repository_id=? AND shard=? AND kind=? AND uid=?",
			repoUID, int8(31), fmt.Sprintf("FutureKind%d", i), memberUID).Exec())
	}
	blocked, err = store.HasCatalogResources(ctx, repo.UID)
	require.NoError(t, err)
	require.True(t, blocked, "an unregistered kind must still block deletion")
	require.NoError(t, session.Query("DELETE FROM "+scyllaKeyspace+".resources_by_repository WHERE repository_id=? AND shard=? AND kind=? AND uid=?",
		repoUID, int8(31), "FutureKind39", memberUID).Exec())
	blocked, err = store.HasCatalogResources(ctx, repo.UID)
	require.NoError(t, err)
	require.False(t, blocked)
}

func TestScylla_CatalogAdmissionsWaitForNamespaceFence(t *testing.T) {
	storeA, storeB := newTestStores(t)
	ctx := context.Background()
	ns := &datastore.Namespace{UID: newID(), Name: "catalog-wait-" + newID()[:8]}
	require.NoError(t, storeA.CreateNamespace(ctx, ns))
	repo := &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: "catalog"}
	require.NoError(t, storeA.CreateRepositoryInActiveNamespace(ctx, repo))
	session, err := openRootSession(scyllaAddr)
	require.NoError(t, err)
	t.Cleanup(session.Close)
	nsUID, err := gocql.ParseUUID(ns.UID)
	require.NoError(t, err)
	setPending := func(pending int64) {
		t.Helper()
		require.NoError(t, session.Query("UPDATE "+scyllaKeyspace+".namespaces_by_uid SET pending_repository_creations=? WHERE uid=?", pending, nsUID).Exec())
	}
	setPending(1)
	t.Cleanup(func() { setPending(0) })

	canceledCtx, cancel := context.WithTimeout(ctx, 60*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, storeB.CreateProduct(canceledCtx, &datastore.Product{
		UID: newID(), Namespace: ns.Name, Name: "canceled", RepositoryID: repo.UID,
	}), context.DeadlineExceeded)
	_, err = storeA.GetProductByName(ctx, ns.Name, "canceled")
	require.ErrorIs(t, err, datastore.ErrNotFound)

	const writers = 8
	results := make(chan error, writers)
	products := make([]*datastore.Product, writers)
	for i := range writers {
		products[i] = &datastore.Product{UID: newID(), Namespace: ns.Name, Name: fmt.Sprintf("product-%d", i), RepositoryID: repo.UID}
		go func(i int) {
			writer := storeA
			if i%2 == 0 {
				writer = storeB
			}
			results <- writer.CreateProduct(ctx, products[i])
		}(i)
	}
	select {
	case err := <-results:
		t.Fatalf("admission returned before reservation release: %v", err)
	case <-time.After(120 * time.Millisecond):
	}
	setPending(0)
	for range writers {
		require.NoError(t, <-results)
	}
	for _, product := range products {
		got, err := storeA.GetProduct(ctx, product.UID)
		require.NoError(t, err)
		require.Equal(t, product.Name, got.Name)
	}
}

func TestScylla_FileCohortOrdersOverlappingNamesUnderOneFence(t *testing.T) {
	for _, snapshots := range []bool{false, true} {
		t.Run(fmt.Sprintf("snapshots=%t", snapshots), func(t *testing.T) {
			testFileCohortOrdersOverlappingNamesUnderOneFence(t, snapshots)
		})
	}
}

func testFileCohortOrdersOverlappingNamesUnderOneFence(t *testing.T, snapshots bool) {
	t.Helper()
	first, peer := newTestStores(t)
	rawFirst := first
	var recorder *tracetest.SpanRecorder
	if snapshots {
		previous := otel.GetTracerProvider()
		defer otel.SetTracerProvider(previous)
		recorder = tracetest.NewSpanRecorder()
		provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(recorder))
		defer provider.Shutdown(t.Context())
		otel.SetTracerProvider(provider)
		first = datastore.NewInstrumentedDatastoreWithRegistry(first, "scylla", zap.NewNop(), prometheus.NewRegistry())
	}
	ctx := t.Context()
	ns := &datastore.Namespace{UID: newID(), Name: "file-ordered-" + newID()[:8]}
	require.NoError(t, first.CreateNamespace(ctx, ns))
	repo := &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: "catalog"}
	require.NoError(t, first.CreateRepositoryInActiveNamespace(ctx, repo))
	file := &datastore.File{
		UID: newID(), Namespace: ns.Name, Name: "shared", RepositoryID: repo.UID,
		ResourceVersion: "1", Generation: 1, CreationTimestamp: time.Now().UTC(),
	}
	require.NoError(t, first.CreateFile(ctx, file))
	witness := &datastore.File{UID: newID(), Namespace: ns.Name, Name: "witness", RepositoryID: repo.UID,
		ResourceVersion: "1", CreationTimestamp: time.Now().UTC()}
	if snapshots {
		require.NoError(t, first.CreateFile(ctx, witness))
	}
	session, err := openRootSession(scyllaAddr)
	require.NoError(t, err)
	t.Cleanup(session.Close)
	nsUID, err := gocql.ParseUUID(ns.UID)
	require.NoError(t, err)
	readFence := func() (int64, int64) {
		t.Helper()
		var epoch, pending int64
		require.NoError(t, session.Query("SELECT repository_creation_epoch,pending_repository_creations FROM "+
			scyllaKeyspace+".namespaces_by_uid WHERE uid=?", nsUID).Scan(&epoch, &pending))
		return epoch, pending
	}
	beforeEpoch, pending := readFence()
	require.Zero(t, pending)
	initialStarted, releaseInitial := make(chan struct{}), make(chan struct{})
	firstStarted, releaseFirst := make(chan struct{}), make(chan struct{})
	var initialOnce, firstOnce sync.Once
	unblockInitial := func() { initialOnce.Do(func() { close(releaseInitial) }) }
	unblockFirst := func() { firstOnce.Do(func() { close(releaseFirst) }) }
	var running sync.WaitGroup
	t.Cleanup(func() {
		unblockInitial()
		unblockFirst()
		running.Wait()
	})
	initialDone := make(chan error, 1)
	running.Go(func() {
		initialDone <- first.(datastore.CatalogMutationBatcher).WithCatalogMutationBatch(ctx, ns.Name, func(context.Context) error {
			close(initialStarted)
			<-releaseInitial
			return nil
		})
	})
	<-initialStarted
	var finished atomic.Int64
	results := make([]chan error, 4)
	for i := range results {
		results[i] = make(chan error, 1)
		running.Go(func() {
			operationCtx := ctx
			end := func() {}
			if snapshots {
				spanCtx, span := otel.Tracer("test").Start(ctx, "file.callback")
				operationCtx, end = spanCtx, func() { span.End() }
			}
			apply := func(batchCtx context.Context) error {
				assert.EqualValues(t, i, finished.Load(), "overlapping File mutations must finish in queue order")
				if i == 0 {
					close(firstStarted)
					<-releaseFirst
				}
				current, err := first.GetFile(batchCtx, file.UID)
				if err != nil {
					return err
				}
				expected := current.ResourceVersion
				current.Annotations = map[string]string{"sequence": fmt.Sprint(i)}
				datastore.AdvanceFileSystemVersion(current)
				if err := first.UpdateFile(batchCtx, current, expected); err != nil {
					return err
				}
				finished.Store(int64(i + 1))
				return nil
			}
			var result error
			if snapshots {
				result = first.(datastore.FileMutationSnapshotBatcher).WithFileMutationBatchSnapshots(operationCtx, ns.Name, repo.UID,
					[]string{file.Name, witness.Name}, []string{file.UID, witness.UID}, apply)
			} else {
				result = first.(datastore.FileMutationBatcher).WithFileMutationBatch(operationCtx, ns.Name, repo.UID, []string{file.Name}, apply)
			}
			end()
			results[i] <- result
		})
		require.Eventually(t, func() bool {
			return scylla.QueuedNamespaceCatalogWritesForTest(rawFirst, ns.UID) == i+2
		}, time.Second, time.Millisecond)
	}
	unblockInitial()
	require.NoError(t, <-initialDone)
	select {
	case <-firstStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("the selected File cohort did not start")
	}
	heldEpoch, held := readFence()
	require.Equal(t, beforeEpoch+2, heldEpoch)
	require.EqualValues(t, 1, held)
	terminating := *repo
	now := time.Now().UTC()
	terminating.DeletionTimestamp = &now
	datastore.AdvanceRepositorySystemVersion(&terminating)
	short, cancel := context.WithTimeout(ctx, 60*time.Millisecond)
	require.ErrorIs(t, peer.UpdateRepository(short, &terminating, repo.ResourceVersion), context.DeadlineExceeded)
	cancel()
	unblockFirst()
	for _, result := range results {
		require.NoError(t, <-result)
	}
	afterEpoch, afterPending := readFence()
	require.Equal(t, heldEpoch, afterEpoch, "all four ordered callbacks must share the confirmed reservation")
	require.Zero(t, afterPending)
	require.EqualValues(t, 4, finished.Load())
	current, err := peer.GetFile(ctx, file.UID)
	require.NoError(t, err)
	require.Equal(t, "5", current.ResourceVersion)
	require.EqualValues(t, 1, current.Generation)
	require.Equal(t, "3", current.Annotations["sequence"])
	blocked, err := peer.HasCatalogResources(ctx, repo.UID)
	require.NoError(t, err)
	require.True(t, blocked)
	require.Zero(t, scylla.QueuedNamespaceCatalogWritesForTest(rawFirst, ns.UID))
	if snapshots {
		var cohort sdktrace.ReadOnlySpan
		roots := map[string]sdktrace.ReadOnlySpan{}
		for _, span := range recorder.Ended() {
			if span.Name() == "datastore.FileMutationCohort" {
				require.Nil(t, cohort, "all four callbacks must link to one physical reservation")
				cohort = span
			} else if span.Name() == "file.callback" {
				roots[span.SpanContext().SpanID().String()] = span
			}
		}
		require.NotNil(t, cohort)
		require.Len(t, roots, 4)
		require.Len(t, cohort.Links(), 4)
		for _, link := range cohort.Links() {
			root := roots[link.SpanContext.SpanID().String()]
			require.NotNil(t, root)
			require.Len(t, root.Links(), 1)
			require.Equal(t, cohort.SpanContext(), root.Links()[0].SpanContext)
		}
		physicalPhases := 0
		for _, span := range recorder.Ended() {
			switch span.Name() {
			case "datastore.NamespaceFenceReserve", "datastore.NamespaceFenceMutation", "datastore.NamespaceFenceRelease":
				physicalPhases++
				require.Equal(t, cohort.SpanContext().SpanID(), span.Parent().SpanID())
			}
		}
		require.Equal(t, 3, physicalPhases, "physical fence phases must be direct children of the exact owner span")
	}
}

func TestScylla_FileCohortSharesOneFenceAndSurvivesLeaderCancellation(t *testing.T) {
	testFileCohortSharesOneFenceAndSurvivesLeaderCancellation(t, false)
}

func TestScylla_FileCohortDisjointSameRepositorySharesFence(t *testing.T) {
	testFileCohortSharesOneFenceAndSurvivesLeaderCancellation(t, true)
}

func testFileCohortSharesOneFenceAndSurvivesLeaderCancellation(t *testing.T, sharedRepository bool) {
	t.Helper()
	storeA, storeB := newTestStores(t)
	ctx := t.Context()
	ns := &datastore.Namespace{UID: newID(), Name: "file-cohort-" + newID()[:8]}
	require.NoError(t, storeA.CreateNamespace(ctx, ns))
	repositories := make([]*datastore.Repository, 4)
	for i := range repositories {
		if sharedRepository && i > 0 {
			repositories[i] = repositories[0]
			continue
		}
		repositories[i] = &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: fmt.Sprintf("catalog-%d", i)}
		require.NoError(t, storeA.CreateRepositoryInActiveNamespace(ctx, repositories[i]))
	}
	session, err := openRootSession(scyllaAddr)
	require.NoError(t, err)
	t.Cleanup(session.Close)
	nsUID, err := gocql.ParseUUID(ns.UID)
	require.NoError(t, err)
	readFence := func() (int64, int64) {
		t.Helper()
		var epoch, pending int64
		require.NoError(t, session.Query("SELECT repository_creation_epoch,pending_repository_creations FROM "+
			scyllaKeyspace+".namespaces_by_uid WHERE uid=?", nsUID).Scan(&epoch, &pending))
		return epoch, pending
	}
	beforeEpoch, beforePending := readFence()
	require.Zero(t, beforePending)
	started, releaseInitial := make(chan struct{}), make(chan struct{})
	var initialOnce sync.Once
	release := func() { initialOnce.Do(func() { close(releaseInitial) }) }
	t.Cleanup(release)
	initialDone := make(chan error, 1)
	var running sync.WaitGroup
	running.Go(func() {
		initialDone <- storeA.(datastore.CatalogMutationBatcher).WithCatalogMutationBatch(ctx, ns.Name, func(context.Context) error {
			close(started)
			<-releaseInitial
			return nil
		})
	})
	<-started
	allStarted, releaseMutations := make(chan struct{}), make(chan struct{})
	var mutationsOnce sync.Once
	proceed := func() { mutationsOnce.Do(func() { close(releaseMutations) }) }
	t.Cleanup(proceed)
	type actorKey struct{}
	var active atomic.Int64
	results := make([]chan error, 4)
	files := make([][]*datastore.File, 4)
	leaderCtx, cancelLeader := context.WithCancel(context.WithValue(ctx, actorKey{}, 0))
	defer cancelLeader()
	t.Cleanup(func() {
		release()
		proceed()
		cancelLeader()
		running.Wait()
	})
	for i, repo := range repositories {
		results[i] = make(chan error, 1)
		fileCtx := context.WithValue(ctx, actorKey{}, i)
		if i == 0 {
			fileCtx = leaderCtx
		}
		names := []string{fmt.Sprintf("cohort-%d-first", i), fmt.Sprintf("cohort-%d-second", i)}
		for _, name := range names {
			files[i] = append(files[i], &datastore.File{UID: newID(), Namespace: ns.Name, Name: name,
				RepositoryID: repo.UID, ResourceVersion: "1", CreationTimestamp: time.Now().UTC()})
		}
		running.Go(func() {
			results[i] <- storeA.(datastore.FileMutationBatcher).WithFileMutationBatch(fileCtx, ns.Name, repo.UID, names, func(batchCtx context.Context) error {
				assert.Equal(t, i, batchCtx.Value(actorKey{}))
				if active.Add(1) == 4 {
					close(allStarted)
				}
				<-releaseMutations
				if err := batchCtx.Err(); err != nil {
					return err
				}
				errs := make([]error, len(files[i]))
				var writes sync.WaitGroup
				for j, file := range files[i] {
					writes.Go(func() { errs[j] = storeA.CreateFile(batchCtx, file) })
				}
				writes.Wait()
				return errors.Join(errs...)
			})
		})
		require.Eventually(t, func() bool {
			return scylla.QueuedNamespaceCatalogWritesForTest(storeA, ns.UID) == i+2
		}, time.Second, time.Millisecond)
	}
	release()
	require.NoError(t, <-initialDone)
	select {
	case <-allStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("four queued callbacks did not share the next exclusive reservation")
	}
	cohortEpoch, cohortPending := readFence()
	require.Equal(t, beforeEpoch+2, cohortEpoch)
	require.EqualValues(t, 1, cohortPending)
	terminating := *repositories[1]
	now := time.Now().UTC()
	terminating.DeletionTimestamp = &now
	datastore.AdvanceRepositorySystemVersion(&terminating)
	peerCtx, stopPeer := context.WithTimeout(ctx, 60*time.Millisecond)
	require.ErrorIs(t, storeB.UpdateRepository(peerCtx, &terminating, repositories[1].ResourceVersion), context.DeadlineExceeded,
		"another replica's lifecycle transition cannot enter the shared reservation")
	stopPeer()
	cancelLeader()
	proceed()
	require.ErrorIs(t, <-results[0], context.Canceled)
	for i := 1; i < 4; i++ {
		require.NoError(t, <-results[i])
		for _, file := range files[i] {
			current, err := storeB.GetFile(ctx, file.UID)
			require.NoError(t, err)
			require.Equal(t, file.Name, current.Name)
			require.Equal(t, repositories[i].UID, current.RepositoryID)
		}
		blocked, err := storeB.HasCatalogResources(ctx, repositories[i].UID)
		require.NoError(t, err)
		require.True(t, blocked)
	}
	for _, file := range files[0] {
		_, err := storeB.GetFile(ctx, file.UID)
		require.ErrorIs(t, err, datastore.ErrNotFound)
	}
	afterEpoch, afterPending := readFence()
	require.Equal(t, cohortEpoch, afterEpoch)
	require.Zero(t, afterPending)
	require.Zero(t, scylla.QueuedNamespaceCatalogWritesForTest(storeA, ns.UID))
}

func datastorePhaseCounts(t *testing.T, registry *prometheus.Registry) map[string]uint64 {
	t.Helper()
	families, err := registry.Gather()
	require.NoError(t, err)
	counts := make(map[string]uint64)
	for _, family := range families {
		if family.GetName() != "gitstore_datastore_operation_duration_seconds" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "operation" {
					counts[label.GetValue()] += metric.GetHistogram().GetSampleCount()
				}
			}
		}
	}
	return counts
}

func TestScylla_QueuedFenceCandidatesRequireFreshConditionalAcquisition(t *testing.T) {
	for _, state := range []string{"unchanged", "legacy", "legacy-published", "status", "epoch", "unpublished", "terminating", "intent"} {
		t.Run(state, func(t *testing.T) {
			type candidateKey struct{}
			ctx := context.WithValue(t.Context(), candidateKey{}, true)
			peer := newTestStore(t)
			ns := &datastore.Namespace{UID: newID(), Name: "fence-candidate-" + newID()}
			require.NoError(t, peer.CreateNamespace(ctx, ns))
			uid, err := gocql.ParseUUID(ns.UID)
			require.NoError(t, err)
			raw := newRawSession(t)
			if state == "legacy" || state == "legacy-published" {
				require.NoError(t, raw.Query("UPDATE namespaces_by_uid SET watch_committed=null WHERE uid=?", uid).Exec())
			}
			var initialEpoch int64
			require.NoError(t, raw.Query("SELECT repository_creation_epoch FROM namespaces_by_uid WHERE uid=?", uid).Scan(&initialEpoch))
			changed := make(chan error, 1)
			var once sync.Once
			cfg := testScyllaConfig(t)
			cfg.QueryObserver = namespaceReservationObserver(func(observedCtx context.Context, query gocql.ObservedQuery) {
				if observedCtx.Value(candidateKey{}) != true || query.Err != nil ||
					query.Statement != "UPDATE namespaces_by_uid SET pending_repository_creations=? WHERE uid=? IF repository_creation_epoch=? AND pending_repository_creations=?" {
					return
				}
				once.Do(func() {
					var err error
					switch state {
					case "legacy-published":
						err = raw.Query("UPDATE namespaces_by_uid SET watch_committed=true WHERE uid=?", uid).Exec()
					case "status":
						err = raw.Query("UPDATE namespaces_by_uid SET status=? WHERE uid=?", `{"candidateProbe":"changed"}`, uid).Exec()
					case "epoch":
						err = raw.Query("UPDATE namespaces_by_uid SET repository_creation_epoch=? WHERE uid=?", initialEpoch+100, uid).Exec()
					case "unpublished":
						err = raw.Query("UPDATE namespaces_by_uid SET watch_committed=false WHERE uid=?", uid).Exec()
					case "terminating":
						err = raw.Query("UPDATE namespaces_by_uid SET deletion_timestamp=? WHERE uid=?", time.Now().UTC(), uid).Exec()
					case "intent":
						err = raw.Query("UPDATE namespaces_by_uid SET status=? WHERE uid=?", `{"deletionIntent":{"uid":"intent"}}`, uid).Exec()
					}
					changed <- err
				})
			})
			store, err := scylla.New(cfg, zap.NewNop())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Close()) })
			registry := prometheus.NewRegistry()
			batcher := datastore.NewInstrumentedDatastoreWithRegistry(store, "scylla", zap.NewNop(), registry).(datastore.CatalogMutationBatcher)
			started, proceed := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(proceed) }) }
			t.Cleanup(release)
			firstDone, secondDone := make(chan error, 1), make(chan error, 1)
			go func() {
				firstDone <- batcher.WithCatalogMutationBatch(ctx, ns.Name, func(context.Context) error {
					close(started)
					<-proceed
					return nil
				})
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("initial reservation did not start")
			}
			var called atomic.Bool
			go func() {
				secondDone <- batcher.WithCatalogMutationBatch(ctx, ns.Name, func(context.Context) error {
					called.Store(true)
					return nil
				})
			}()
			require.Eventually(t, func() bool {
				return scylla.QueuedNamespaceCatalogWritesForTest(store, ns.UID) == 2
			}, 5*time.Second, time.Millisecond)
			release()
			require.NoError(t, <-firstDone)
			require.NoError(t, <-changed)
			err = <-secondDone
			switch state {
			case "unpublished":
				require.ErrorIs(t, err, datastore.ErrNotFound)
			case "terminating", "intent":
				require.ErrorIs(t, err, datastore.ErrNamespaceNotActive)
			default:
				require.NoError(t, err)
			}
			active := state != "unpublished" && state != "terminating" && state != "intent"
			require.Equal(t, active, called.Load())
			counts := datastorePhaseCounts(t, registry)
			wantReads, wantCAS := uint64(2), uint64(2)
			if state == "unchanged" || state == "legacy" {
				wantReads = 1
			} else if active {
				wantCAS = 3
			}
			require.Equal(t, wantReads, counts["NamespaceFenceReserveRead"])
			require.Equal(t, wantCAS, counts["NamespaceFenceReserveCAS"])
			var pending int64
			require.NoError(t, raw.Query("SELECT pending_repository_creations FROM namespaces_by_uid WHERE uid=?", uid).Scan(&pending))
			require.Zero(t, pending)
			if active {
				require.NoError(t, batcher.WithCatalogMutationBatch(ctx, ns.Name, func(context.Context) error { return nil }))
				counts = datastorePhaseCounts(t, registry)
				require.Equal(t, wantReads+1, counts["NamespaceFenceReserveRead"], "a drained queue cannot retain a candidate")
				require.Equal(t, wantCAS+1, counts["NamespaceFenceReserveCAS"])
			}
		})
	}
}

func TestScylla_CatalogReservationSeparatesBusyAndConflictWaits(t *testing.T) {
	for _, state := range []string{"active", "busy", "conflict"} {
		t.Run(state, func(t *testing.T) {
			type reservationKey struct{}
			ctx := context.WithValue(t.Context(), reservationKey{}, true)
			peer := newTestStore(t)
			ns := &datastore.Namespace{UID: newID(), Name: "reserve-phases-" + newID()}
			require.NoError(t, peer.CreateNamespace(ctx, ns))
			uid, err := gocql.ParseUUID(ns.UID)
			require.NoError(t, err)
			raw := newRawSession(t)
			var initialEpoch int64
			require.NoError(t, raw.Query("SELECT repository_creation_epoch FROM namespaces_by_uid WHERE uid=?", uid).Scan(&initialEpoch))
			if state == "busy" {
				require.NoError(t, raw.Query("UPDATE namespaces_by_uid SET pending_repository_creations=? WHERE uid=?", int64(1), uid).Exec())
			}
			var changed sync.Once
			var changeErr error
			cfg := testScyllaConfig(t)
			cfg.QueryObserver = namespaceReservationObserver(func(observedCtx context.Context, query gocql.ObservedQuery) {
				if observedCtx.Value(reservationKey{}) != true ||
					!strings.HasPrefix(query.Statement, "SELECT repository_creation_epoch, pending_repository_creations, deletion_timestamp, status, watch_committed ") {
					return
				}
				changed.Do(func() {
					switch state {
					case "busy":
						changeErr = raw.Query("UPDATE namespaces_by_uid SET pending_repository_creations=? WHERE uid=?", int64(0), uid).Exec()
					case "conflict":
						changeErr = raw.Query("UPDATE namespaces_by_uid SET repository_creation_epoch=? WHERE uid=?", initialEpoch+1, uid).Exec()
					}
				})
			})
			store, err := scylla.New(cfg, zap.NewNop())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Close()) })
			registry := prometheus.NewRegistry()
			batcher := datastore.NewInstrumentedDatastoreWithRegistry(store, "scylla", zap.NewNop(), registry).(datastore.CatalogMutationBatcher)
			called := false
			require.NoError(t, batcher.WithCatalogMutationBatch(ctx, ns.Name, func(context.Context) error {
				called = true
				return nil
			}))
			require.True(t, called)
			require.NoError(t, changeErr)
			counts := datastorePhaseCounts(t, registry)
			require.EqualValues(t, 1, counts["NamespaceFenceReserve"])
			require.EqualValues(t, 1, counts["NamespaceFenceRelease"])
			wantReads, wantCAS, wantBusy, wantConflict := uint64(1), uint64(1), uint64(0), uint64(0)
			switch state {
			case "busy":
				wantReads, wantBusy = 2, 1
			case "conflict":
				wantReads, wantCAS, wantConflict = 2, 2, 1
			}
			require.Equal(t, wantReads, counts["NamespaceFenceReserveRead"])
			require.Equal(t, wantCAS, counts["NamespaceFenceReserveCAS"])
			require.Equal(t, wantBusy, counts["NamespaceFenceReserveBusyWait"])
			require.Equal(t, wantConflict, counts["NamespaceFenceReserveConflictWait"])
			var epoch, pending int64
			require.NoError(t, raw.Query("SELECT repository_creation_epoch, pending_repository_creations FROM namespaces_by_uid WHERE uid=?", uid).Scan(&epoch, &pending))
			require.Zero(t, pending)
			wantEpoch := initialEpoch + 1
			if state == "conflict" {
				wantEpoch++
			}
			require.Equal(t, wantEpoch, epoch)
		})
	}
}

func TestScylla_CatalogFencePublicationSharesFreshReservationRead(t *testing.T) {
	for _, fileBatch := range []bool{false, true} {
		for _, state := range []string{"active", "legacy", "staged", "staged-partial", "dangling", "absent", "terminating", "intent", "concurrent-termination", "concurrent-intent", "cancel", "unpublished-while-queued", "unpublished-before-cas", "legacy-published-before-cas"} {
			t.Run(fmt.Sprintf("file=%t/%s", fileBatch, state), func(t *testing.T) {
				type lookupKey struct{}
				ctx, cancel := context.WithCancel(context.WithValue(t.Context(), lookupKey{}, true))
				defer cancel()
				ns := &datastore.Namespace{UID: newID(), Name: "fence-identity-" + newID()}
				uid, err := gocql.ParseUUID(ns.UID)
				require.NoError(t, err)
				peer := newTestStore(t)
				raw := newRawSession(t)
				if state != "absent" {
					require.NoError(t, peer.CreateNamespace(t.Context(), ns))
				}
				switch state {
				case "legacy", "legacy-published-before-cas":
					require.NoError(t, raw.Query("UPDATE namespaces_by_uid SET watch_committed=null WHERE uid=?", uid).Exec())
				case "staged":
					require.NoError(t, raw.Query("UPDATE namespaces_by_uid SET watch_committed=false WHERE uid=?", uid).Exec())
				case "staged-partial":
					require.NoError(t, raw.Query("UPDATE namespaces_by_uid SET watch_committed=false, repository_creation_epoch=null, pending_repository_creations=null WHERE uid=?", uid).Exec())
				case "dangling":
					require.NoError(t, raw.Query("DELETE FROM namespaces_by_uid WHERE uid=?", uid).Exec())
				case "terminating", "intent":
					desired := *ns
					if state == "intent" {
						desired.Status = json.RawMessage(`{"deletionIntent":{"uid":"intent"},"conditions":[{"type":"DeletionPending","status":"True"}]}`)
					} else {
						now := time.Now().UTC()
						desired.DeletionTimestamp = &now
					}
					datastore.AdvanceNamespaceSystemVersion(&desired)
					require.NoError(t, peer.UpdateNamespace(t.Context(), &desired, ns.ResourceVersion))
				}
				var names, markers, fullReads, fences, unpublishedFences atomic.Int64
				var mutationOnce sync.Once
				peerResult := make(chan error, 1)
				cfg := testScyllaConfig(t)
				cfg.QueryObserver = namespaceReservationObserver(func(observedCtx context.Context, query gocql.ObservedQuery) {
					if observedCtx.Value(lookupKey{}) != true || !strings.HasPrefix(query.Statement, "SELECT ") {
						return
					}
					if strings.Contains(query.Statement, " FROM namespaces_by_name WHERE name=?") {
						names.Add(1)
						if state == "cancel" {
							cancel()
						}
						if state == "concurrent-termination" || state == "concurrent-intent" {
							mutationOnce.Do(func() {
								desired := *ns
								if state == "concurrent-intent" {
									desired.Status = json.RawMessage(`{"deletionIntent":{"uid":"intent"},"conditions":[{"type":"DeletionPending","status":"True"}]}`)
								} else {
									now := time.Now().UTC()
									desired.DeletionTimestamp = &now
								}
								datastore.AdvanceNamespaceSystemVersion(&desired)
								peerResult <- peer.UpdateNamespace(t.Context(), &desired, ns.ResourceVersion)
							})
						}
					}
					if !strings.Contains(query.Statement, " FROM namespaces_by_uid WHERE uid=?") {
						return
					}
					if strings.HasPrefix(query.Statement, "SELECT repository_creation_epoch, pending_repository_creations, deletion_timestamp, status") {
						fences.Add(1)
						if !strings.Contains(query.Statement, "watch_committed") {
							unpublishedFences.Add(1)
						}
						if state == "unpublished-before-cas" || state == "legacy-published-before-cas" {
							mutationOnce.Do(func() {
								peerResult <- raw.Query("UPDATE namespaces_by_uid SET watch_committed=? WHERE uid=?",
									state == "legacy-published-before-cas", uid).Exec()
							})
						}
					} else if query.Statement == "SELECT watch_committed FROM namespaces_by_uid WHERE uid=?" {
						markers.Add(1)
					} else {
						fullReads.Add(1)
					}
				})
				store, err := scylla.New(cfg, zap.NewNop())
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, store.Close()) })
				called := false
				operation := func(context.Context) error {
					called = true
					return nil
				}
				apply := func() error {
					if fileBatch {
						return store.(datastore.FileMutationBatcher).WithFileMutationBatch(ctx, ns.Name, newID(), []string{"file"}, operation)
					}
					return store.(datastore.CatalogMutationBatcher).WithCatalogMutationBatch(ctx, ns.Name, operation)
				}
				if state == "unpublished-while-queued" {
					started, resume := make(chan struct{}), make(chan struct{})
					var released sync.Once
					release := func() { released.Do(func() { close(resume) }) }
					var workers sync.WaitGroup
					t.Cleanup(func() { release(); workers.Wait() })
					holder := make(chan error, 1)
					workers.Go(func() {
						holder <- store.(datastore.CatalogMutationBatcher).WithCatalogMutationBatch(t.Context(), ns.Name, func(context.Context) error {
							close(started)
							<-resume
							return nil
						})
					})
					select {
					case <-started:
					case <-time.After(5 * time.Second):
						t.Fatal("initial reservation did not start")
					}
					result := make(chan error, 1)
					workers.Go(func() { result <- apply() })
					require.Eventually(t, func() bool {
						return scylla.QueuedNamespaceCatalogWritesForTest(store, ns.UID) == 2
					}, time.Second, time.Millisecond)
					require.NoError(t, raw.Query("UPDATE namespaces_by_uid SET watch_committed=false WHERE uid=?", uid).Exec())
					release()
					require.NoError(t, <-holder)
					err = <-result
				} else {
					err = apply()
				}
				switch state {
				case "active", "legacy", "legacy-published-before-cas":
					require.NoError(t, err)
					require.True(t, called)
				case "staged", "staged-partial", "dangling", "absent", "unpublished-while-queued", "unpublished-before-cas":
					require.ErrorIs(t, err, datastore.ErrNotFound)
					require.False(t, called)
				case "cancel":
					require.ErrorIs(t, err, context.Canceled)
					require.False(t, called)
				default:
					require.ErrorIs(t, err, datastore.ErrNamespaceNotActive)
					require.False(t, called)
				}
				if state == "concurrent-termination" || state == "concurrent-intent" || state == "unpublished-before-cas" || state == "legacy-published-before-cas" {
					require.NoError(t, <-peerResult, "actual peer lifecycle and publication transitions must retain their ordering")
				}
				require.EqualValues(t, 1, names.Load())
				require.Zero(t, fullReads.Load(), "catalog fencing needs published identity, not the full Namespace envelope")
				require.Zero(t, markers.Load(), "publication must share the existing fresh reservation read, not a separate pre-queue query")
				require.Zero(t, unpublishedFences.Load(), "the fresh authoritative fence read must include publication state")
				switch state {
				case "absent", "cancel":
					require.Zero(t, fences.Load())
				case "unpublished-before-cas", "legacy-published-before-cas":
					require.EqualValues(t, 2, fences.Load(), "publication changes must lose the old CAS and be freshly checked")
				default:
					require.EqualValues(t, 1, fences.Load())
				}
				if state == "unpublished-before-cas" || state == "unpublished-while-queued" {
					var epoch, pending int64
					require.NoError(t, raw.Query("SELECT repository_creation_epoch,pending_repository_creations FROM namespaces_by_uid WHERE uid=?", uid).Scan(&epoch, &pending))
					expectedEpoch := int64(0)
					if state == "unpublished-while-queued" {
						expectedEpoch = 1
					}
					require.Equal(t, expectedEpoch, epoch, "rejected publication must not acquire or advance a reservation")
					require.Zero(t, pending, "rejected publication must not strand a reservation")
				}
			})
		}
	}
}

func TestScylla_FileBatchActiveNamespaceProofBlocksPeerLifecycle(t *testing.T) {
	first, second := newTestStores(t)
	namespace := &datastore.Namespace{UID: newID(), Name: "batch-parent-" + newID()}
	require.NoError(t, first.CreateNamespace(t.Context(), namespace))
	version := namespace.ResourceVersion
	require.NoError(t, first.(datastore.CatalogMutationBatcher).WithCatalogMutationBatch(t.Context(), namespace.Name, func(ctx context.Context) error {
		for _, intent := range []bool{false, true} {
			desired := *namespace
			if intent {
				desired.Status = json.RawMessage(`{"deletionIntent":{"uid":"intent"},"conditions":[{"type":"DeletionPending","status":"True"}]}`)
			} else {
				now := time.Now().UTC()
				desired.DeletionTimestamp = &now
			}
			datastore.AdvanceNamespaceSystemVersion(&desired)
			require.ErrorIs(t, second.UpdateNamespace(ctx, &desired, version), datastore.ErrNamespaceNotEmpty,
				"a held Namespace reservation must block peer lifecycle transitions even without repositories or children")
			current, err := second.GetNamespace(ctx, namespace.UID)
			require.NoError(t, err)
			require.Equal(t, version, current.ResourceVersion)
			require.Nil(t, current.DeletionTimestamp)
			require.False(t, datastore.HasDeletionIntent(current.Status))
		}
		return nil
	}))
	desired := *namespace
	now := time.Now().UTC()
	desired.DeletionTimestamp = &now
	datastore.AdvanceNamespaceSystemVersion(&desired)
	require.NoError(t, second.UpdateNamespace(t.Context(), &desired, version),
		"releasing the reservation must permit lifecycle transitions for an otherwise-empty Namespace")
	require.ErrorIs(t, first.(datastore.CatalogMutationBatcher).WithCatalogMutationBatch(t.Context(), namespace.Name, func(context.Context) error {
		t.Fatal("a terminating Namespace must not admit an active batch")
		return nil
	}), datastore.ErrNamespaceNotActive)
}

func TestScylla_FileUpdateSkipsUnchangedColumns(t *testing.T) {
	cfg := testScyllaConfig(t)
	cfg.QueryObserver = capacityQueryObserver{}
	first, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, first.Close()) })
	second := newTestStore(t)
	file := &datastore.File{
		UID: newID(), Namespace: "selective-file-" + newID(), Name: "file",
		APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File", Generation: 1, ResourceVersion: "1",
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond), CreationActor: "creator",
		Labels: map[string]string{"kept": "value"}, Annotations: map[string]string{"previous": "value"},
		Finalizers: []string{"example"}, OwnerReferences: json.RawMessage(`[]`),
		SourcePath: "file.json", GitCommitSHA: "before", GitRef: "main", Body: "before",
		Spec: json.RawMessage(`{"path":"example"}`), Status: json.RawMessage(`{"resolved":{}}`),
	}
	require.NoError(t, first.CreateNamespace(t.Context(), &datastore.Namespace{UID: newID(), Name: file.Namespace}))
	require.NoError(t, first.CreateFile(t.Context(), file))
	before, err := second.GetFile(t.Context(), file.UID)
	require.NoError(t, err)
	desired := *before
	desired.Body, desired.GitCommitSHA = "after", "after"
	desired.Generation++
	datastore.AdvanceFileSystemVersion(&desired)
	evidence := &capacityQueryEvidence{tables: make(map[string]int)}
	ctx := context.WithValue(t.Context(), capacityQueryKey{}, evidence)
	require.NoError(t, first.UpdateFile(ctx, &desired, before.ResourceVersion))
	current, err := second.GetFile(t.Context(), file.UID)
	require.NoError(t, err)
	require.Equal(t, &desired, current, "selective bindings must retain the complete current envelope")
	evidence.mu.Lock()
	written := make(map[int]int, len(evidence.fileCASFields))
	for column, count := range evidence.fileCASFields {
		written[column] = count
	}
	evidence.mu.Unlock()
	require.NotEmpty(t, written, "observe the actual authoritative CAS bindings")
	require.Zero(t, written[0], "unchanged name must not be rewritten")
	require.Zero(t, written[9], "unchanged labels must not be rewritten")
	require.Zero(t, written[12], "unchanged finalizers must not be rewritten")
	require.Equal(t, 1, written[4], "resourceVersion must remain an explicit CAS write")
	require.Equal(t, 1, written[16], "changed commit must be written")
	require.Equal(t, 1, written[19], "changed body must be written")
	require.Zero(t, written[11], "unchanged nonempty serialized JSON must not be rewritten")
	require.Zero(t, written[18])
	require.Zero(t, written[20])
	cleared := *current
	cleared.Labels, cleared.Annotations, cleared.Finalizers = nil, nil, nil
	cleared.OwnerReferences, cleared.Spec, cleared.Status = nil, nil, nil
	cleared.Body, cleared.GitCommitSHA = "", ""
	datastore.AdvanceFileSystemVersion(&cleared)
	require.NoError(t, first.UpdateFile(t.Context(), &cleared, current.ResourceVersion))
	after, err := second.GetFile(t.Context(), file.UID)
	require.NoError(t, err)
	require.Equal(t, &cleared, after, "explicit removals must be written, never treated as unchanged")
	stale := *before
	stale.Body = "stale"
	datastore.AdvanceFileSystemVersion(&stale)
	require.ErrorIs(t, second.UpdateFile(t.Context(), &stale, before.ResourceVersion), datastore.ErrConflict)
	unchanged, err := first.GetFile(t.Context(), file.UID)
	require.NoError(t, err)
	require.Equal(t, after, unchanged, "a stale version cannot partially change selective columns")
	raw := newRawSession(t)
	t.Cleanup(raw.Close)
	uid, err := gocql.ParseUUID(file.UID)
	require.NoError(t, err)
	require.NoError(t, raw.Query("UPDATE files_by_namespace SET owner_references=?,spec=?,status=? WHERE namespace=? AND creation_timestamp=? AND uid=?",
		nil, nil, nil, file.Namespace, file.CreationTimestamp, uid).Exec())
	repair, err := second.GetFile(t.Context(), file.UID)
	require.NoError(t, err)
	require.Nil(t, repair.OwnerReferences)
	require.Nil(t, repair.Spec)
	require.Nil(t, repair.Status)
	expected := repair.ResourceVersion
	datastore.AdvanceFileSystemVersion(repair)
	require.NoError(t, first.UpdateFile(t.Context(), repair, expected))
	var owners, spec, status *string
	require.NoError(t, raw.Query("SELECT owner_references,spec,status FROM files_by_namespace WHERE namespace=? AND creation_timestamp=? AND uid=?",
		file.Namespace, file.CreationTimestamp, uid).Scan(&owners, &spec, &status))
	require.NotNil(t, owners, "unchanged logical JSON absence must still normalize CQL null to empty text")
	require.NotNil(t, spec)
	require.NotNil(t, status)
	require.Empty(t, *owners)
	require.Empty(t, *spec)
	require.Empty(t, *status)
}

func TestScylla_FileBatchReusesOneFenceWithoutWeakeningExclusion(t *testing.T) {
	cfg := testScyllaConfig(t)
	cfg.QueryObserver = capacityQueryObserver{}
	storeA, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storeA.Close()) })
	storeB := newTestStore(t)
	ctx := t.Context()
	ns := &datastore.Namespace{UID: newID(), Name: "file-batch-" + newID()[:8]}
	require.NoError(t, storeA.CreateNamespace(ctx, ns))
	repo := &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: "catalog"}
	require.NoError(t, storeA.CreateRepositoryInActiveNamespace(ctx, repo))
	session, err := openRootSession(scyllaAddr)
	require.NoError(t, err)
	t.Cleanup(session.Close)
	nsUID, err := gocql.ParseUUID(ns.UID)
	require.NoError(t, err)
	readFence := func() (int64, int64) {
		t.Helper()
		var epoch, pending int64
		require.NoError(t, session.Query("SELECT repository_creation_epoch,pending_repository_creations FROM "+scyllaKeyspace+".namespaces_by_uid WHERE uid=?", nsUID).Scan(&epoch, &pending))
		return epoch, pending
	}
	epoch, pending := readFence()
	require.Zero(t, pending)
	files := []*datastore.File{
		{UID: newID(), Namespace: ns.Name, Name: "first", RepositoryID: repo.UID, ResourceVersion: "1", CreationTimestamp: time.Now().UTC()},
		{UID: newID(), Namespace: ns.Name, Name: "second", RepositoryID: repo.UID, ResourceVersion: "1", CreationTimestamp: time.Now().UTC()},
	}
	var retained context.Context
	registry := prometheus.NewRegistry()
	batcher := datastore.NewInstrumentedDatastoreWithRegistry(storeA, "scylla", zap.NewNop(), registry).(datastore.CatalogMutationBatcher)
	createEvidence := &capacityQueryEvidence{tables: map[string]int{}}
	createCtx := context.WithValue(ctx, capacityQueryKey{}, createEvidence)
	require.NoError(t, batcher.WithCatalogMutationBatch(createCtx, ns.Name, func(batchCtx context.Context) error {
		retained = batchCtx
		errs := make([]error, len(files))
		var wg sync.WaitGroup
		for i, file := range files {
			wg.Go(func() { errs[i] = storeA.CreateFile(batchCtx, file) })
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			return err
		}
		_, pending := readFence()
		require.EqualValues(t, 1, pending, "nested File writes must not release the outer reservation")
		terminating := *repo
		now := time.Now().UTC()
		terminating.DeletionTimestamp = &now
		datastore.AdvanceRepositorySystemVersion(&terminating)
		require.ErrorIs(t, storeA.UpdateRepository(batchCtx, &terminating, repo.ResourceVersion), datastore.ErrInvalidArgument,
			"parent lifecycle writes cannot reuse a child admission reservation")
		require.ErrorIs(t, storeA.DeleteFile(batchCtx, files[0].UID), datastore.ErrInvalidArgument)
		peerCtx, cancel := context.WithTimeout(batchCtx, 60*time.Millisecond)
		defer cancel()
		peer := &datastore.File{UID: newID(), Namespace: ns.Name, Name: "peer", RepositoryID: repo.UID, ResourceVersion: "1", CreationTimestamp: time.Now().UTC()}
		require.ErrorIs(t, storeB.CreateFile(peerCtx, peer), context.DeadlineExceeded,
			"a context issued by another datastore replica must not bypass its own fence")
		lifecycleCtx, stop := context.WithTimeout(batchCtx, 60*time.Millisecond)
		defer stop()
		require.ErrorIs(t, storeB.UpdateRepository(lifecycleCtx, &terminating, repo.ResourceVersion), context.DeadlineExceeded,
			"the held reservation must keep the checked Repository active across peer lifecycle writes")
		return nil
	}))
	afterEpoch, afterPending := readFence()
	require.Equal(t, epoch+1, afterEpoch, "one reservation must cover both File writes")
	require.Zero(t, afterPending)
	createEvidence.mu.Lock()
	require.Equal(t, 1, createEvidence.tables["namespaces_by_name"], "the held active Namespace reservation must avoid redundant parent-name lookups")
	require.Equal(t, 2, createEvidence.tables["repositories_by_uid"], "the File writes share one checked parent; the lifecycle rejection reads its own current row")
	require.Zero(t, createEvidence.errors)
	createEvidence.mu.Unlock()
	blocked, err := storeB.HasCatalogResources(ctx, repo.UID)
	require.NoError(t, err)
	require.True(t, blocked)
	beforeUpdateEpoch, _ := readFence()
	updateEvidence := &capacityQueryEvidence{tables: map[string]int{}}
	updateCtx := context.WithValue(ctx, capacityQueryKey{}, updateEvidence)
	require.NoError(t, batcher.WithCatalogMutationBatch(updateCtx, ns.Name, func(batchCtx context.Context) error {
		errs := make([]error, len(files))
		var wg sync.WaitGroup
		for i, file := range files {
			wg.Go(func() {
				desired := *file
				desired.Annotations = map[string]string{"sequence": "1"}
				desired.CreationTimestamp = file.CreationTimestamp.Add(time.Hour)
				datastore.AdvanceFileSystemVersion(&desired)
				errs[i] = storeA.UpdateFile(batchCtx, &desired, file.ResourceVersion)
			})
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			return err
		}
		for _, file := range files {
			current, err := storeB.GetFile(batchCtx, file.UID)
			if err != nil {
				return err
			}
			require.Equal(t, "1", current.Annotations["sequence"])
			require.Equal(t, file.CreationTimestamp.UnixMilli(), current.CreationTimestamp.UnixMilli(), "updates preserve the immutable authoritative key")
			stale := *file
			datastore.AdvanceFileSystemVersion(&stale)
			require.ErrorIs(t, storeA.UpdateFile(batchCtx, &stale, file.ResourceVersion), datastore.ErrConflict)
		}
		return nil
	}))
	afterUpdateEpoch, afterUpdatePending := readFence()
	require.Equal(t, beforeUpdateEpoch+1, afterUpdateEpoch)
	require.Zero(t, afterUpdatePending)
	updateEvidence.mu.Lock()
	require.Equal(t, 1, updateEvidence.tables["namespaces_by_name"], "the held active Namespace reservation must avoid redundant parent-name lookups")
	require.Equal(t, 1, updateEvidence.tables["repositories_by_uid"], "updates and stale CAS attempts share only the parent eligibility check within the held reservation")
	require.Equal(t, 4, updateEvidence.tables["files_by_uid"], "each update and stale attempt resolves the UID once, then reads back its exact immutable authoritative key")
	require.Equal(t, 4, updateEvidence.tables["files_by_namespace"]-updateEvidence.fileProjectionReads, "four attempts retain fresh authoritative before reads")
	require.Equal(t, 4, updateEvidence.fileProjectionKeys, "four attempts retain fresh readback keys, including coalesced queries")
	require.Equal(t, 4, updateEvidence.inserts["resources_by_repository"], "each update must repair its full conservative blocker once; identical readback must not rewrite it")
	require.Zero(t, updateEvidence.errors)
	updateEvidence.mu.Unlock()
	currentFiles := make([]*datastore.File, len(files))
	for i, file := range files {
		currentFiles[i], err = storeB.GetFile(ctx, file.UID)
		require.NoError(t, err)
		datastore.AdvanceFileSystemVersion(currentFiles[i])
	}
	keyEvidence := &capacityQueryEvidence{tables: map[string]int{}}
	keyCtx := context.WithValue(ctx, capacityQueryKey{}, keyEvidence)
	require.NoError(t, batcher.WithCatalogMutationBatch(keyCtx, ns.Name, func(batchCtx context.Context) error {
		errs := make([]error, len(currentFiles))
		var wg sync.WaitGroup
		for i, file := range currentFiles {
			wg.Go(func() { errs[i] = storeA.UpdateFile(batchCtx, file, "2") })
		}
		wg.Wait()
		return errors.Join(errs...)
	}))
	keyEvidence.mu.Lock()
	require.Equal(t, 1, keyEvidence.namespaceFenceReads, "a confirmed exclusive reservation must not reread its unchanged fence on release")
	require.Equal(t, 1, keyEvidence.namespaceReleaseCASAttempts, "release must use one conditional attempt when the held fence is unchanged")
	require.Equal(t, 2, keyEvidence.tables["files_by_uid"], "each update resolves its immutable target through the datastore-owned UID locator")
	require.Equal(t, 2, keyEvidence.tables["files_by_namespace"]-keyEvidence.fileProjectionReads, "each update retains its fresh authoritative before read")
	require.Equal(t, 2, keyEvidence.fileProjectionKeys, "each update retains a fresh readback key, including coalesced queries")
	require.Equal(t, 2, keyEvidence.inserts["resources_by_repository"], "each update still repairs its own conservative blocker")
	require.Zero(t, keyEvidence.errors)
	keyEvidence.mu.Unlock()
	for _, file := range currentFiles {
		current, err := storeB.GetFile(ctx, file.UID)
		require.NoError(t, err)
		require.Equal(t, "3", current.ResourceVersion)
		require.Equal(t, file.Generation, current.Generation)
		require.Equal(t, file.CreationTimestamp, current.CreationTimestamp)
		require.Equal(t, file.Annotations, current.Annotations)
	}
	families, err := registry.Gather()
	require.NoError(t, err)
	phaseCounts := make(map[string]uint64)
	for _, family := range families {
		if family.GetName() != "gitstore_datastore_operation_duration_seconds" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "operation" {
					phaseCounts[label.GetValue()] = metric.GetHistogram().GetSampleCount()
				}
			}
		}
	}
	require.EqualValues(t, 8, phaseCounts["CatalogParentCheck"])
	require.EqualValues(t, 8, phaseCounts["CatalogProjectionReadBefore"])
	require.EqualValues(t, 8, phaseCounts["CatalogProjectionReadAfter"])
	require.EqualValues(t, 10, phaseCounts["CatalogMembershipWrite"])
	require.EqualValues(t, 4, phaseCounts["FileAuthoritativeCAS"])
	require.EqualValues(t, 4, phaseCounts["FileOwnerReferenceSync"])
	require.EqualValues(t, 13, phaseCounts["FileUIDLocatorRead"], "include create/readback, updates, peer reads and the rejected delete's initial lookup")
	blocked, err = storeB.HasCatalogResources(ctx, repo.UID)
	require.NoError(t, err)
	require.True(t, blocked, "same-path CAS updates must retain repository deletion blockers")
	require.NoError(t, session.Query("UPDATE "+scyllaKeyspace+".namespaces_by_uid SET pending_repository_creations=? WHERE uid=?", int64(1), nsUID).Exec())
	short, cancel := context.WithTimeout(retained, 60*time.Millisecond)
	err = storeA.CreateFile(short, &datastore.File{UID: newID(), Namespace: ns.Name, Name: "escaped", RepositoryID: repo.UID, ResourceVersion: "1", CreationTimestamp: time.Now().UTC()})
	cancel()
	require.ErrorIs(t, err, context.DeadlineExceeded, "a retained context must not outlive its batch reservation")
	require.NoError(t, session.Query("UPDATE "+scyllaKeyspace+".namespaces_by_uid SET pending_repository_creations=? WHERE uid=?", int64(0), nsUID).Exec())
	sentinel := fmt.Errorf("batch operation failed")
	require.ErrorIs(t, batcher.WithCatalogMutationBatch(ctx, ns.Name, func(context.Context) error { return sentinel }), sentinel)
	_, pending = readFence()
	require.Zero(t, pending, "an unambiguous failure must release the parent reservation")
	canceled, stop := context.WithCancel(ctx)
	require.ErrorIs(t, batcher.WithCatalogMutationBatch(canceled, ns.Name, func(context.Context) error {
		stop()
		return canceled.Err()
	}), context.Canceled)
	_, pending = readFence()
	require.Zero(t, pending, "caller cancellation must not strand a confirmed reservation")
	repair := datastore.NewRepairRequiredError(datastore.MutationStep{Operation: "create_file", ResourceKind: "File"},
		fmt.Errorf("ambiguous File membership write"), nil)
	require.ErrorIs(t, batcher.WithCatalogMutationBatch(ctx, ns.Name, func(context.Context) error { return repair }), datastore.ErrRepairRequired)
	_, pending = readFence()
	require.EqualValues(t, 1, pending, "ambiguous mutation outcomes must retain the reservation for repair")
}

type namespaceReservationObserver func(context.Context, gocql.ObservedQuery)

func (observe namespaceReservationObserver) ObserveQuery(ctx context.Context, query gocql.ObservedQuery) {
	observe(ctx, query)
}

func TestScylla_FileConditionalBatchesPreserveEnvelopesAndIndependentConflicts(t *testing.T) {
	testFileConditionalBatches(t, false)
}

func TestScylla_FileConditionalServerTraceRoundTrip(t *testing.T) {
	testFileConditionalBatches(t, true)
}

func testFileConditionalBatches(t *testing.T, nativeTrace bool) {
	t.Helper()
	raw := newRawSession(t)
	var replication map[string]string
	require.NoError(t, raw.Query("SELECT replication FROM system_schema.keyspaces WHERE keyspace_name=?", scyllaKeyspace).Scan(&replication))
	require.Contains(t, replication["class"], "NetworkTopologyStrategy")
	for name, factor := range replication {
		if name != "class" {
			require.Equal(t, strconv.Itoa(scyllaReplicationFactor), factor)
		}
	}
	t.Logf("Native CQL trace keyspace %s replication=%v", scyllaKeyspace, replication)
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprintf("conflict-%t", conflict), func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			previous := otel.GetTracerProvider()
			otel.SetTracerProvider(provider)
			t.Cleanup(func() {
				otel.SetTracerProvider(previous)
				require.NoError(t, provider.Shutdown(context.Background()))
			})
			const count = 16
			type updateKey struct{}
			started, release := make(chan struct{}), make(chan struct{})
			var held, resumed sync.Once
			var reads atomic.Int64
			cfg := testScyllaConfig(t)
			cfg.QueryObserver = namespaceReservationObserver(func(ctx context.Context, query gocql.ObservedQuery) {
				if ctx.Value(updateKey{}) != true || query.Err != nil {
					return
				}
				if strings.Contains(query.Statement, " FROM files_by_namespace WHERE namespace=? AND creation_timestamp=? AND uid=?") {
					reads.Add(1)
				}
				if strings.HasPrefix(query.Statement, "UPDATE files_by_namespace SET name=") {
					held.Do(func() {
						close(started)
						select {
						case <-release:
						case <-ctx.Done():
						}
					})
				}
			})
			store, err := scylla.New(cfg, zap.NewNop())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Close()) })
			peer := newTestStore(t)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			ctx, parent := provider.Tracer("native-cql-proof").Start(ctx, "conditional-files")
			defer parent.End()
			ns := &datastore.Namespace{UID: newID(), Name: "conditional-files-" + newID()[:8]}
			require.NoError(t, store.CreateNamespace(ctx, ns))
			repo := &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: "catalog"}
			require.NoError(t, store.CreateRepositoryInActiveNamespace(ctx, repo))
			desired := make([]*datastore.File, count)
			created := time.Now().UTC().Truncate(time.Millisecond)
			for i := range desired {
				file := &datastore.File{
					UID: newID(), Namespace: ns.Name, Name: fmt.Sprintf("file-%d", i), RepositoryID: repo.UID,
					APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File", Generation: 1, ResourceVersion: "1",
					CreationTimestamp: created, CreationActor: "creator", Labels: map[string]string{"stable": "label"},
					Annotations: map[string]string{"old": "annotation"}, Finalizers: []string{"old-finalizer"},
					Spec: json.RawMessage(`{"old":true}`), Status: json.RawMessage(`{"old":true}`),
					SourcePath: fmt.Sprintf("files/%d.yaml", i), GitCommitSHA: "old-sha", GitRef: "refs/heads/main",
					Body: "old-body",
				}
				require.NoError(t, store.CreateFile(ctx, file))
				current, err := peer.GetFile(ctx, file.UID)
				require.NoError(t, err)
				current.CreationTimestamp = created.Add(time.Hour)
				current.Generation++
				current.UpdateActor = fmt.Sprintf("actor-%d", i)
				current.Annotations = nil
				current.Finalizers = nil
				current.Spec, current.Status, current.OwnerReferences = nil, nil, nil
				current.Body = fmt.Sprintf("body-%d", i)
				current.GitCommitSHA = fmt.Sprintf("sha-%d", i)
				datastore.AdvanceFileSystemVersion(current)
				desired[i] = current
			}
			registry := prometheus.NewRegistry()
			batcher := datastore.NewInstrumentedDatastoreWithRegistry(store, "scylla", zap.NewNop(), registry).(datastore.CatalogMutationBatcher)
			resume := func() { resumed.Do(func() { close(release) }) }
			t.Cleanup(resume)
			var results [count]error
			if nativeTrace {
				finishCapture := startFileNativeTraceCapture(t, ctx)
				defer finishCapture()
			}
			err = batcher.WithCatalogMutationBatch(context.WithValue(ctx, updateKey{}, true), ns.Name, func(batchCtx context.Context) error {
				var writes sync.WaitGroup
				writes.Go(func() { results[0] = store.UpdateFile(batchCtx, desired[0], "1") })
				select {
				case <-started:
				case <-ctx.Done():
					resume()
					writes.Wait()
					return ctx.Err()
				}
				for i := 1; i < count; i++ {
					writes.Go(func() { results[i] = store.UpdateFile(batchCtx, desired[i], "1") })
				}
				ready := assert.Eventually(t, func() bool { return reads.Load() >= count }, 5*time.Second, time.Millisecond)
				if ready {
					// Hold only this test's first CAS response until peer writes queue.
					time.Sleep(50 * time.Millisecond)
					if conflict {
						file := desired[count-1]
						raw := newRawSession(t)
						uid, err := gocql.ParseUUID(file.UID)
						assert.NoError(t, err)
						applied, err := execCapacityCAS(raw.Query("UPDATE files_by_namespace SET resource_version=?,body=? WHERE namespace=? AND creation_timestamp=? AND uid=? IF resource_version=?",
							"2", "peer-body", ns.Name, created, uid, "1").WithContext(ctx))
						assert.NoError(t, err)
						assert.True(t, applied, "the racing row must actually invalidate its queued condition")
					}
				}
				resume()
				writes.Wait()
				return errors.Join(results[:]...)
			})
			if conflict {
				require.ErrorIs(t, err, datastore.ErrConflict)
			} else {
				require.NoError(t, err)
			}
			for i, file := range desired {
				current, err := peer.GetFile(ctx, file.UID)
				require.NoError(t, err)
				if conflict && i == count-1 {
					require.ErrorIs(t, results[i], datastore.ErrConflict)
					require.Equal(t, "peer-body", current.Body)
				} else {
					require.NoError(t, results[i], "an unrelated row must not inherit another row's definite CAS conflict")
					file.CreationTimestamp = created
					require.Equal(t, file, current, "physical batches retain independent actors, clears, UNSET fields and immutable keys")
				}
			}
			families, err := registry.Gather()
			require.NoError(t, err)
			batches := uint64(0)
			rows := uint64(0)
			queued, singles, physicalRows := uint64(0), uint64(0), uint64(0)
			maximumBatch := 0
			for _, family := range families {
				if family.GetName() != "gitstore_datastore_operation_duration_seconds" {
					continue
				}
				for _, metric := range family.GetMetric() {
					for _, label := range metric.GetLabel() {
						if label.GetName() != "operation" {
							continue
						}
						switch label.GetValue() {
						case "FileAuthoritativeCAS":
							rows = metric.GetHistogram().GetSampleCount()
						case "FileConditionalQueue":
							queued = metric.GetHistogram().GetSampleCount()
						case "FileConditionalSingle":
							singles = metric.GetHistogram().GetSampleCount()
							physicalRows += singles
						default:
							if strings.HasPrefix(label.GetValue(), "FileConditionalBatch") {
								batches += metric.GetHistogram().GetSampleCount()
								size, err := strconv.Atoi(strings.TrimPrefix(label.GetValue(), "FileConditionalBatch"))
								require.NoError(t, err)
								physicalRows += uint64(size) * metric.GetHistogram().GetSampleCount()
								maximumBatch = max(maximumBatch, size)
							}
						}
					}
				}
			}
			require.Positive(t, batches, "the proof must execute actual full-binding conditional batches")
			queriesTraced, batchesTraced := 0, 0
			for _, span := range recorder.Ended() {
				if span.Name() != "scylla.query" && span.Name() != "scylla.batch" {
					continue
				}
				require.Equal(t, parent.SpanContext().TraceID(), span.SpanContext().TraceID())
				coordinator, elapsed := false, false
				for _, attr := range span.Attributes() {
					switch attr.Key {
					case "db.cassandra.coordinator.id":
						require.NotEmpty(t, attr.Value.AsString())
						coordinator = true
					case "gitstore.db.attempt.duration_ns":
						require.GreaterOrEqual(t, attr.Value.AsInt64(), int64(0))
						elapsed = true
					case "db.operation.batch.size":
						require.Positive(t, attr.Value.AsInt64())
						require.LessOrEqual(t, attr.Value.AsInt64(), int64(count))
					}
				}
				require.True(t, coordinator, "native attempts identify their driver-selected host")
				require.True(t, elapsed, "native attempts retain measured duration separately from span timestamps")
				if span.Name() == "scylla.batch" {
					batchesTraced++
				} else {
					queriesTraced++
				}
			}
			require.Positive(t, queriesTraced)
			require.Positive(t, batchesTraced, "the real ExecuteBatch path must reach the batch observer")
			requireFileNativeTraceSessions(t, recorder.Ended(), raw, nativeTrace)
			require.Greater(t, maximumBatch, 4, "the proof must exercise larger same-partition physical batches")
			require.LessOrEqual(t, maximumBatch, count)
			require.EqualValues(t, count, rows, "row attempts remain individually measurable")
			require.EqualValues(t, count, queued, "every accepted row reports its own queue boundary")
			require.Positive(t, singles, "the first physical one-row query remains independently measurable")
			if conflict {
				require.Greater(t, singles, uint64(1), "a definite batch miss reports the actual independent one-row fallback")
				require.Greater(t, physicalRows, uint64(count), "fallback repeats only physical work, not queue submission")
			} else {
				require.EqualValues(t, count, physicalRows, "successful single and batch queries cover each queued row exactly once")
			}
			blocked, err := peer.HasCatalogResources(ctx, repo.UID)
			require.NoError(t, err)
			require.True(t, blocked)
		})
	}
}

func startFileNativeTraceCapture(t *testing.T, ctx context.Context) func() {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	shutdown, err := observability.StartCapacityProfiler(observability.ProfilingConfig{URI: "http://" + address}, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, shutdown(stop))
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/debug/pprof/trace?seconds=5&sample_limit=64", nil)
	require.NoError(t, err)
	result := make(chan error, 1)
	go func() {
		response, err := (&http.Client{Timeout: 8 * time.Second}).Do(request)
		if err != nil {
			result <- err
			return
		}
		count, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, (32<<20)+1))
		err = errors.Join(readErr, response.Body.Close())
		if response.StatusCode != http.StatusOK || count == 0 || count > 32<<20 {
			err = errors.Join(err, fmt.Errorf("runtime capture status=%d bytes=%d", response.StatusCode, count))
		}
		result <- err
	}()
	// Claim one test-only slot to acknowledge budget activation, not merely trace startup.
	require.Eventually(t, func() bool { return observability.TakeRuntimeTraceSample(ctx) }, time.Second, time.Millisecond)
	var once sync.Once
	finish := func() {
		once.Do(func() { require.NoError(t, <-result) })
	}
	t.Cleanup(finish)
	return finish
}

func requireFileNativeTraceSessions(t *testing.T, spans []sdktrace.ReadOnlySpan, session *gocql.Session, enabled bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	requested, singles, batches, preparations := 0, 0, 0, 0
	seen := make(map[gocql.UUID]bool)
	for _, span := range spans {
		sampled := false
		for _, attr := range span.Attributes() {
			switch attr.Key {
			case "gitstore.db.server_trace.requested":
				sampled = attr.Value.AsBool()
			case "gitstore.db.server_trace.invalid", "gitstore.db.server_trace.overflow":
				require.False(t, attr.Value.AsBool())
			}
		}
		ids, executionKind := 0, ""
		for _, event := range span.Events() {
			if event.Name != "scylla.server_trace" {
				continue
			}
			require.True(t, enabled)
			require.True(t, sampled)
			require.Equal(t, "scylla.exchange", span.Name())
			var id gocql.UUID
			for _, attr := range event.Attributes {
				if attr.Key == "db.cassandra.trace_id" {
					var err error
					id, err = gocql.ParseUUID(attr.Value.AsString())
					require.NoError(t, err)
				}
			}
			require.NotEqual(t, gocql.UUID{}, id)
			require.False(t, seen[id], "native sessions must join exactly one physical exchange")
			seen[id] = true
			ids++
			duration, coordinator, request := requireCapacityServerTraceSession(t, ctx, session, id)
			t.Logf("Native File session: exchange=%s id=%s coordinator=%s duration_us=%d request=%q",
				span.SpanContext().SpanID(), id, coordinator, duration, request)
			switch {
			case request == "Preparing CQL3 query":
				preparations++
			case strings.HasPrefix(request, "Execute CQL3 prepared query"):
				require.Empty(t, executionKind, "this fixture performs one native execution per exchange")
				executionKind = "scylla.query"
			case request == "Execute batch of CQL3 queries":
				require.Empty(t, executionKind, "this fixture performs one native execution per exchange")
				executionKind = "scylla.batch"
			default:
				t.Fatalf("unrecognized native session request %q", request)
			}
		}
		if !sampled {
			continue
		}
		requested++
		require.Positive(t, ids, "every sampled physical exchange must return its native session")
		require.LessOrEqual(t, ids, 4)
		require.NotEmpty(t, executionKind, "preparation alone cannot establish the mutation's server timing")
		attempts := 0
		for _, child := range spans {
			if child.Parent().SpanID() == span.SpanContext().SpanID() {
				switch child.Name() {
				case "scylla.query":
					singles++
					attempts++
					require.Equal(t, executionKind, child.Name())
				case "scylla.batch":
					batches++
					attempts++
					require.Equal(t, executionKind, child.Name())
				}
			}
		}
		require.Equal(t, 1, attempts)
	}
	if enabled {
		require.Positive(t, singles, "production single-write hook must return completed server sessions")
		require.Positive(t, batches, "production batch hook must return completed server sessions")
		require.LessOrEqual(t, requested, 63, "one of 64 budget slots belongs to the activation probe")
		t.Logf("Native File trace round-trip: %d exchanges, %d sessions, %d single attempts, %d batch attempts, %d preparations",
			requested, len(seen), singles, batches, preparations)
	} else {
		require.Zero(t, requested, "ordinary integration writes must leave native tracing disabled")
	}
}

func TestScylla_FileCohortIncludesArrivalsDuringReservation(t *testing.T) {
	type reservationKey struct{}
	reserved, resume, peerLookup := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once, resumed, peerObserved sync.Once
	cfg := testScyllaConfig(t)
	cfg.QueryObserver = namespaceReservationObserver(func(ctx context.Context, query gocql.ObservedQuery) {
		if ctx.Value(reservationKey{}) == "peer" && query.Err == nil &&
			strings.Contains(query.Statement, " FROM namespaces_by_name WHERE ") {
			peerObserved.Do(func() { close(peerLookup) })
		}
		if ctx.Value(reservationKey{}) == true && query.Err == nil &&
			strings.HasPrefix(query.Statement, "UPDATE namespaces_by_uid SET repository_creation_epoch=") {
			once.Do(func() {
				close(reserved)
				<-resume
			})
		}
	})
	store, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ns := &datastore.Namespace{UID: newID(), Name: "late-cohort-" + newID()[:8]}
	require.NoError(t, store.CreateNamespace(ctx, ns))
	repo := &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: "catalog"}
	require.NoError(t, store.CreateRepositoryInActiveNamespace(ctx, repo))
	raw := newRawSession(t)
	uid, err := gocql.ParseUUID(ns.UID)
	require.NoError(t, err)
	readEpoch := func() int64 {
		t.Helper()
		var epoch int64
		require.NoError(t, raw.Query("SELECT repository_creation_epoch FROM namespaces_by_uid WHERE uid=?", uid).Scan(&epoch))
		return epoch
	}
	before := readEpoch()
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	release := func() { resumed.Do(func() { close(resume) }) }
	t.Cleanup(release)
	batcher := store.(datastore.FileMutationBatcher)
	create := func(name string) func(context.Context) error {
		return func(ctx context.Context) error {
			return store.CreateFile(ctx, &datastore.File{UID: newID(), Namespace: ns.Name, Name: name,
				RepositoryID: repo.UID, ResourceVersion: "1", CreationTimestamp: time.Now().UTC()})
		}
	}
	go func() {
		firstDone <- batcher.WithFileMutationBatch(context.WithValue(ctx, reservationKey{}, true), ns.Name, repo.UID, []string{"first"}, create("first"))
	}()
	select {
	case <-reserved:
	case <-ctx.Done():
		t.Fatal("first cohort did not acquire its reservation")
	}
	go func() {
		secondDone <- batcher.WithFileMutationBatch(context.WithValue(ctx, reservationKey{}, "peer"), ns.Name, repo.UID, []string{"second"}, create("second"))
	}()
	select {
	case <-peerLookup:
	case <-ctx.Done():
		t.Fatal("peer did not complete its Namespace lookup while the reservation was held")
	}
	require.Eventually(t, func() bool {
		return scylla.QueuedNamespaceCatalogWritesForTest(store, ns.UID) == 2
	}, time.Second, time.Millisecond)
	release()
	require.NoError(t, <-firstDone)
	require.NoError(t, <-secondDone)
	require.Equal(t, before+1, readEpoch(), "consecutive callbacks queued during the reservation RPC must share that confirmed fence")
	for _, name := range []string{"first", "second"} {
		file, err := store.GetFileByName(ctx, ns.Name, name)
		require.NoError(t, err)
		require.Equal(t, "1", file.ResourceVersion)
		require.Equal(t, repo.UID, file.RepositoryID)
	}
}

func TestScylla_CatalogReleasePreservesPeerRepositoryReservation(t *testing.T) {
	cfg := testScyllaConfig(t)
	cfg.QueryObserver = capacityQueryObserver{}
	first, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, first.Close()) })
	ctx := t.Context()
	ns := &datastore.Namespace{UID: newID(), Name: "release-peer-" + newID()[:8]}
	require.NoError(t, first.CreateNamespace(ctx, ns))
	raw := newRawSession(t)
	uid, err := gocql.ParseUUID(ns.UID)
	require.NoError(t, err)
	readPending := func() int64 {
		t.Helper()
		var pending int64
		require.NoError(t, raw.Query("SELECT pending_repository_creations FROM namespaces_by_uid WHERE uid=?", uid).Scan(&pending))
		return pending
	}
	reserved, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	cfg.QueryObserver = namespaceReservationObserver(func(_ context.Context, query gocql.ObservedQuery) {
		if query.Err == nil && strings.HasPrefix(query.Statement, "UPDATE namespaces_by_uid SET repository_creation_epoch=") {
			once.Do(func() {
				close(reserved)
				<-resume
			})
		}
	})
	second, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, second.Close()) })
	peerCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	peerDone := make(chan error, 1)
	peerStarted := false
	defer func() {
		assert.NoError(t, raw.Query("UPDATE namespaces_by_uid SET watch_committed=true WHERE uid=?", uid).Exec())
		close(resume)
		if peerStarted {
			require.NoError(t, <-peerDone)
			require.Zero(t, readPending(), "the peer must release only its remaining reservation")
		}
	}()
	evidence := &capacityQueryEvidence{tables: map[string]int{}}
	batchCtx := context.WithValue(ctx, capacityQueryKey{}, evidence)
	repo := &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: "peer"}
	require.NoError(t, first.(datastore.CatalogMutationBatcher).WithCatalogMutationBatch(batchCtx, ns.Name, func(context.Context) error {
		peerStarted = true
		go func() { peerDone <- second.CreateRepositoryInActiveNamespace(peerCtx, repo) }()
		select {
		case <-reserved:
		case err := <-peerDone:
			peerDone <- err
			return errors.Join(errors.New("peer did not retain a reservation"), err)
		case <-peerCtx.Done():
			return peerCtx.Err()
		}
		require.EqualValues(t, 2, readPending())
		require.NoError(t, raw.Query("UPDATE namespaces_by_uid SET watch_committed=false WHERE uid=?", uid).Exec())
		return nil
	}))
	require.EqualValues(t, 1, readPending(), "catalog release must preserve the still-active peer reservation even after unpublication")
	evidence.mu.Lock()
	require.Equal(t, 2, evidence.namespaceFenceReads, "a changed epoch requires the existing fresh-read release")
	require.Equal(t, 2, evidence.namespaceReleaseCASAttempts, "one definite miss may fall back to the existing conditional decrement")
	require.Zero(t, evidence.errors)
	evidence.mu.Unlock()
}

func TestScylla_CatalogReleaseFailureRetainsReservation(t *testing.T) {
	cfg := testScyllaConfig(t)
	cfg.QueryObserver = capacityQueryObserver{}
	store, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	ns := &datastore.Namespace{UID: newID(), Name: "release-failure-" + newID()[:8]}
	require.NoError(t, store.CreateNamespace(t.Context(), ns))
	evidence := &capacityQueryEvidence{tables: map[string]int{}}
	ctx := context.WithValue(t.Context(), capacityQueryKey{}, evidence)
	err = store.(datastore.CatalogMutationBatcher).WithCatalogMutationBatch(ctx, ns.Name, func(context.Context) error { return store.Close() })
	require.ErrorIs(t, err, datastore.ErrRepairRequired)
	require.ErrorContains(t, err, "release namespace catalog reservation", "release errors must return without a fresh-read decrement")
	evidence.mu.Lock()
	require.Equal(t, 1, evidence.namespaceFenceReads)
	evidence.mu.Unlock()
	raw := newRawSession(t)
	uid, err := gocql.ParseUUID(ns.UID)
	require.NoError(t, err)
	var pending int64
	require.NoError(t, raw.Query("SELECT pending_repository_creations FROM namespaces_by_uid WHERE uid=?", uid).Scan(&pending))
	require.EqualValues(t, 1, pending, "unconfirmed releases remain blocked for explicit repair")
}

func TestScylla_CheckedCatalogParentCannotMoveUntilReservationEnds(t *testing.T) {
	first, second := newTestStores(t)
	ctx := t.Context()
	original := &datastore.Namespace{UID: newID(), Name: "checked-parent-" + newID()[:8]}
	target := &datastore.Namespace{UID: newID(), Name: "checked-target-" + newID()[:8]}
	require.NoError(t, first.CreateNamespace(ctx, original))
	require.NoError(t, first.CreateNamespace(ctx, target))
	repo := &datastore.Repository{UID: newID(), Namespace: original.Name, Name: "catalog"}
	require.NoError(t, first.CreateRepositoryInActiveNamespace(ctx, repo))
	moved := *repo
	moved.Namespace = target.Name
	datastore.AdvanceRepositorySystemVersion(&moved)
	var retained context.Context
	require.NoError(t, first.(datastore.CatalogMutationBatcher).WithCatalogMutationBatch(ctx, original.Name, func(batchCtx context.Context) error {
		retained = batchCtx
		require.NoError(t, scylla.CatalogParentsActiveForTest(first, batchCtx, original.Name, repo.UID))
		peerCtx, cancel := context.WithTimeout(batchCtx, 60*time.Millisecond)
		defer cancel()
		require.ErrorIs(t, second.UpdateRepository(peerCtx, &moved, repo.ResourceVersion), context.DeadlineExceeded,
			"a checked parent's namespace cannot change while the reservation is held")
		require.NoError(t, scylla.CatalogParentsActiveForTest(first, batchCtx, original.Name, repo.UID))
		terminating := *repo
		terminating.Status = json.RawMessage(`{"deletionIntent":{"uid":"intent"},"conditions":[{"type":"DeletionPending","status":"True"}]}`)
		datastore.AdvanceRepositorySystemVersion(&terminating)
		lifecycleCtx, stop := context.WithTimeout(batchCtx, 60*time.Millisecond)
		defer stop()
		require.ErrorIs(t, second.UpdateRepository(lifecycleCtx, &terminating, repo.ResourceVersion), context.DeadlineExceeded,
			"deletion intent cannot invalidate a checked parent while the reservation is held")
		return nil
	}))
	require.NoError(t, second.UpdateRepository(ctx, &moved, repo.ResourceVersion))
	require.ErrorIs(t, scylla.CatalogParentsActiveForTest(first, retained, original.Name, repo.UID), datastore.ErrConflict,
		"a retained context must perform a fresh parent read after its reservation ends")
	require.NoError(t, first.(datastore.CatalogMutationBatcher).WithCatalogMutationBatch(ctx, target.Name, func(batchCtx context.Context) error {
		require.NoError(t, scylla.CatalogParentsActiveForTest(first, batchCtx, target.Name, repo.UID),
			"the new reservation checks the parent's current namespace independently")
		require.ErrorIs(t, scylla.CatalogParentsActiveForTest(first, batchCtx, original.Name, repo.UID), datastore.ErrConflict,
			"a checked Repository cannot be reused for a different Namespace")
		return nil
	}))
}

func TestScylla_ActiveRepositoryUpdateUsesOneFreshSnapshot(t *testing.T) {
	for _, transition := range []string{"none", "stale", "status", "deletion"} {
		t.Run(transition, func(t *testing.T) {
			type updateKey struct{}
			started, release := make(chan struct{}), make(chan struct{})
			var reads, writes atomic.Int64
			var resumed sync.Once
			resume := func() { resumed.Do(func() { close(release) }) }
			cfg := testScyllaConfig(t)
			cfg.QueryObserver = namespaceReservationObserver(func(ctx context.Context, query gocql.ObservedQuery) {
				if ctx.Value(updateKey{}) != true || query.Err != nil {
					return
				}
				if strings.HasPrefix(query.Statement, "UPDATE repositories_by_uid SET ") {
					writes.Add(1)
				}
				if strings.HasPrefix(query.Statement, "SELECT ") && strings.Contains(query.Statement, " FROM repositories_by_uid WHERE ") {
					if reads.Add(1) == 1 {
						close(started)
						select {
						case <-release:
						case <-ctx.Done():
						}
					}
				}
			})
			store, err := scylla.New(cfg, zap.NewNop())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Close()) })
			peer := newTestStore(t)
			var workers sync.WaitGroup
			defer workers.Wait()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			ns := &datastore.Namespace{UID: newID(), Name: "repository-snapshot-" + newID()[:8]}
			require.NoError(t, peer.CreateNamespace(ctx, ns))
			repo := &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: "catalog"}
			require.NoError(t, peer.CreateRepositoryInActiveNamespace(ctx, repo))
			desired := *repo
			desired.Annotations = map[string]string{"writer": "snapshot"}
			datastore.AdvanceRepositorySystemVersion(&desired)
			expected := repo.ResourceVersion
			if transition == "stale" {
				expected = newID()
			}
			results := make(chan error, 1)
			workers.Go(func() {
				results <- store.UpdateRepository(context.WithValue(ctx, updateKey{}, true), &desired, expected)
			})
			defer resume()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("Repository update did not load its first fresh snapshot")
			}
			current := *repo
			if transition == "status" || transition == "deletion" {
				if transition == "status" {
					current.Status = json.RawMessage(`{"conditions":[{"type":"Ready","status":"True"}]}`)
				} else {
					now := time.Now().UTC().Truncate(time.Millisecond)
					current.DeletionTimestamp = &now
				}
				datastore.AdvanceRepositorySystemVersion(&current)
				require.NoError(t, peer.UpdateRepository(ctx, &current, repo.ResourceVersion))
			}
			resume()
			err = <-results
			if transition == "none" {
				require.NoError(t, err)
				current = desired
			} else {
				require.ErrorIs(t, err, datastore.ErrConflict, "an intervening version or stale caller must not be overwritten")
			}
			require.EqualValues(t, 1, reads.Load(), "an active same-path update needs only its own fresh initial snapshot")
			if transition == "stale" {
				require.Zero(t, writes.Load(), "a stale initial snapshot must be rejected before writing")
			} else {
				require.EqualValues(t, 1, writes.Load(), "the authoritative CAS must decide intervening-version conflicts")
			}
			actual, err := peer.GetRepository(ctx, repo.UID)
			require.NoError(t, err)
			require.Equal(t, current.ResourceVersion, actual.ResourceVersion)
			require.Equal(t, current.Generation, actual.Generation)
			require.Equal(t, current.Annotations, actual.Annotations)
			require.Equal(t, current.Status, actual.Status)
			require.Equal(t, current.DeletionTimestamp, actual.DeletionTimestamp)
		})
	}
}

func TestScylla_ActiveRepositoryUpdatesDoNotAcquireCatalogFence(t *testing.T) {
	storeA, storeB := newTestStores(t)
	ctx := t.Context()
	ns := &datastore.Namespace{UID: newID(), Name: "repository-write-" + newID()[:8]}
	require.NoError(t, storeA.CreateNamespace(ctx, ns))
	repo := &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: "catalog"}
	require.NoError(t, storeA.CreateRepositoryInActiveNamespace(ctx, repo))
	session, err := openRootSession(scyllaAddr)
	require.NoError(t, err)
	t.Cleanup(session.Close)
	nsUID, err := gocql.ParseUUID(ns.UID)
	require.NoError(t, err)
	require.NoError(t, session.Query("UPDATE "+scyllaKeyspace+".namespaces_by_uid SET pending_repository_creations=? WHERE uid=?", int64(1), nsUID).Exec())
	t.Cleanup(func() {
		require.NoError(t, session.Query("UPDATE "+scyllaKeyspace+".namespaces_by_uid SET pending_repository_creations=? WHERE uid=?", int64(0), nsUID).Exec())
	})
	readFence := func() (int64, int64) {
		t.Helper()
		var epoch, pending int64
		require.NoError(t, session.Query("SELECT repository_creation_epoch,pending_repository_creations FROM "+scyllaKeyspace+".namespaces_by_uid WHERE uid=?", nsUID).Scan(&epoch, &pending))
		return epoch, pending
	}
	epoch, pending := readFence()
	version := repo.ResourceVersion
	repo.Annotations = map[string]string{"capacity.gitstore.dev/sequence": "metadata-1"}
	datastore.AdvanceRepositorySystemVersion(repo)
	writeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	err = storeB.UpdateRepository(writeCtx, repo, version)
	cancel()
	require.NoError(t, err, "an active same-path CAS update must not wait for unrelated catalog work")
	afterEpoch, afterPending := readFence()
	require.Equal(t, epoch, afterEpoch)
	require.Equal(t, pending, afterPending)
	current, err := storeA.GetRepository(ctx, repo.UID)
	require.NoError(t, err)
	require.Equal(t, repo.Annotations, current.Annotations)
	require.Equal(t, repo.ResourceVersion, current.ResourceVersion)
	require.Equal(t, repo.Generation, current.Generation)
	for _, intent := range []bool{false, true} {
		t.Run(fmt.Sprintf("deletion-intent-%t", intent), func(t *testing.T) {
			desired := *current
			if intent {
				desired.Status = json.RawMessage(`{"deletionIntent":{"uid":"intent"},"conditions":[{"type":"DeletionPending","status":"True"}]}`)
			} else {
				now := time.Now().UTC()
				desired.DeletionTimestamp = &now
			}
			datastore.AdvanceRepositorySystemVersion(&desired)
			writeCtx, cancel := context.WithTimeout(ctx, 60*time.Millisecond)
			defer cancel()
			require.ErrorIs(t, storeB.UpdateRepository(writeCtx, &desired, current.ResourceVersion), context.DeadlineExceeded,
				"deletion transitions must still exclude child admission")
			unchanged, err := storeA.GetRepository(ctx, repo.UID)
			require.NoError(t, err)
			require.Equal(t, current.ResourceVersion, unchanged.ResourceVersion)
			require.Nil(t, unchanged.DeletionTimestamp)
			require.False(t, datastore.HasDeletionIntent(unchanged.Status))
		})
	}
	results := make(chan error, 2)
	for i, writer := range []datastore.Datastore{storeA, storeB} {
		desired := *current
		desired.Annotations = map[string]string{"writer": strconv.Itoa(i)}
		datastore.AdvanceRepositorySystemVersion(&desired)
		go func() {
			writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			results <- writer.UpdateRepository(writeCtx, &desired, current.ResourceVersion)
		}()
	}
	var successes, conflicts int
	for range 2 {
		err := <-results
		if err == nil {
			successes++
		} else {
			require.ErrorIs(t, err, datastore.ErrConflict)
			conflicts++
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)
	current, err = storeA.GetRepository(ctx, repo.UID)
	require.NoError(t, err)
	require.Equal(t, repo.Generation, current.Generation)
	afterEpoch, afterPending = readFence()
	require.Equal(t, epoch, afterEpoch)
	require.Equal(t, pending, afterPending)

	require.NoError(t, session.Query("UPDATE "+scyllaKeyspace+".namespaces_by_uid SET pending_repository_creations=? WHERE uid=?", int64(0), nsUID).Exec())
	previous := current.ResourceVersion
	now := time.Now().UTC()
	current.DeletionTimestamp = &now
	datastore.AdvanceRepositorySystemVersion(current)
	require.NoError(t, storeA.UpdateRepository(ctx, current, previous))
	require.NoError(t, session.Query("UPDATE "+scyllaKeyspace+".namespaces_by_uid SET pending_repository_creations=? WHERE uid=?", int64(1), nsUID).Exec())
	desired := *current
	desired.Status = json.RawMessage(`{"resolved":{"progress":"pending"}}`)
	datastore.AdvanceRepositorySystemVersion(&desired)
	writeCtx, cancel = context.WithTimeout(ctx, 60*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, storeB.UpdateRepository(writeCtx, &desired, current.ResourceVersion), context.DeadlineExceeded,
		"terminating status writes must not race completion cleanup")
}

func TestScylla_CategoryDeletionMarkFencesSameNameReplacement(t *testing.T) {
	first, peer := newTestStore(t), newTestStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Millisecond)
	ns := &datastore.Namespace{UID: newID(), Name: "category-mark-" + newID()[:8], CreationTimestamp: now}
	require.NoError(t, first.CreateNamespace(ctx, ns))
	old := &datastore.CategoryTaxonomy{
		UID: newID(), Namespace: ns.Name, Name: "fixture", CreationTimestamp: now,
		ResourceVersion: "1", Generation: 1,
	}
	require.NoError(t, first.CreateCategoryTaxonomy(ctx, old))
	observed, err := first.GetCategoryTaxonomy(ctx, old.UID)
	require.NoError(t, err)
	require.NoError(t, peer.DeleteCategoryTaxonomy(ctx, old.UID))
	replacement := *old
	replacement.UID = newID()
	replacement.CreationTimestamp = now.Add(time.Millisecond)
	require.NoError(t, peer.CreateCategoryTaxonomy(ctx, &replacement))
	before, err := peer.GetCategoryTaxonomy(ctx, replacement.UID)
	require.NoError(t, err)
	for _, store := range []datastore.Datastore{first, peer} {
		for _, uid := range []string{observed.UID, ""} {
			_, err := store.(datastore.CategoryTaxonomyDeletionStore).MarkCategoryTaxonomyDeletion(
				ctx, ns.Name, old.Name, observed.ResourceVersion, uid, now)
			require.ErrorIs(t, err, datastore.ErrConflict)
		}
	}
	current, err := first.GetCategoryTaxonomy(ctx, replacement.UID)
	require.NoError(t, err)
	require.Equal(t, before, current, "stale and empty identities cannot mark or version the replacement")
	marked, err := peer.(datastore.CategoryTaxonomyDeletionStore).MarkCategoryTaxonomyDeletion(
		ctx, ns.Name, replacement.Name, current.ResourceVersion, current.UID, now)
	require.NoError(t, err)
	require.NotNil(t, marked.DeletionTimestamp)
	for _, uid := range []string{observed.UID, ""} {
		_, err := first.(datastore.CategoryTaxonomyDeletionStore).MarkCategoryTaxonomyDeletion(
			ctx, ns.Name, replacement.Name, marked.ResourceVersion, uid, now)
		require.ErrorIs(t, err, datastore.ErrConflict, "an already terminating replacement is not idempotent success for another UID")
	}
}

func TestScylla_NamespacePeerCompletionDoesNotRestoreDeletedListing(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(fmt.Sprintf("replacement-%t", replace), func(t *testing.T) {
			type completionKey struct{}
			started, release := make(chan struct{}), make(chan struct{})
			var removed, held atomic.Bool
			cfg := testScyllaConfig(t)
			cfg.QueryObserver = namespaceReservationObserver(func(ctx context.Context, query gocql.ObservedQuery) {
				if ctx.Value(completionKey{}) != true || query.Err != nil {
					return
				}
				if strings.HasPrefix(query.Statement, "DELETE FROM namespaces_by_bucket ") {
					removed.Store(true)
				}
				if removed.Load() && strings.HasPrefix(query.Statement, "SELECT name,uid FROM namespaces_by_name ") && held.CompareAndSwap(false, true) {
					close(started)
					select {
					case <-release:
					case <-ctx.Done():
					}
				}
			})
			stale, err := scylla.New(cfg, zap.NewNop())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, stale.Close()) })
			peer := newTestStore(t)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			now := time.Now().UTC().Truncate(time.Millisecond)
			ns := &datastore.Namespace{UID: newID(), Name: "namespace-peer-" + newID()[:8], CreationTimestamp: now}
			require.NoError(t, stale.CreateNamespace(ctx, ns))
			previous := ns.ResourceVersion
			ns.DeletionTimestamp = &now
			ns.Finalizers = []string{datastore.NamespaceForegroundDeletionFinalizer}
			datastore.AdvanceNamespaceSystemVersion(ns)
			require.NoError(t, stale.UpdateNamespace(ctx, ns, previous))
			done := make(chan error, 1)
			go func() {
				done <- stale.DeleteNamespaceWithResourceVersion(context.WithValue(ctx, completionKey{}, true), ns.UID, ns.ResourceVersion)
			}()
			var released sync.Once
			resume := func() { released.Do(func() { close(release) }) }
			t.Cleanup(resume)
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("completion did not reach confirmed projection cleanup")
			}
			require.NoError(t, peer.DeleteNamespaceWithResourceVersion(ctx, ns.UID, ns.ResourceVersion))
			var replacement *datastore.Namespace
			if replace {
				replacement = &datastore.Namespace{UID: newID(), Name: ns.Name, CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond)}
				require.NoError(t, peer.CreateNamespace(ctx, replacement))
			}
			resume()
			completionErr := <-done
			raw := newRawSession(t)
			uid, err := gocql.ParseUUID(ns.UID)
			require.NoError(t, err)
			var oldUID gocql.UUID
			err = raw.Query("SELECT uid FROM namespaces_by_bucket WHERE bucket=? AND creation_timestamp=? AND uid=?",
				ns.CreationTimestamp.UTC().Format("2006-01"), ns.CreationTimestamp, uid).WithContext(ctx).Scan(&oldUID)
			require.ErrorIs(t, err, gocql.ErrNotFound, "a losing peer must not resurrect the deleted Namespace listing")
			require.NoError(t, completionErr, "a peer's confirmed authoritative deletion is idempotent success")
			_, err = peer.GetNamespace(ctx, ns.UID)
			require.ErrorIs(t, err, datastore.ErrNotFound)
			current, err := peer.GetNamespaceByName(ctx, ns.Name)
			if replacement != nil {
				require.NoError(t, err)
				require.Equal(t, replacement.UID, current.UID)
				require.Equal(t, replacement.ResourceVersion, current.ResourceVersion)
			} else {
				require.ErrorIs(t, err, datastore.ErrNotFound)
			}
		})
	}
}

func TestScylla_NamespaceRestoreFencesConcurrentCompletion(t *testing.T) {
	for _, restoreBeforeCommit := range []bool{false, true} {
		t.Run(fmt.Sprintf("restore-before-commit-%t", restoreBeforeCommit), func(t *testing.T) {
			type completionKey struct{}
			started, release := make(chan struct{}), make(chan struct{})
			peerStarted, peerRelease := make(chan struct{}), make(chan struct{})
			restoreStarted, restoreRelease := make(chan struct{}), make(chan struct{})
			var staleRemoved, peerRemoved, staleHeld, peerHeld, restoreHeld atomic.Bool
			cfg := testScyllaConfig(t)
			cfg.QueryObserver = namespaceReservationObserver(func(ctx context.Context, query gocql.ObservedQuery) {
				role, _ := ctx.Value(completionKey{}).(string)
				if role == "" || query.Err != nil {
					return
				}
				pause := func(started, release chan struct{}) {
					close(started)
					select {
					case <-release:
					case <-ctx.Done():
					}
				}
				if strings.HasPrefix(query.Statement, "DELETE FROM namespaces_by_bucket ") {
					if role == "stale" {
						staleRemoved.Store(true)
					} else {
						peerRemoved.Store(true)
					}
				}
				if strings.HasPrefix(query.Statement, "SELECT name,uid FROM namespaces_by_name ") {
					if role == "stale" && staleRemoved.Load() && staleHeld.CompareAndSwap(false, true) {
						pause(started, release)
					}
					if role == "winner" && peerRemoved.Load() && peerHeld.CompareAndSwap(false, true) {
						pause(peerStarted, peerRelease)
					}
				}
				if !restoreBeforeCommit && role == "stale" &&
					strings.HasPrefix(query.Statement, "INSERT INTO namespaces_by_bucket ") && restoreHeld.CompareAndSwap(false, true) {
					pause(restoreStarted, restoreRelease)
				}
			})
			stale, err := scylla.New(cfg, zap.NewNop())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, stale.Close()) })
			peer, err := scylla.New(cfg, zap.NewNop())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, peer.Close()) })
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			now := time.Now().UTC().Truncate(time.Millisecond)
			ns := &datastore.Namespace{UID: newID(), Name: "namespace-restore-" + newID()[:8], CreationTimestamp: now}
			require.NoError(t, stale.CreateNamespace(ctx, ns))
			previous := ns.ResourceVersion
			ns.DeletionTimestamp = &now
			ns.Finalizers = []string{datastore.NamespaceForegroundDeletionFinalizer}
			datastore.AdvanceNamespaceSystemVersion(ns)
			require.NoError(t, stale.UpdateNamespace(ctx, ns, previous))
			staleVersion := ns.ResourceVersion
			staleDone, peerDone := make(chan error, 1), make(chan error, 1)
			var released, peerReleased, restoreReleased sync.Once
			resume := func() { released.Do(func() { close(release) }) }
			resumePeer := func() { peerReleased.Do(func() { close(peerRelease) }) }
			resumeRestore := func() { restoreReleased.Do(func() { close(restoreRelease) }) }
			t.Cleanup(func() { resume(); resumePeer(); resumeRestore() })
			wait := func(started chan struct{}) {
				t.Helper()
				select {
				case <-started:
				case <-ctx.Done():
					t.Fatal("completion did not reach the coordinated race boundary")
				}
			}
			go func() {
				staleDone <- stale.DeleteNamespaceWithResourceVersion(context.WithValue(ctx, completionKey{}, "stale"), ns.UID, staleVersion)
			}()
			wait(started)
			datastore.AdvanceNamespaceSystemVersion(ns)
			require.NoError(t, peer.UpdateNamespace(ctx, ns, staleVersion))
			if restoreBeforeCommit {
				go func() {
					peerDone <- peer.DeleteNamespaceWithResourceVersion(context.WithValue(ctx, completionKey{}, "winner"), ns.UID, ns.ResourceVersion)
				}()
				wait(peerStarted)
				resume()
				require.ErrorIs(t, <-staleDone, datastore.ErrConflict, "a live authoritative version conflict must remain a conflict")
				resumePeer()
				require.NoError(t, <-peerDone)
			} else {
				resume()
				wait(restoreStarted)
				require.NoError(t, peer.DeleteNamespaceWithResourceVersion(ctx, ns.UID, ns.ResourceVersion))
				resumeRestore()
				require.NoError(t, <-staleDone, "post-restoration confirmation must resolve the peer's committed delete")
			}
			raw := newRawSession(t)
			uid, err := gocql.ParseUUID(ns.UID)
			require.NoError(t, err)
			var found gocql.UUID
			err = raw.Query("SELECT uid FROM namespaces_by_bucket WHERE bucket=? AND creation_timestamp=? AND uid=?",
				ns.CreationTimestamp.UTC().Format("2006-01"), ns.CreationTimestamp, uid).WithContext(ctx).Scan(&found)
			require.ErrorIs(t, err, gocql.ErrNotFound)
			_, err = peer.GetNamespace(ctx, ns.UID)
			require.ErrorIs(t, err, datastore.ErrNotFound)
			_, err = peer.GetNamespaceByName(ctx, ns.Name)
			require.ErrorIs(t, err, datastore.ErrNotFound)
		})
	}
}

func TestScylla_NamespaceCDCRepairsDeletedProjectionOnly(t *testing.T) {
	for _, state := range []string{"live", "deleted", "replaced"} {
		t.Run(state, func(t *testing.T) {
			store := newTestStore(t)
			ctx := t.Context()
			ns := &datastore.Namespace{
				UID: newID(), Name: "namespace-cdc-repair-" + newID()[:8],
				CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
			}
			require.NoError(t, store.CreateNamespace(ctx, ns))
			raw := newRawSession(t)
			uid, err := gocql.ParseUUID(ns.UID)
			require.NoError(t, err)
			var replacement *datastore.Namespace
			if state != "live" {
				require.NoError(t, store.DeleteNamespace(ctx, ns.UID))
				if state == "replaced" {
					replacement = &datastore.Namespace{UID: newID(), Name: ns.Name}
					require.NoError(t, store.CreateNamespace(ctx, replacement))
				}
				require.NoError(t, raw.Query("INSERT INTO namespaces_by_bucket (bucket,creation_timestamp,uid) VALUES (?,?,?)",
					ns.CreationTimestamp.UTC().Format("2006-01"), ns.CreationTimestamp, uid).WithContext(ctx).Exec())
				_, err = raw.Query("INSERT INTO namespaces_by_name (name,uid) VALUES (?,?) IF NOT EXISTS", ns.Name, uid).
					WithContext(ctx).MapScanCAS(make(map[string]interface{}))
				require.NoError(t, err)
			}
			ready, err := scylla.NamespaceCDCDeletionReadyForTest(ctx, store, ns)
			var found gocql.UUID
			listErr := raw.Query("SELECT uid FROM namespaces_by_bucket WHERE bucket=? AND creation_timestamp=? AND uid=?",
				ns.CreationTimestamp.UTC().Format("2006-01"), ns.CreationTimestamp, uid).WithContext(ctx).Scan(&found)
			if state == "live" {
				require.ErrorContains(t, err, "namespace CDC listing projection deletion is not committed")
				require.False(t, ready)
				require.NoError(t, listErr)
			} else {
				require.NoError(t, err)
				require.True(t, ready)
				require.ErrorIs(t, listErr, gocql.ErrNotFound)
			}
			current, lookupErr := store.GetNamespaceByName(ctx, ns.Name)
			switch state {
			case "live":
				require.NoError(t, lookupErr)
				require.Equal(t, ns.UID, current.UID)
			case "replaced":
				require.NoError(t, lookupErr)
				require.Equal(t, replacement.UID, current.UID)
			case "deleted":
				require.ErrorIs(t, lookupErr, datastore.ErrNotFound)
			}
		})
	}
}

func TestScylla_RepositoryLookupOverlapsIndependentValidationReads(t *testing.T) {
	writer := newTestStore(t)
	ns := &datastore.Namespace{UID: newID(), Name: "lookup-" + newID()[:8]}
	require.NoError(t, writer.CreateNamespace(t.Context(), ns))
	repo := &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: "catalog"}
	require.NoError(t, writer.CreateRepositoryInActiveNamespace(t.Context(), repo))
	require.NoError(t, writer.CreateNamespaceMapping(t.Context(), &datastore.NamespaceMapping{
		Namespace: ns.Name, Name: repo.Name, RepositoryID: repo.UID,
	}))

	cfg := testScyllaConfig(t)
	var armed atomic.Bool
	var countsMu sync.Mutex
	counts := make(map[string]int)
	authoritativeStarted, reverseStarted, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var authoritativeOnce, reverseOnce, releaseOnce sync.Once
	cfg.QueryObserver = namespaceReservationObserver(func(ctx context.Context, query gocql.ObservedQuery) {
		if !armed.Load() || !strings.HasPrefix(query.Statement, "SELECT ") {
			return
		}
		for _, tableName := range []string{"namespace_mappings", "repositories_by_uid", "namespace_mappings_by_repository"} {
			if !strings.Contains(query.Statement, " FROM "+tableName+" WHERE ") {
				continue
			}
			countsMu.Lock()
			counts[tableName]++
			countsMu.Unlock()
			switch tableName {
			case "repositories_by_uid":
				authoritativeOnce.Do(func() { close(authoritativeStarted) })
				select {
				case <-release:
				case <-ctx.Done():
				}
			case "namespace_mappings_by_repository":
				reverseOnce.Do(func() { close(reverseStarted) })
			}
		}
	})
	reader, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reader.Close()) })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	defer releaseOnce.Do(func() { close(release) })
	armed.Store(true)
	type result struct {
		mapping *datastore.NamespaceMapping
		err     error
	}
	done := make(chan result, 1)
	go func() {
		mapping, lookupErr := reader.LookupRepository(ctx, ns.Name, repo.Name)
		done <- result{mapping: mapping, err: lookupErr}
	}()
	select {
	case <-authoritativeStarted:
	case <-ctx.Done():
		t.Fatal("authoritative repository validation did not start")
	}
	select {
	case <-reverseStarted:
	case <-time.After(time.Second):
		t.Error("reverse validation must not wait for the independent authoritative read")
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case outcome := <-done:
		require.NoError(t, outcome.err)
		require.Equal(t, repo.UID, outcome.mapping.RepositoryID)
	case <-ctx.Done():
		t.Fatal("repository lookup did not join its validation reads")
	}
	countsMu.Lock()
	require.Equal(t, map[string]int{
		"namespace_mappings": 1, "repositories_by_uid": 1, "namespace_mappings_by_repository": 1,
	}, counts, "overlap must retain all three fresh validation reads without amplification")
	countsMu.Unlock()
	armed.Store(false)

	raw := newRawSession(t)
	uid, err := gocql.ParseUUID(repo.UID)
	require.NoError(t, err)
	for _, state := range []string{"stale-reverse", "missing-reverse", "stale-authoritative"} {
		t.Run(state, func(t *testing.T) {
			require.NoError(t, raw.Query("UPDATE namespace_mappings_by_repository SET namespace=?,name=? WHERE repository_id=?", ns.Name, repo.Name, uid).Exec())
			switch state {
			case "stale-reverse":
				require.NoError(t, raw.Query("UPDATE namespace_mappings_by_repository SET namespace=? WHERE repository_id=?", ns.Name+"-moved", uid).Exec())
			case "missing-reverse":
				require.NoError(t, raw.Query("DELETE FROM namespace_mappings_by_repository WHERE repository_id=?", uid).Exec())
			case "stale-authoritative":
				require.NoError(t, raw.Query("UPDATE repositories_by_uid SET namespace=? WHERE uid=?", ns.Name+"-moved", uid).Exec())
			}
			mapping, lookupErr := reader.LookupRepository(t.Context(), ns.Name, repo.Name)
			require.Nil(t, mapping)
			require.ErrorIs(t, lookupErr, datastore.ErrRepairRequired)
		})
	}
	require.NoError(t, raw.Query("UPDATE namespace_mappings_by_repository SET namespace=?,name=? WHERE repository_id=?", ns.Name, repo.Name, uid).Exec())
	require.NoError(t, raw.Query("DELETE FROM repositories_by_uid WHERE uid=?", uid).Exec())
	mapping, err := reader.LookupRepository(t.Context(), ns.Name, repo.Name)
	require.NoError(t, err, "consistent reserved mappings remain discoverable before repository provisioning")
	require.Equal(t, repo.UID, mapping.RepositoryID)
	canceled, stop := context.WithCancel(t.Context())
	stop()
	mapping, err = reader.LookupRepository(canceled, ns.Name, repo.Name)
	require.Nil(t, mapping)
	require.ErrorIs(t, err, context.Canceled)
	t.Logf("isolated Scylla keyspace=%s", scyllaKeyspace)
}

func TestScylla_RepositoryCompletionRestoresAllLookups(t *testing.T) {
	storeA, storeB := newTestStores(t)
	ctx := context.Background()
	for _, stage := range []struct {
		name  string
		step  string
		after bool
	}{
		{"partial-cleanup", "complete-repository-projections", true},
		{"before-authoritative-delete", "complete-repository-authoritative", false},
		{"lost-authoritative-response", "complete-repository-authoritative", true},
	} {
		t.Run(stage.name, func(t *testing.T) {
			ns := &datastore.Namespace{UID: newID(), Name: "completion-" + newID()[:8], CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond)}
			require.NoError(t, storeA.CreateNamespace(ctx, ns))
			repo := &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: "catalog", CreationTimestamp: ns.CreationTimestamp}
			require.NoError(t, storeA.CreateRepositoryInActiveNamespace(ctx, repo))
			require.NoError(t, storeA.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{Namespace: ns.Name, Name: repo.Name, RepositoryID: repo.UID}))
			previous := repo.ResourceVersion
			now := time.Now().UTC().Truncate(time.Millisecond)
			repo.DeletionTimestamp = &now
			repo.Finalizers = []string{datastore.RepositoryForegroundDeletionFinalizer}
			datastore.AdvanceRepositorySystemVersion(repo)
			require.NoError(t, storeA.UpdateRepository(ctx, repo, previous))
			restoreInjector, injected := scylla.InjectRepositoryCompletionFailureForTest(storeA, stage.step, stage.after)
			t.Cleanup(restoreInjector)
			err := storeA.(datastore.RepositoryDeletionStore).CompleteRepositoryDeletion(ctx, repo.UID, repo.ResourceVersion)
			require.True(t, injected(), "the selected completion failure must actually fire")
			if stage.name != "lost-authoritative-response" {
				require.Error(t, err)
				current, err := storeB.GetRepository(ctx, repo.UID)
				require.NoError(t, err)
				require.Equal(t, repo.ResourceVersion, current.ResourceVersion)
				mapping, err := storeB.LookupRepository(ctx, ns.Name, repo.Name)
				require.NoError(t, err)
				require.Equal(t, repo.UID, mapping.RepositoryID)
				reverse, err := storeB.LookupNamespaceByRepoID(ctx, repo.UID)
				require.NoError(t, err)
				require.Equal(t, repo.Name, reverse.Name)
				list, err := storeB.ListRepositoriesByNamespace(ctx, ns.Name, datastore.PageParams{First: 2})
				require.NoError(t, err)
				require.Len(t, list.Items, 1)
				require.Equal(t, repo.UID, list.Items[0].UID)
				require.NoError(t, storeB.(datastore.RepositoryDeletionStore).CompleteRepositoryDeletion(ctx, repo.UID, repo.ResourceVersion))
			} else {
				require.NoError(t, err)
			}
			_, err = storeB.GetRepository(ctx, repo.UID)
			require.ErrorIs(t, err, datastore.ErrNotFound)
			_, err = storeB.LookupRepository(ctx, ns.Name, repo.Name)
			require.ErrorIs(t, err, datastore.ErrNotFound)
			_, err = storeB.LookupNamespaceByRepoID(ctx, repo.UID)
			require.ErrorIs(t, err, datastore.ErrNotFound)
		})
	}
}

func TestScylla_GitDeletionSagaRecoveryNative(t *testing.T) {
	uri, secret := testutil.NativeGitTestConfiguration(t)
	client := func(t *testing.T, options ...grpc.DialOption) *gitclient.Client {
		t.Helper()
		c, err := gitclient.NewClientWithAddr(uri, secret, options...)
		require.NoError(t, err)
		c.Registry = auth.NewProviderRegistry(nil, testutil.NewAllowAllAuthZ(), nil)
		t.Cleanup(func() { require.NoError(t, c.Close()) })
		return c
	}
	cleaner := client(t)
	storeA, storeB := newTestStores(t)
	for _, kind := range []string{"Repository", "Namespace"} {
		for _, stage := range []string{"lost-git-response", "partial-cleanup", "before-authoritative-delete", "lost-authoritative-response"} {
			t.Run(kind+"/"+stage, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				provision := func(uid string) {
					t.Helper()
					t.Cleanup(func() {
						cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
						defer stop()
						require.NoError(t, cleaner.DeleteRepository(cleanup, uid))
					})
					_, err := cleaner.CreateRepository(ctx, uid, "")
					require.NoError(t, err)
				}
				write := func(uid, path string) string {
					t.Helper()
					sha, err := cleaner.CommitFileForRepo(ctx, uid, gitclient.CommitFileParams{
						Path: path, Content: []byte("owned native saga fixture\n"), CommitMessage: "native saga fixture",
						AuthorName: "GitStore test", AuthorEmail: "native-test@gitstore.invalid",
					})
					require.NoError(t, err)
					require.Len(t, sha, 40)
					return sha
				}
				ns, repo := createNamespaceCompletionResources(t, storeA)
				if kind == "Repository" {
					repo = &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: "catalog", CreationTimestamp: ns.CreationTimestamp}
					require.NoError(t, storeA.CreateRepositoryInActiveNamespace(ctx, repo))
					require.NoError(t, storeA.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{
						Namespace: ns.Name, Name: repo.Name, RepositoryID: repo.UID,
					}))
				}
				provision(repo.UID)
				write(repo.UID, "retained.md")
				authorID := newID()
				provision(authorID)
				path := "repositories/" + repo.Name + ".md"
				resourceUID := repo.UID
				if kind == "Namespace" {
					path, resourceUID = "namespaces/"+ns.Name+".md", ns.UID
				}
				initial := write(authorID, path)
				removed, err := cleaner.DeleteFileForRepo(ctx, authorID, gitclient.DeleteFileParams{
					Path: path, RefName: "refs/heads/main", ExpectedCommitSHA: initial,
					CommitMessage: "remove native saga manifest", AuthorName: "GitStore test", AuthorEmail: "native-test@gitstore.invalid",
				})
				require.NoError(t, err)
				require.Len(t, removed, 40)
				head := write(authorID, "unrelated.md")
				require.NotEqual(t, removed, head)
				intent := &admission.InfrastructureDeletionIntent{
					UID: resourceUID, RepositoryID: authorID, Path: path, Ref: "refs/heads/main",
					Actor: "native-saga-test", RemovalCommit: removed,
				}
				if kind == "Namespace" {
					previous := ns.ResourceVersion
					now := time.Now().UTC()
					ns.DeletionTimestamp = &now
					ns.Finalizers = []string{datastore.NamespaceForegroundDeletionFinalizer}
					ns.Status, err = admission.WithDeletionIntent(ns.Status, intent, ns.Generation, time.Now().UTC())
					require.NoError(t, err)
					datastore.AdvanceNamespaceSystemVersion(ns)
					require.NoError(t, storeA.UpdateNamespace(ctx, ns, previous))
				} else {
					previous := repo.ResourceVersion
					now := time.Now().UTC()
					repo.DeletionTimestamp = &now
					repo.Finalizers = []string{datastore.RepositoryForegroundDeletionFinalizer}
					repo.Status, err = admission.WithDeletionIntent(repo.Status, intent, repo.Generation, now)
					require.NoError(t, err)
					datastore.AdvanceRepositorySystemVersion(repo)
					require.NoError(t, storeA.UpdateRepository(ctx, repo, previous))
				}
				var options []grpc.DialOption
				var receipts <-chan string
				var injected func() bool
				if stage == "lost-git-response" {
					intercept, observed := testutil.LoseFirstSuccessfulGitReply(gitv1.GitService_DeleteRepository_FullMethodName)
					receipts = observed
					options = append(options, grpc.WithChainUnaryInterceptor(intercept))
				} else {
					step := "complete-repository-authoritative"
					if stage == "partial-cleanup" {
						step = "complete-repository-projections"
					}
					var restore func()
					restore, injected = scylla.InjectRepositoryCompletionFailureForTest(storeA, step, stage != "before-authoritative-delete")
					t.Cleanup(restore)
					require.False(t, injected())
				}
				writer := client(t, options...)
				first, err := resolver.NewService(resolver.ServiceDeps{Store: storeA, GitWriter: writer, Logger: zap.NewNop()})
				require.NoError(t, err)
				peerClient := client(t)
				peer, err := resolver.NewService(resolver.ServiceDeps{Store: storeB, GitWriter: peerClient, Logger: zap.NewNop()})
				require.NoError(t, err)
				complete := func(service *resolver.Service) error {
					if kind == "Namespace" {
						_, err := service.CompleteNamespaceDeletion(ctx, ns.Name, ns.ResourceVersion, ns.UID)
						return err
					}
					_, err := service.CompleteRepositoryDeletion(ctx, repo.Namespace, repo.Name, repo.ResourceVersion, repo.UID)
					return err
				}
				err = complete(first)
				if injected != nil {
					require.True(t, injected(), "a successful no-fault completion is not lost-response evidence")
				}
				if stage == "lost-authoritative-response" {
					require.NoError(t, err, "a confirmed committed delete resolves its lost response")
				} else {
					require.Error(t, err)
					current, err := storeB.GetRepository(ctx, repo.UID)
					require.NoError(t, err)
					require.NotNil(t, current.DeletionTimestamp)
					require.Equal(t, []string{datastore.RepositoryForegroundDeletionFinalizer}, current.Finalizers)
					mapping, err := storeB.LookupRepository(ctx, ns.Name, repo.Name)
					require.NoError(t, err)
					require.Equal(t, repo.UID, mapping.RepositoryID)
					reverse, err := storeB.LookupNamespaceByRepoID(ctx, repo.UID)
					require.NoError(t, err)
					require.Equal(t, repo.Name, reverse.Name)
					page, err := storeB.ListRepositoriesByNamespace(ctx, ns.Name, datastore.PageParams{First: 10})
					require.NoError(t, err)
					listed := false
					for _, row := range page.Items {
						listed = listed || row.UID == repo.UID
					}
					require.True(t, listed, "failed completion must restore the listing projection")
					if kind == "Namespace" {
						parent, err := storeB.GetNamespaceByName(ctx, ns.Name)
						require.NoError(t, err)
						require.Equal(t, ns.UID, parent.UID)
						require.Equal(t, ns.ResourceVersion, parent.ResourceVersion)
						require.NotNil(t, parent.DeletionTimestamp)
						require.Equal(t, []string{datastore.NamespaceForegroundDeletionFinalizer}, parent.Finalizers)
					}
					_, err = peer.CompleteRepositoryDeletion(ctx, repo.Namespace, repo.Name, "stale-version", repo.UID)
					require.ErrorIs(t, err, datastore.ErrConflict)
					if receipts != nil {
						require.Len(t, receipts, 1)
						require.Equal(t, repo.UID, <-receipts)
					}
					require.NoError(t, complete(peer))
				}
				require.NoError(t, complete(peer), "peer completion remains idempotent")
				for _, store := range []datastore.Datastore{storeA, storeB} {
					_, err := store.GetRepository(ctx, repo.UID)
					require.ErrorIs(t, err, datastore.ErrNotFound)
					_, err = store.LookupRepository(ctx, ns.Name, repo.Name)
					require.ErrorIs(t, err, datastore.ErrNotFound)
					_, err = store.LookupNamespaceByRepoID(ctx, repo.UID)
					require.ErrorIs(t, err, datastore.ErrNotFound)
				}
				_, err = cleaner.CreateRepository(ctx, repo.UID, "")
				require.Equal(t, codes.FailedPrecondition, status.Code(err), "old native UID remains tombstoned")
				if kind == "Namespace" {
					_, err := storeB.GetNamespaceByName(ctx, ns.Name)
					require.ErrorIs(t, err, datastore.ErrNotFound)
					replacement := &datastore.Namespace{UID: newID(), Name: ns.Name, CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond)}
					require.NoError(t, storeB.CreateNamespace(ctx, replacement))
				}
				replacement := &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: repo.Name, CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond)}
				require.NoError(t, storeB.CreateRepositoryInActiveNamespace(ctx, replacement))
				require.NoError(t, storeB.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{
					Namespace: ns.Name, Name: replacement.Name, RepositoryID: replacement.UID,
				}))
				provision(replacement.UID)
				replacementHead := write(replacement.UID, "retained.md")
				require.ErrorIs(t, complete(first), datastore.ErrConflict, "stale API finalization cannot redirect to a new UID")
				require.NoError(t, cleaner.DeleteRepository(ctx, repo.UID))
				content, err := cleaner.ReadFileForRepo(ctx, replacement.UID, "retained.md", replacementHead)
				require.NoError(t, err)
				require.Equal(t, []byte("owned native saga fixture\n"), content)
				for _, store := range []datastore.Datastore{storeA, storeB} {
					current, err := store.GetRepository(ctx, replacement.UID)
					require.NoError(t, err)
					require.Nil(t, current.DeletionTimestamp)
					require.Empty(t, current.Finalizers)
					mapping, err := store.LookupRepository(ctx, ns.Name, repo.Name)
					require.NoError(t, err)
					require.Equal(t, replacement.UID, mapping.RepositoryID)
				}
			})
		}
	}
}

type namespaceCompletionGit struct {
	resolver.GitWriter
	afterRemovalRead func()
	deletedUIDs      []string
}

func (g *namespaceCompletionGit) ReadFileForRepo(context.Context, string, string, string) ([]byte, error) {
	if g.afterRemovalRead != nil {
		hook := g.afterRemovalRead
		g.afterRemovalRead = nil
		hook()
	}
	return nil, status.Error(codes.NotFound, "recorded manifest removal")
}

func (g *namespaceCompletionGit) DeleteRepository(_ context.Context, uid string) error {
	g.deletedUIDs = append(g.deletedUIDs, uid)
	return nil
}

type namespaceCompletionStore struct {
	datastore.Datastore
	parentReads     int
	afterParentRead func()
}

func (s *namespaceCompletionStore) GetNamespaceByName(ctx context.Context, name string) (*datastore.Namespace, error) {
	current, err := s.Datastore.GetNamespaceByName(ctx, name)
	if err == nil && s.afterParentRead != nil {
		s.parentReads++
		if s.parentReads == 2 {
			hook := s.afterParentRead
			s.afterParentRead = nil
			hook()
		}
	}
	return current, err
}

func createNamespaceCompletionResources(t *testing.T, store datastore.Datastore) (*datastore.Namespace, *datastore.Repository) {
	t.Helper()
	ctx := t.Context()
	name := "completion-peer-" + newID()[:8]
	now := time.Now().UTC().Truncate(time.Millisecond)
	original := &datastore.Namespace{UID: newID(), Name: name, CreationTimestamp: now}
	require.NoError(t, store.CreateNamespace(ctx, original))
	system := &datastore.Repository{
		UID: newID(), Namespace: name, Name: "gitstore-system", CreationTimestamp: now,
	}
	system.RepositoryID = system.UID
	require.NoError(t, store.CreateRepositoryInActiveNamespace(ctx, system))
	require.NoError(t, store.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{
		Namespace: name, Name: system.Name, RepositoryID: system.UID,
	}))
	return original, system
}

func createNamespaceCompletionFixture(t *testing.T, store datastore.Datastore) (*datastore.Namespace, *datastore.Repository) {
	t.Helper()
	ctx := t.Context()
	original, system := createNamespaceCompletionResources(t, store)
	now := original.CreationTimestamp
	previous := original.ResourceVersion
	original.DeletionTimestamp = &now
	original.Finalizers = []string{datastore.NamespaceForegroundDeletionFinalizer}
	var err error
	original.Status, err = admission.WithDeletionIntent(original.Status, &admission.InfrastructureDeletionIntent{
		UID: original.UID, RepositoryID: newID(), Path: "namespaces/" + original.Name + ".md",
		Ref: "refs/heads/main", Actor: "alice", RemovalCommit: strings.Repeat("a", 40),
	}, original.Generation, now)
	require.NoError(t, err)
	datastore.AdvanceNamespaceSystemVersion(original)
	require.NoError(t, store.UpdateNamespace(ctx, original, previous))
	return original, system
}

func TestScylla_NamespaceCompletionPreservesReplacementAcrossPeers(t *testing.T) {
	storeA, storeB := newTestStores(t)
	for _, stage := range []string{"after_manifest_read", "after_parent_read"} {
		t.Run(stage, func(t *testing.T) {
			ctx := t.Context()
			original, system := createNamespaceCompletionFixture(t, storeA)
			name := original.Name
			git := &namespaceCompletionGit{}
			var staleStore datastore.Datastore = storeA
			parentRead := &namespaceCompletionStore{Datastore: storeA}
			if stage == "after_parent_read" {
				staleStore = parentRead
			}
			stale, err := resolver.NewService(resolver.ServiceDeps{Store: staleStore, GitWriter: git, Logger: zap.NewNop()})
			require.NoError(t, err)
			peer, err := resolver.NewService(resolver.ServiceDeps{Store: storeB, GitWriter: git, Logger: zap.NewNop()})
			require.NoError(t, err)
			var replacement *datastore.Namespace
			var replacementSystem *datastore.Repository
			replace := func() {
				_, err := peer.CompleteNamespaceDeletion(ctx, name, original.ResourceVersion, original.UID)
				require.NoError(t, err)
				replacement = &datastore.Namespace{UID: newID(), Name: name, CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond)}
				require.NoError(t, storeB.CreateNamespace(ctx, replacement))
				replacementSystem = &datastore.Repository{
					UID: newID(), Namespace: name, Name: system.Name, CreationTimestamp: replacement.CreationTimestamp,
				}
				replacementSystem.RepositoryID = replacementSystem.UID
				require.NoError(t, storeB.CreateRepositoryInActiveNamespace(ctx, replacementSystem))
				require.NoError(t, storeB.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{
					Namespace: name, Name: system.Name, RepositoryID: replacementSystem.UID,
				}))
			}
			if stage == "after_manifest_read" {
				git.afterRemovalRead = replace
			} else {
				parentRead.afterParentRead = replace
			}
			observed, err := stale.CompleteNamespaceDeletion(ctx, name, original.ResourceVersion, original.UID)
			require.NotNil(t, replacement)
			if stage == "after_manifest_read" {
				require.ErrorIs(t, err, datastore.ErrConflict)
				require.NotNil(t, observed)
				require.Equal(t, replacement.UID, observed.UID)
				require.Equal(t, replacement.ResourceVersion, observed.ResourceVersion)
			} else {
				require.ErrorIs(t, err, datastore.ErrNotFound)
			}
			for _, store := range []datastore.Datastore{storeA, storeB} {
				current, err := store.GetRepository(ctx, replacementSystem.UID)
				require.NoError(t, err)
				require.Equal(t, replacementSystem.ResourceVersion, current.ResourceVersion)
				require.Empty(t, current.Finalizers)
				require.Nil(t, current.DeletionTimestamp)
				namespace, err := store.GetNamespaceByName(ctx, name)
				require.NoError(t, err)
				require.Equal(t, replacement.UID, namespace.UID)
				require.Equal(t, replacement.ResourceVersion, namespace.ResourceVersion)
				require.Empty(t, namespace.Finalizers)
				require.Nil(t, namespace.DeletionTimestamp)
				mapping, err := store.LookupRepository(ctx, name, system.Name)
				require.NoError(t, err)
				require.Equal(t, replacementSystem.UID, mapping.RepositoryID)
				_, err = store.GetRepository(ctx, system.UID)
				require.ErrorIs(t, err, datastore.ErrNotFound)
			}
			require.Equal(t, []string{system.UID}, git.deletedUIDs, "only the peer's old UID may reach storage deletion")
		})
	}
}

func TestScylla_NamespaceCompletionPreservesUnrelatedFinalizersAcrossPeers(t *testing.T) {
	storeA, storeB := newTestStores(t)
	for _, kind := range []string{"Namespace", "Repository"} {
		t.Run(kind, func(t *testing.T) {
			ctx := t.Context()
			ns, system := createNamespaceCompletionFixture(t, storeA)
			if kind == "Namespace" {
				expected := ns.ResourceVersion
				ns.Finalizers = append(ns.Finalizers, "example.test/retain")
				datastore.AdvanceNamespaceSystemVersion(ns)
				require.NoError(t, storeB.UpdateNamespace(ctx, ns, expected))
			} else {
				expected := system.ResourceVersion
				now := time.Now().UTC().Truncate(time.Millisecond)
				system.DeletionTimestamp = &now
				system.Finalizers = []string{datastore.RepositoryForegroundDeletionFinalizer, "example.test/retain"}
				datastore.AdvanceRepositorySystemVersion(system)
				require.NoError(t, storeB.UpdateRepository(ctx, system, expected))
			}
			git := &namespaceCompletionGit{}
			for _, store := range []datastore.Datastore{storeA, storeB} {
				service, err := resolver.NewService(resolver.ServiceDeps{Store: store, GitWriter: git, Logger: zap.NewNop()})
				require.NoError(t, err)
				_, err = service.CompleteNamespaceDeletion(ctx, ns.Name, ns.ResourceVersion, ns.UID)
				require.ErrorContains(t, err, "other finalizers")
				_, err = service.CompleteRepositoryDeletion(ctx, system.Namespace, system.Name, system.ResourceVersion, system.UID)
				require.ErrorContains(t, err, "other finalizers")
				current, err := store.GetNamespaceByName(ctx, ns.Name)
				require.NoError(t, err)
				require.Equal(t, ns.UID, current.UID)
				require.Equal(t, ns.ResourceVersion, current.ResourceVersion)
				require.Equal(t, ns.Finalizers, current.Finalizers)
				require.NotNil(t, current.DeletionTimestamp)
				preserved, err := store.GetRepository(ctx, system.UID)
				require.NoError(t, err)
				require.Equal(t, system.ResourceVersion, preserved.ResourceVersion)
				require.Equal(t, system.Finalizers, preserved.Finalizers)
				require.Equal(t, system.DeletionTimestamp, preserved.DeletionTimestamp)
				mapping, err := store.LookupRepository(ctx, ns.Name, system.Name)
				require.NoError(t, err)
				require.Equal(t, system.UID, mapping.RepositoryID)
			}
			require.Empty(t, git.deletedUIDs, "neither completion entry point may delete storage while blocked")
			if kind == "Namespace" {
				expected := ns.ResourceVersion
				ns.Finalizers = []string{datastore.NamespaceForegroundDeletionFinalizer}
				datastore.AdvanceNamespaceSystemVersion(ns)
				require.NoError(t, storeA.UpdateNamespace(ctx, ns, expected))
			} else {
				expected := system.ResourceVersion
				system.Finalizers = []string{datastore.RepositoryForegroundDeletionFinalizer}
				datastore.AdvanceRepositorySystemVersion(system)
				require.NoError(t, storeA.UpdateRepository(ctx, system, expected))
			}
			peer, err := resolver.NewService(resolver.ServiceDeps{Store: storeB, GitWriter: git, Logger: zap.NewNop()})
			require.NoError(t, err)
			_, err = peer.CompleteNamespaceDeletion(ctx, ns.Name, ns.ResourceVersion, ns.UID)
			require.NoError(t, err)
			require.Equal(t, []string{system.UID}, git.deletedUIDs)
			for _, store := range []datastore.Datastore{storeA, storeB} {
				_, err := store.GetNamespaceByName(ctx, ns.Name)
				require.ErrorIs(t, err, datastore.ErrNotFound)
				_, err = store.GetRepository(ctx, system.UID)
				require.ErrorIs(t, err, datastore.ErrNotFound)
				_, err = store.LookupRepository(ctx, ns.Name, system.Name)
				require.ErrorIs(t, err, datastore.ErrNotFound)
			}
		})
	}
}

func TestScylla_CatalogAdmissionRacesDeletionIntentAcrossReplicas(t *testing.T) {
	storeA, storeB := newTestStores(t)
	ctx := context.Background()
	for _, namespaceIntent := range []bool{false, true} {
		for round := 0; round < 4; round++ {
			ns := &datastore.Namespace{UID: newID(), Name: "catalog-race-" + newID()[:8], CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond)}
			require.NoError(t, storeA.CreateNamespace(ctx, ns))
			repo := &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: "gitstore-system", CreationTimestamp: ns.CreationTimestamp}
			repo.RepositoryID = repo.UID
			require.NoError(t, storeA.CreateRepositoryInActiveNamespace(ctx, repo))
			product := &datastore.Product{UID: newID(), Namespace: ns.Name, Name: "product", RepositoryID: repo.UID, ResourceVersion: "1"}
			start := make(chan struct{})
			create := func() error { return storeB.CreateProduct(ctx, product) }
			remove := func() error { return storeB.DeleteProduct(ctx, product.UID) }
			if round%2 != 0 {
				file := &datastore.File{UID: product.UID, Namespace: ns.Name, Name: "file", RepositoryID: repo.UID, ResourceVersion: "1"}
				create = func() error { return storeB.CreateFile(ctx, file) }
				remove = func() error { return storeB.DeleteFile(ctx, file.UID) }
			}
			createResult, intentResult := make(chan error, 1), make(chan error, 1)
			go func() {
				<-start
				createResult <- create()
			}()
			go func() {
				<-start
				intent := json.RawMessage(`{"deletionIntent":{"uid":"intent"},"conditions":[{"type":"DeletionPending","status":"True"}]}`)
				if namespaceIntent {
					expected := ns.ResourceVersion
					ns.Status = intent
					datastore.AdvanceNamespaceSystemVersion(ns)
					intentResult <- storeA.UpdateNamespace(ctx, ns, expected)
				} else {
					expected := repo.ResourceVersion
					repo.Status = intent
					datastore.AdvanceRepositorySystemVersion(repo)
					intentResult <- storeA.UpdateRepository(ctx, repo, expected)
				}
			}()
			close(start)
			createErr, intentErr := <-createResult, <-intentResult
			require.True(t, createErr == nil || intentErr == nil, "both operations failed: create=%v intent=%v", createErr, intentErr)
			require.False(t, createErr == nil && intentErr == nil, "child admission and empty-to-intent transition cannot both commit")
			if createErr == nil {
				blocked, err := storeA.HasCatalogResources(ctx, repo.UID)
				require.NoError(t, err)
				require.True(t, blocked)
				require.NoError(t, remove())
				blocked, err = storeA.HasCatalogResources(ctx, repo.UID)
				require.NoError(t, err)
				require.False(t, blocked)
			}
			if intentErr == nil {
				require.Error(t, create())
			}
		}
	}
}

func TestScylla_DeletionIntentAndGuardedCompletionAcrossReplicas(t *testing.T) {
	storeA, storeB := newTestStores(t)
	ctx := context.Background()
	ns := &datastore.Namespace{UID: newID(), Name: "deletion-" + newID()[:8], CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond)}
	require.NoError(t, storeA.CreateNamespace(ctx, ns))
	repo := &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: "gitstore-system", CreationTimestamp: ns.CreationTimestamp}
	repo.RepositoryID = repo.UID
	require.NoError(t, storeB.CreateRepositoryInActiveNamespace(ctx, repo))
	require.NoError(t, storeB.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{Namespace: ns.Name, Name: repo.Name, RepositoryID: repo.UID}))
	blocked, err := datastore.NamespaceDeletionBlocked(ctx, storeA, ns)
	require.NoError(t, err)
	require.False(t, blocked)
	ns.Status = json.RawMessage(`{"deletionIntent":{"uid":"intent"},"conditions":[{"type":"DeletionPending","status":"True"}]}`)
	previous := ns.ResourceVersion
	datastore.AdvanceNamespaceSystemVersion(ns)
	require.NoError(t, storeA.UpdateNamespace(ctx, ns, previous))
	late := &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: "late"}
	require.ErrorIs(t, storeB.CreateRepositoryInActiveNamespace(ctx, late), datastore.ErrNamespaceNotActive)
	require.ErrorIs(t, storeB.CreateProduct(ctx, &datastore.Product{UID: newID(), Namespace: ns.Name, Name: "late", RepositoryID: repo.UID}), datastore.ErrNamespaceNotActive)
	now := time.Now().UTC().Truncate(time.Millisecond)
	previous = ns.ResourceVersion
	ns.DeletionTimestamp = &now
	datastore.AdvanceNamespaceSystemVersion(ns)
	require.NoError(t, storeA.MarkNamespaceDeletion(ctx, ns, previous))
	require.ErrorIs(t, storeB.DeleteNamespaceWithResourceVersion(ctx, ns.UID, ns.ResourceVersion), datastore.ErrNamespaceNotEmpty)
	repo.DeletionTimestamp = &now
	repo.Finalizers = []string{datastore.RepositoryForegroundDeletionFinalizer, "other/finalizer"}
	previous = repo.ResourceVersion
	datastore.AdvanceRepositorySystemVersion(repo)
	require.NoError(t, storeA.UpdateRepository(ctx, repo, previous))
	completion := storeB.(datastore.RepositoryDeletionStore)
	require.ErrorIs(t, completion.CompleteRepositoryDeletion(ctx, repo.UID, previous), datastore.ErrConflict)
	require.ErrorIs(t, completion.CompleteRepositoryDeletion(ctx, repo.UID, repo.ResourceVersion), datastore.ErrConflict)
	previous = repo.ResourceVersion
	repo.Finalizers = []string{datastore.RepositoryForegroundDeletionFinalizer}
	datastore.AdvanceRepositorySystemVersion(repo)
	require.NoError(t, storeA.UpdateRepository(ctx, repo, previous))
	require.NoError(t, completion.CompleteRepositoryDeletion(ctx, repo.UID, repo.ResourceVersion))
	_, err = storeA.LookupRepository(ctx, ns.Name, repo.Name)
	require.ErrorIs(t, err, datastore.ErrNotFound)
	require.NoError(t, storeB.DeleteNamespaceWithResourceVersion(ctx, ns.UID, ns.ResourceVersion))
}

func TestScylla_NamespaceRepositoryLifecycleCoordinationAcrossReplicas(t *testing.T) {
	storeA, storeB := newTestStores(t)
	ctx := context.Background()
	namespace := &datastore.Namespace{
		UID:               newID(),
		Name:              "repository-lifecycle-" + newID()[:8],
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
	require.NoError(t, storeA.CreateNamespace(ctx, namespace))
	repository := &datastore.Repository{
		UID:               newID(),
		Namespace:         namespace.Name,
		Name:              "catalog",
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}

	require.NoError(t, storeB.CreateRepositoryInActiveNamespace(ctx, repository))
	current, err := storeA.GetNamespace(ctx, namespace.UID)
	require.NoError(t, err)
	deletedAt := time.Now().UTC().Truncate(time.Millisecond)
	expectedResourceVersion := current.ResourceVersion
	current.DeletionTimestamp = &deletedAt
	datastore.AdvanceNamespaceSystemVersion(current)
	require.ErrorIs(t, storeA.MarkNamespaceDeletion(ctx, current, expectedResourceVersion), datastore.ErrNamespaceNotEmpty)

	require.NoError(t, storeB.DeleteRepository(ctx, repository.UID))
	current, err = storeA.GetNamespace(ctx, namespace.UID)
	require.NoError(t, err)
	expectedResourceVersion = current.ResourceVersion
	current.DeletionTimestamp = &deletedAt
	datastore.AdvanceNamespaceSystemVersion(current)
	require.NoError(t, storeA.MarkNamespaceDeletion(ctx, current, expectedResourceVersion))

	late := *repository
	late.UID = newID()
	late.Name = "late"
	require.ErrorIs(t, storeB.CreateRepositoryInActiveNamespace(ctx, &late), datastore.ErrNamespaceNotActive)
}

func TestScylla_NamespaceRepositoryLifecycleRejectsUninitializedFence(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	namespace := &datastore.Namespace{
		UID:               newID(),
		Name:              "invalid-fence-" + newID()[:8],
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
	require.NoError(t, store.CreateNamespace(ctx, namespace))
	clearNamespaceRepositoryFence(t, namespace.UID)

	repository := &datastore.Repository{
		UID:               newID(),
		Namespace:         namespace.Name,
		Name:              "catalog",
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
	require.ErrorIs(t, store.CreateRepositoryInActiveNamespace(ctx, repository), datastore.ErrRepairRequired)
	_, err := store.GetRepository(ctx, repository.UID)
	require.ErrorIs(t, err, datastore.ErrNotFound, "an invalid fence cannot admit a repository")

	current, err := store.GetNamespace(ctx, namespace.UID)
	require.NoError(t, err)
	expectedResourceVersion := current.ResourceVersion
	deletedAt := time.Now().UTC().Truncate(time.Millisecond)
	current.DeletionTimestamp = &deletedAt
	datastore.AdvanceNamespaceSystemVersion(current)
	require.ErrorIs(t, store.MarkNamespaceDeletion(ctx, current, expectedResourceVersion), datastore.ErrRepairRequired)
	current, err = store.GetNamespace(ctx, namespace.UID)
	require.NoError(t, err)
	require.Nil(t, current.DeletionTimestamp, "an invalid fence cannot start termination")
}

func clearNamespaceRepositoryFence(t *testing.T, namespaceUID string) {
	t.Helper()
	session, err := openRootSession(scyllaAddr)
	require.NoError(t, err)
	t.Cleanup(session.Close)
	uid, err := gocql.ParseUUID(namespaceUID)
	require.NoError(t, err)
	require.NoError(t, session.Query(
		fmt.Sprintf(
			"UPDATE %s.namespaces_by_uid SET repository_creation_epoch=null, pending_repository_creations=null WHERE uid=?",
			scyllaKeyspace,
		),
		uid,
	).Exec())
}

func TestScylla_RepositoryDirectUIDPathReversePath_FullEnvelopeAndBodyRoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	deletedAt := now.Add(time.Hour)
	uid := newID()
	namespace := "repository-envelope-" + newID()[:8]
	repository := &datastore.Repository{
		APIVersion:        "gitstore.dev/v1beta1",
		Kind:              "Repository",
		UID:               uid,
		ID:                uid,
		Namespace:         namespace,
		NamespaceID:       namespace,
		Name:              "canonical-" + newID()[:8],
		Generation:        9,
		ResourceVersion:   "13",
		Revision:          "main@sha1:repository",
		CreationTimestamp: now,
		CreationActor:     "alice",
		UpdateTimestamp:   now.Add(time.Minute),
		UpdateActor:       "bob",
		Labels:            map[string]string{"team": "catalog"},
		Annotations:       map[string]string{"gitstore.dev/note": "repository"},
		OwnerReferences:   json.RawMessage(`[{"apiVersion":"gitstore.dev/v1beta1","kind":"Namespace","name":"owner","uid":"00000000-0000-0000-0000-000000000001"}]`),
		Finalizers:        []string{"gitstore.dev/test"},
		DeletionTimestamp: &deletedAt,
		RepositoryID:      uid,
		SourcePath:        "repositories/canonical.md",
		GitCommitSHA:      "repository-sha",
		GitRef:            "refs/heads/main",
		Spec:              json.RawMessage(`{"defaultBranch":"main","visibility":"PRIVATE"}`),
		Body:              "# Repository\n\nRaw **Markdown** body.\n",
		Status:            json.RawMessage(`{"observedGeneration":9,"conditions":[]}`),
		DefaultBranch:     "main",
		StorageClass:      "default",
		MaxPackSizeBytes:  64 * 1024 * 1024,
		MaxFileSizeBytes:  8 * 1024 * 1024,
	}
	mapping := &datastore.NamespaceMapping{
		Namespace:    namespace,
		NamespaceID:  namespace,
		Name:         repository.Name,
		RepositoryID: uid,
		RepoID:       uid,
	}

	require.NoError(t, store.CreateRepository(ctx, repository))
	require.NoError(t, store.CreateNamespaceMapping(ctx, mapping))

	got, err := store.GetRepository(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, repository, got)
	assert.Equal(t, uid, got.UID)
	assert.Equal(t, namespace, got.Namespace)
	assert.Equal(t, uid, got.RepositoryID)
	assert.Equal(t, repository.Body, got.Body)
	assert.JSONEq(t, string(repository.OwnerReferences), string(got.OwnerReferences))

	byPath, err := store.LookupRepository(ctx, namespace, repository.Name)
	require.NoError(t, err)
	assert.Equal(t, mapping, byPath)

	reverse, err := store.LookupNamespaceByRepoID(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, mapping, reverse)
}

func TestScylla_NamespaceRepositoryQueryShapeMetadata(t *testing.T) {
	tests := []struct {
		name  string
		table interface {
			Metadata() table.Metadata
			Get(...string) (string, []string)
		}
		partKey  []string
		sortKey  []string
		whereKey string
	}{
		{"namespace UID", scylla.NamespaceByUID, []string{"uid"}, nil, "uid"},
		{"namespace name", scylla.NamespaceByName, []string{"name"}, nil, "name"},
		{"repository UID", scylla.RepositoryByUID, []string{"uid"}, nil, "uid"},
		{"repository path", scylla.NamespaceMapping, []string{"namespace"}, []string{"name"}, "namespace"},
		{"repository reverse path", scylla.NamespaceMappingByRepository, []string{"repository_id"}, nil, "repository_id"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metadata := tt.table.Metadata()
			assert.Equal(t, tt.partKey, metadata.PartKey)
			assert.Equal(t, tt.sortKey, metadata.SortKey)

			statement, _ := tt.table.Get()
			lower := strings.ToLower(statement)
			assert.Contains(t, lower, "where "+tt.whereKey+"=")
			assert.NotContains(t, lower, "allow filtering")
			assert.NotContains(t, lower, " index ")
		})
	}

	store := newTestStore(t)
	require.NotNil(t, store)
	session := newRawSession(t)
	iter := session.Query(
		`SELECT index_name FROM system_schema.indexes WHERE keyspace_name = ?`,
		scyllaKeyspace,
	).Iter()
	var indexName string
	for iter.Scan(&indexName) {
		lower := strings.ToLower(indexName)
		assert.NotContains(t, lower, "repository")
		assert.NotContains(t, lower, "namespace_mapping")
	}
	require.NoError(t, iter.Close())
}
