// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRBACNativeJournalRecovery(t *testing.T) {
	fixture := ownedRBACNativeFixture(t)
	if os.Getenv("RBAC_TEST_JOURNAL_RECOVERY_RUN") != "1" {
		t.Skip("fresh owned short-retention journal fixture not selected")
	}
	config := os.Getenv("RBAC_TEST_JOURNAL_CONFIG")
	binary := os.Getenv("RBAC_TEST_API_BINARY")
	endpoint := os.Getenv("RBAC_TEST_JOURNAL_API")
	subject := os.Getenv("RBAC_TEST_SUBJECT")
	for _, value := range []string{config, binary, subject} {
		require.NotEmpty(t, value)
	}
	require.NotContains(t, fixture.apis, endpoint)
	for _, key := range []string{"RBAC_TEST_CONTROLLER_A", "RBAC_TEST_CONTROLLER_B"} {
		response, err := (&http.Client{Timeout: time.Second}).Get(os.Getenv(key) + "/health")
		require.NoError(t, err, "both owned native controllers must be live")
		require.Equal(t, http.StatusOK, response.StatusCode)
		require.NoError(t, response.Body.Close())
	}
	var ownership struct {
		Fresh    bool   `json:"freshDatabaseNotRestoredOldFixture"`
		Keyspace string `json:"keyspace"`
	}
	data, err := os.ReadFile(os.Getenv("RBAC_TEST_JOURNAL_OWNERSHIP"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &ownership))
	require.True(t, ownership.Fresh, "never shorten retention on retained acceptance storage")
	require.True(t, strings.HasPrefix(ownership.Keyspace, "gitstore_rbac_owner_"))
	proxy, err := newRBACJournalFaultProxy(os.Getenv("RBAC_TEST_JOURNAL_SCYLLA_ADDRESS"))
	require.NoError(t, err)
	t.Cleanup(proxy.close)
	actor := fixture
	actor.token = rbacNativeToken(t, os.Getenv("RBAC_TEST_ACTOR_TOKEN_FILE"))
	list := `query($namespace:String!){products(namespace:$namespace,first:1){edges{node{id}}}}`
	variables := map[string]any{"namespace": fixture.namespace}
	query := func(api string) gqlResponse {
		return secretTestGraphQLWithTimeout(t, api, actor.token, list, variables, time.Second)
	}
	ready := func() bool {
		response, err := (&http.Client{Timeout: time.Second}).Get(endpoint + "/ready")
		if err != nil {
			return false
		}
		require.NoError(t, response.Body.Close())
		return response.StatusCode == http.StatusOK
	}
	logPath, modelsPath, stop := rbacJournalStartAPI(t, binary, config, endpoint, proxy.address())
	rbacNativeEventually(t, ready, "the additional native API must reconstruct canonical state before the fault")
	for _, api := range append(append([]string{}, fixture.apis...), endpoint) {
		rbacNativeEventually(t, func() bool {
			result := query(api)
			if graphqlErrorHasCode(result.Errors, "AUTHORIZATION_UNAVAILABLE") {
				return false
			}
			rbacNativeErrorCode(t, result, "FORBIDDEN")
			return true
		}, "the fresh fixture must not inherit an unrelated actor grant")
	}
	name := uniqueName("journal-recovery")
	binding := fixture.manifest("RoleBinding", name)
	binding["spec"] = map[string]any{
		"roleRef":  map[string]any{"kind": "ClusterRole", "name": "view"},
		"subjects": []any{map[string]any{"kind": "User", "name": subject}},
	}
	var uid string
	defer fixture.revokeBinding(t, "RoleBinding", name, binding["spec"], &uid)
	grant := fixture.create(t, "RoleBinding", binding)
	uid = grant.Metadata.UID
	rbacNativeEventually(t, func() bool {
		for _, api := range append(append([]string{}, fixture.apis...), endpoint) {
			result := query(api)
			if graphqlErrorHasCode(result.Errors, "FORBIDDEN") ||
				graphqlErrorHasCode(result.Errors, "AUTHORIZATION_UNAVAILABLE") {
				return false
			}
			require.Empty(t, result.Errors)
		}
		return true
	}, "the exact admitted User grant must be usable on every native API")

	require.Positive(t, proxy.pause(), "the short fault must close real native CQL connections")
	resumed := make(chan struct{})
	timer := time.AfterFunc(250*time.Millisecond, func() { proxy.resume(); close(resumed) })
	defer func() { timer.Stop(); proxy.resume() }()
	for _, api := range fixture.apis {
		require.Empty(t, query(api).Errors, "a private short CQL outage cannot change the healthy peers' grant")
	}
	<-resumed
	rbacNativeEventually(t, func() bool {
		result := query(endpoint)
		if graphqlErrorHasCode(result.Errors, "AUTHORIZATION_UNAVAILABLE") {
			return false
		}
		require.Empty(t, result.Errors)
		return ready()
	}, "the same native process must recover from the below-bound interruption")

	cursor := capacityBootstrapCursor(t, endpoint, fixture.token)
	faultStarted := time.Now()
	require.Positive(t, proxy.pause(), "the long fault must close real native CQL connections")
	safety := time.AfterFunc(20*time.Second, proxy.resume)
	defer func() { safety.Stop(); proxy.resume() }()
	fixture.revokeBinding(t, "RoleBinding", name, binding["spec"], &uid)
	for _, api := range fixture.apis {
		rbacNativeEventually(t, func() bool {
			result := query(api)
			if graphqlErrorHasCode(result.Errors, "FORBIDDEN") {
				return true
			}
			require.Empty(t, result.Errors)
			return false
		}, "healthy peers must enforce revocation while the isolated API cannot consume the journal")
	}
	if remaining := 5*time.Second - time.Since(faultStarted); remaining > 0 {
		time.Sleep(remaining)
	}
	require.False(t, ready(), "the isolated API must be unready after the unchanged five-second freshness bound")
	unavailable := secretTestGraphQLWithTimeout(t, endpoint, fixture.token,
		`query{clusterRoles(first:1){edges{node{id}}}}`, nil, time.Second)
	require.True(t, unavailable.Status == http.StatusServiceUnavailable ||
		graphqlErrorHasCode(unavailable.Errors, "AUTHORIZATION_UNAVAILABLE"),
		"trusted administrators cannot bypass missing journal freshness")
	require.True(t, safety.Stop(), "the bounded fault must finish before its independent safety restoration")
	restored := time.Now()
	proxy.resume()
	recoveryDeadline := restored.Add(30 * time.Second)
	var recoveredRevisions map[string]bool
	rbacNativeEventually(t, func() bool {
		require.True(t, time.Now().Before(recoveryDeadline), "native domain recovery exceeded 30 seconds")
		if !ready() {
			return false
		}
		result := query(endpoint)
		if graphqlErrorHasCode(result.Errors, "AUTHORIZATION_UNAVAILABLE") {
			return false
		}
		rbacNativeErrorCode(t, result, "FORBIDDEN")
		revisions := rbacJournalCanonicalRevisions(t, fixture)
		if !rbacRuntimePublicationMatches(t, logPath, 0, subject, revisions, "deny") {
			return false
		}
		recoveredRevisions = revisions
		return true
	}, "complete post-fault snapshots and an exact canonical-revision denial must precede recovery")
	recoveredIn := time.Since(restored)
	require.LessOrEqual(t, recoveredIn, 30*time.Second)
	for _, api := range fixture.apis {
		rbacNativeErrorCode(t, query(api), "FORBIDDEN")
	}
	conn, err := dialCapacityWatch(endpoint, fixture.token, cursor, 5*time.Second)
	require.NoError(t, err)
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, expired := readCapacityEvent(conn)
	require.NoError(t, conn.Close())
	require.ErrorContains(t, expired, "WATCH_EXPIRED")
	require.ErrorContains(t, expired, "RETENTION_EXPIRED",
		"the native journal must actually have lost the pre-fault cursor, not merely reconnected")
	stop()
	require.True(t, rbacJournalResyncedSources(t, modelsPath, restored, restored.Add(recoveredIn), recoveredRevisions, name),
		"both complete canonical source models must precede the post-fault denial")
	restartLog, _, _ := rbacJournalStartAPI(t, binary, config, endpoint, proxy.address())
	rbacNativeEventually(t, func() bool {
		if !ready() {
			return false
		}
		result := query(endpoint)
		if graphqlErrorHasCode(result.Errors, "AUTHORIZATION_UNAVAILABLE") {
			return false
		}
		rbacNativeErrorCode(t, result, "FORBIDDEN")
		return rbacRuntimePublicationMatches(t, restartLog, 0, subject, rbacJournalCanonicalRevisions(t, fixture), "deny")
	}, "physical API replacement must not restore the revoked grant")
	t.Logf("Native CQL interruption, retained-cursor expiry, full-source resync and revoked-grant restart pass; recovery=%s", recoveredIn)
}

func rbacJournalCanonicalRevisions(t *testing.T, fixture rbacNativeFixture) map[string]bool {
	t.Helper()
	git := rbacNativeGit{fixture: fixture, dir: t.TempDir()}
	revisions := make(map[string]bool, 2)
	for _, namespace := range []string{"gitstore-system", fixture.namespace} {
		output, err := git.command(t, "ls-remote", fixture.gitURL+"/"+namespace+"/gitstore-system", "refs/heads/main")
		require.NoError(t, err, "canonical source truth must remain independently readable")
		fields := strings.Fields(string(output))
		require.Len(t, fields, 2)
		revisions["main@sha1:"+fields[0]] = true
	}
	require.Len(t, revisions, 2)
	return revisions
}

func rbacJournalResyncedSources(t *testing.T, path string, after, before time.Time, revisions map[string]bool, revoked string) bool {
	t.Helper()
	file, err := os.Open(path)
	require.NoError(t, err)
	defer file.Close()
	seen := make(map[string]bool, 2)
	err = walkRBACCapacitySourceModelsWithClock(io.LimitReader(file, 64<<20),
		func(header rbacCapacitySourceHeader, resources []json.RawMessage, observed time.Time) error {
			var origin struct{ GitRevision string }
			if err := json.Unmarshal(header.Origin, &origin); err != nil {
				return err
			}
			if observed.After(after) && !observed.After(before) && revisions[origin.GitRevision] {
				for _, raw := range resources {
					var resource struct {
						Kind     string
						Metadata struct {
							Name              string
							DeletionTimestamp *string
						}
					}
					if err := json.Unmarshal(raw, &resource); err != nil {
						return err
					}
					if resource.Kind == "RoleBinding" && resource.Metadata.Name == revoked &&
						resource.Metadata.DeletionTimestamp == nil {
						return fmt.Errorf("resynchronized source retained the live revoked binding")
					}
				}
				seen[origin.GitRevision] = true
			}
			return nil
		})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		require.NoError(t, err)
	}
	return len(seen) == len(revisions)
}

func rbacJournalStartAPI(t *testing.T, binary, config, endpoint, scylla string) (string, string, func()) {
	t.Helper()
	parsed, err := url.Parse(endpoint)
	require.NoError(t, err)
	require.Equal(t, "http", parsed.Scheme)
	require.True(t, net.ParseIP(parsed.Hostname()).IsLoopback())
	require.Nil(t, parsed.User)
	require.Empty(t, parsed.Path)
	require.Empty(t, parsed.RawQuery)
	require.Empty(t, parsed.Fragment)
	existing, err := net.DialTimeout("tcp", parsed.Host, time.Second)
	if existing != nil {
		_ = existing.Close()
	}
	require.Error(t, err, "never replace an existing process at the additional API endpoint")
	directory := os.Getenv("RBAC_TEST_PROCESS_LOG_DIR")
	info, err := os.Lstat(directory)
	require.NoError(t, err)
	require.True(t, info.IsDir())
	require.Zero(t, info.Mode().Perm()&0077)
	directory, err = os.MkdirTemp(directory, "journal-recovery-")
	require.NoError(t, err)
	logPath, modelsPath := filepath.Join(directory, "api.log"), filepath.Join(directory, "models.jsonl")
	output, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	command := exec.CommandContext(ctx, binary, "-test.run", "^TestRBACNativeProcess$", "-test.timeout", "5m")
	command.Cancel = func() error { return command.Process.Signal(os.Interrupt) }
	command.WaitDelay = 15 * time.Second
	command.Env = append(os.Environ(), "GITSTORE_RBAC_NATIVE_TEST_OWNED=1",
		"GITSTORE_RBAC_NATIVE_TEST_CONFIG="+config,
		"GITSTORE_RBAC_NATIVE_TEST_MODELS_FILE="+modelsPath,
		"GITSTORE_API__DATASTORE__SCYLLA__HOSTS="+scylla,
		"GITSTORE_API__PROFILING__URI=")
	command.Stdout, command.Stderr = output, output
	if err := command.Start(); err != nil {
		cancel()
		_ = output.Close()
		require.NoError(t, err)
	}
	done := make(chan struct{})
	go func() { _ = command.Wait(); close(done) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			<-done
			require.NoError(t, output.Close())
			require.True(t, command.ProcessState.Success(), "owned native API must release its leases and stop gracefully")
		})
	}
	t.Cleanup(stop)
	return logPath, modelsPath, stop
}

func TestRBACJournalFaultProxyInterruptsExistingAndNewConnections(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = upstream.Close() })
	go func() {
		for {
			connection, err := upstream.Accept()
			if err != nil {
				return
			}
			go func() {
				defer connection.Close()
				_, _ = io.Copy(connection, connection)
			}()
		}
	}()
	proxy, err := newRBACJournalFaultProxy(upstream.Addr().String())
	require.NoError(t, err)
	t.Cleanup(proxy.close)
	dial := func() net.Conn {
		connection, err := net.DialTimeout("tcp", proxy.address(), time.Second)
		require.NoError(t, err)
		t.Cleanup(func() { _ = connection.Close() })
		require.NoError(t, connection.SetDeadline(time.Now().Add(3*time.Second)))
		return connection
	}
	echo := func(connection net.Conn, value string) {
		_, err := io.WriteString(connection, value)
		require.NoError(t, err)
		data := make([]byte, len(value))
		_, err = io.ReadFull(connection, data)
		require.NoError(t, err)
		require.Equal(t, value, string(data))
	}
	existing := dial()
	echo(existing, "before")
	proxy.pause()
	_, err = existing.Read(make([]byte, 1))
	require.Error(t, err, "an established connection must actually be interrupted")
	blocked := dial()
	_, err = blocked.Read(make([]byte, 1))
	require.Error(t, err, "reconnecting cannot bypass the fault")
	proxy.resume()
	echo(dial(), "after")
	proxy.close()
	proxy.close()
	connection, err := net.DialTimeout("tcp", proxy.address(), time.Second)
	if connection != nil {
		_ = connection.Close()
	}
	require.Error(t, err, "cleanup must close the owned listener")
}

type rbacJournalFaultProxy struct {
	listener net.Listener
	upstream string
	mu       sync.Mutex
	paused   bool
	closed   bool
	active   map[net.Conn]net.Conn
	accepted chan struct{}
	workers  sync.WaitGroup
	once     sync.Once
}

func newRBACJournalFaultProxy(upstream string) (*rbacJournalFaultProxy, error) {
	host, portText, err := net.SplitHostPort(upstream)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 || !net.ParseIP(host).IsLoopback() {
		return nil, fmt.Errorf("journal fault target must be an explicit loopback address")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	proxy := &rbacJournalFaultProxy{listener: listener, upstream: upstream,
		active: make(map[net.Conn]net.Conn), accepted: make(chan struct{})}
	go func() {
		defer close(proxy.accepted)
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			proxy.mu.Lock()
			if proxy.paused || proxy.closed || len(proxy.active) >= 128 {
				proxy.mu.Unlock()
				_ = client.Close()
				continue
			}
			proxy.active[client] = nil
			proxy.mu.Unlock()
			proxy.workers.Add(1)
			go proxy.forward(client)
		}
	}()
	return proxy, nil
}

func (p *rbacJournalFaultProxy) forward(client net.Conn) {
	defer p.workers.Done()
	defer func() {
		_ = client.Close()
		p.mu.Lock()
		delete(p.active, client)
		p.mu.Unlock()
	}()
	upstream, err := net.DialTimeout("tcp", p.upstream, time.Second)
	if err != nil {
		return
	}
	defer upstream.Close()
	p.mu.Lock()
	if p.paused || p.closed {
		p.mu.Unlock()
		return
	}
	p.active[client] = upstream
	p.mu.Unlock()
	copied := make(chan struct{})
	go func() {
		defer close(copied)
		_, _ = io.Copy(upstream, client)
		_ = upstream.Close()
		_ = client.Close()
	}()
	_, _ = io.Copy(client, upstream)
	_ = upstream.Close()
	_ = client.Close()
	<-copied
}

func (p *rbacJournalFaultProxy) address() string { return p.listener.Addr().String() }

func (p *rbacJournalFaultProxy) pause() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.paused = true
	for client, upstream := range p.active {
		_ = client.Close()
		if upstream != nil {
			_ = upstream.Close()
		}
	}
	return len(p.active)
}

func (p *rbacJournalFaultProxy) resume() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.paused = false
}

func (p *rbacJournalFaultProxy) close() {
	p.once.Do(func() {
		p.mu.Lock()
		p.closed = true
		p.mu.Unlock()
		p.pause()
		_ = p.listener.Close()
		<-p.accepted
		p.workers.Wait()
	})
}

func TestRBACJournalFaultProxyRefusesNonLoopbackTargets(t *testing.T) {
	for _, target := range []string{"example.com:9042", "192.0.2.1:9042", "127.0.0.1:0", "127.0.0.1:65536"} {
		proxy, err := newRBACJournalFaultProxy(target)
		require.Error(t, err)
		require.Nil(t, proxy)
	}
}
