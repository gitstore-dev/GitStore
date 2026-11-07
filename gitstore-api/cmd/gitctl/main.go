// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/rbaclocal"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/staticusers"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"golang.org/x/crypto/bcrypt"
)

type projectionRepairClient interface {
	Audit(context.Context) (scylla.RepairPlan, error)
	Apply(context.Context, scylla.RepairPlan) (scylla.RepairResult, error)
	Close()
}

var openProjectionRepair = func(cfg config.ScyllaConfig) (projectionRepairClient, error) {
	return scylla.OpenProjectionRepairService(cfg)
}

var migrateSchema = scylla.Migrate

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}

	switch args[0] {
	case "generate":
		return runGenerate(args[1:], stdout, stderr)
	case "hash-password":
		var password string
		if len(args) >= 2 {
			password = args[1]
		} else {
			scanner := bufio.NewScanner(stdin)
			if !scanner.Scan() {
				fmt.Fprintln(stderr, "Error reading password from stdin")
				return 1
			}
			password = strings.TrimRight(scanner.Text(), "\r\n")
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			fmt.Fprintf(stderr, "Error generating hash: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, string(hash))
		return 0

	case "users":
		return runUsers(args[1:], stdin, stdout, stderr)

	case "rbac":
		return runRBAC(args[1:], stdout, stderr)

	case "gen-jwt-secret":
		return printSecret(stdout, stderr, "GITSTORE_API__AUTH__JWT__SECRET")

	case "gen-hmac-secret":
		return printSecret(stdout, stderr, "GITSTORE_GRPC_AUTH__HMAC_SECRET")

	case "scylla-projection-audit":
		return runProjectionAudit(args[1:], stdout, stderr)

	case "migrate":
		return runMigrate(args[1:], stdout, stderr)

	case "scylla-projection-repair":
		return runProjectionRepair(args[1:], stdout, stderr)

	case "enroll-serviceaccount":
		return runEnrollServiceAccount(args[1:], stdout, stderr)

	case "generate-signing-key":
		return runGenerateServiceAccountKey(args[1:], stdout, stderr)

	case "validate-local-config":
		return runValidateLocalConfig(args[1:], stdout, stderr)

	default:
		fmt.Fprintf(stderr, "Unknown subcommand: %s\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func printUsage(output io.Writer) {
	fmt.Fprintln(output, "Usage: gitctl <subcommand> [args]")
	fmt.Fprintln(output, "Subcommands:")
	fmt.Fprintln(output, "  generate authz --root <repository-root> [--catalog <path>] [--check]")
	fmt.Fprintln(output, "  hash-password <password>")
	fmt.Fprintln(output, "  users add --file <users.yaml> --username <name> --password-stdin [--email <email>] [--display-name <name>]")
	fmt.Fprintln(output, "  rbac role add --file <policy.yaml> --name <role> [--allow <action>] [--deny <action>]")
	fmt.Fprintln(output, "  rbac binding add --file <policy.yaml> --subject <subject> --role <role>")
	fmt.Fprintln(output, "  gen-jwt-secret")
	fmt.Fprintln(output, "  gen-hmac-secret")
	fmt.Fprintln(output, "  scylla-projection-audit [Scylla flags]")
	fmt.Fprintln(output, "  migrate [Scylla flags] [--timeout 5m] (prepare an existing keyspace without starting the API)")
	fmt.Fprintln(output, "  scylla-projection-repair (--dry-run | --confirm) [Scylla flags]")
	fmt.Fprintln(output, "  enroll-serviceaccount --api-url <url> --admin-token <token> --namespace <namespace> --name <name> --key-id <id> --private-key-path <path> [--replace-existing-key]")
	fmt.Fprintln(output, "  generate-signing-key --private-key-path <path> [--record-output-path <path> --key-id <id>]")
	fmt.Fprintln(output, "  validate-local-config --config-file <config.toml> --policy-file <policy.yaml>")
}

// Catalog generation deliberately does not import generated definitions: a
// clean checkout can bootstrap or repair them from the one shared authority.
type authorizationCatalog struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Grammar       string                `json:"grammar"`
	Description   string                `json:"description"`
	Actions       []authorizationAction `json:"actions"`
}

type authorizationAction struct {
	Action   string `json:"action"`
	Kind     string `json:"kind"`
	Scope    string `json:"scope"`
	Resource string `json:"resource"`
	Verb     string `json:"verb"`
	Reserved bool   `json:"reserved,omitempty"`
}

func runGenerate(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "authz" {
		fmt.Fprintln(stderr, "generate requires target authz")
		return 2
	}
	flags := flag.NewFlagSet("generate authz", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", "..", "repository root")
	catalogPath := flags.String("catalog", "", "catalog override for validation")
	check := flags.Bool("check", false, "report drift without modifying files")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "unexpected generate arguments")
		return 2
	}
	if *catalogPath == "" {
		*catalogPath = filepath.Join(*root, "shared/schemas/authorization-actions.json")
	}
	data, err := os.ReadFile(*catalogPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	outputs, err := generateAuthorizationCatalog(data)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	paths := []string{"gitstore-api/internal/auth/actions_generated.go", "gitstore-git-service/src/auth/actions_generated.rs"}
	for i, relative := range paths {
		path := filepath.Join(*root, relative)
		if *check {
			current, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(current, outputs[i]) {
				fmt.Fprintf(stderr, "authorization catalog drift: %s (run make generate TARGET=authz)\n", relative)
				return 1
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := os.WriteFile(path, outputs[i], 0644); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	fmt.Fprintln(stdout, "authorization catalog verified")
	return 0
}

func generateAuthorizationCatalog(data []byte) ([][]byte, error) {
	var catalog authorizationCatalog
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&catalog); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("catalog must contain exactly one JSON document")
	}
	if catalog.SchemaVersion != 1 || catalog.Grammar != "<kind>[.<subresource>].<verb>" || len(catalog.Actions) == 0 {
		return nil, errors.New("invalid catalog version, grammar or empty actions")
	}
	identifier := regexp.MustCompile(`^[a-z][a-zA-Z0-9]*(?:-[a-z0-9]+)*$`)
	seen, scopes, constants := map[string]bool{}, map[string]string{}, map[string]bool{}
	for _, entry := range catalog.Actions {
		parts := strings.Split(entry.Resource, "/")
		if len(parts) > 2 || parts[0] != entry.Kind || !identifier.MatchString(entry.Kind) || !identifier.MatchString(entry.Verb) || (len(parts) == 2 && !identifier.MatchString(parts[1])) {
			return nil, fmt.Errorf("invalid resource/verb projection for %q", entry.Action)
		}
		if entry.Action != strings.ReplaceAll(entry.Resource, "/", ".")+"."+entry.Verb || seen[entry.Action] {
			return nil, fmt.Errorf("duplicate or mismatched catalog action %q", entry.Action)
		}
		if entry.Scope != "cluster" && entry.Scope != "namespace" {
			return nil, fmt.Errorf("invalid scope for %q", entry.Action)
		}
		if scope, ok := scopes[entry.Kind]; ok && scope != entry.Scope {
			return nil, fmt.Errorf("inconsistent scope for %q", entry.Kind)
		}
		name := actionConstant(entry.Action)
		if constants[name] {
			return nil, fmt.Errorf("duplicate generated constant %q", name)
		}
		seen[entry.Action], scopes[entry.Kind], constants[name] = true, entry.Scope, true
	}
	sort.Slice(catalog.Actions, func(i, j int) bool { return catalog.Actions[i].Action < catalog.Actions[j].Action })
	header := "// SPDX-License-Identifier: AGPL-3.0-or-later\n// Copyright (c) 2026 GitStore contributors\n\n// Code generated from shared/schemas/authorization-actions.json; DO NOT EDIT.\n"
	var goCode, rustCode bytes.Buffer
	goCode.WriteString(header + "package auth\n\n// ActionDefinition is a finite registered authorization capability.\ntype ActionDefinition struct { Action, Kind, Scope, Resource, Verb string; Reserved bool }\n\nconst (\n")
	for _, entry := range catalog.Actions {
		fmt.Fprintf(&goCode, "%s = %q\n", actionConstant(entry.Action), entry.Action)
	}
	goCode.WriteString(")\n\nvar authorizationActions = [...]ActionDefinition{\n")
	for _, e := range catalog.Actions {
		fmt.Fprintf(&goCode, "{%q, %q, %q, %q, %q, %t},\n", e.Action, e.Kind, e.Scope, e.Resource, e.Verb, e.Reserved)
	}
	goCode.WriteString("}\n\nvar authorizationActionIndex = func() map[string]ActionDefinition { m := make(map[string]ActionDefinition, len(authorizationActions)); for _, a := range authorizationActions { m[a.Action] = a }; return m }()\n\n// LookupAction returns an immutable copy; unknown and legacy spellings never match.\nfunc LookupAction(action string) (ActionDefinition, bool) { a, ok := authorizationActionIndex[action]; return a, ok }\n\n// AuthorizationActions returns a defensive copy in canonical action order.\nfunc AuthorizationActions() []ActionDefinition { return append([]ActionDefinition(nil), authorizationActions[:]...) }\n")
	rustCode.WriteString(header + "\n#[derive(Debug, Clone, Copy, PartialEq, Eq)]\npub struct ActionDefinition {\n    pub action: &'static str,\n    pub kind: &'static str,\n    pub scope: &'static str,\n    pub resource: &'static str,\n    pub verb: &'static str,\n    pub reserved: bool,\n}\n\n")
	for _, entry := range catalog.Actions {
		fmt.Fprintf(&rustCode, "pub const %s: &str = %q;\n", strings.ToUpper(strings.NewReplacer(".", "_", "-", "_").Replace(entry.Action)), entry.Action)
	}
	rustCode.WriteString("\n#[rustfmt::skip]\npub const AUTHORIZATION_ACTIONS: &[ActionDefinition] = &[\n")
	for _, e := range catalog.Actions {
		fmt.Fprintf(&rustCode, "    ActionDefinition { action: %q, kind: %q, scope: %q, resource: %q, verb: %q, reserved: %t },\n", e.Action, e.Kind, e.Scope, e.Resource, e.Verb, e.Reserved)
	}
	rustCode.WriteString("];\n\npub fn lookup_action(action: &str) -> Option<&'static ActionDefinition> {\n    AUTHORIZATION_ACTIONS\n        .binary_search_by_key(&action, |entry| entry.action)\n        .ok()\n        .map(|index| &AUTHORIZATION_ACTIONS[index])\n}\n")
	formatted, err := format.Source(goCode.Bytes())
	if err != nil {
		return nil, err
	}
	return [][]byte{formatted, rustCode.Bytes()}, nil
}

func actionConstant(action string) string {
	parts := strings.FieldsFunc(action, func(r rune) bool { return r == '.' || r == '-' })
	for i, part := range parts {
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return "Action" + strings.Join(parts, "")
}

func runValidateLocalConfig(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("validate-local-config", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configFile := flags.String("config-file", "", "path to config.toml")
	policyFile := flags.String("policy-file", "", "path to policy.yaml")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(*configFile) == "" || strings.TrimSpace(*policyFile) == "" {
		fmt.Fprintln(stderr, "validate-local-config requires --config-file and --policy-file")
		return 2
	}
	if _, err := config.LoadFrom(*configFile); err != nil {
		fmt.Fprintf(stderr, "Invalid configuration: %v\n", err)
		return 1
	}
	if err := rbaclocal.ValidatePolicyFile(*policyFile); err != nil {
		fmt.Fprintf(stderr, "Invalid RBAC policy: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Configuration and RBAC policy are valid")
	return 0
}

type actionList []string

func (a *actionList) String() string { return strings.Join(*a, ",") }
func (a *actionList) Set(value string) error {
	for _, action := range strings.Split(value, ",") {
		action = strings.TrimSpace(action)
		if action == "" {
			return errors.New("action must not be empty")
		}
		*a = append(*a, action)
	}
	return nil
}

func runRBAC(args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		fmt.Fprintln(stderr, "Usage: gitctl rbac (role add | binding add) [flags]")
		return 2
	}
	switch strings.Join(args[:2], " ") {
	case "role add":
		flags := flag.NewFlagSet("rbac role add", flag.ContinueOnError)
		flags.SetOutput(stderr)
		path := flags.String("file", "", "path to policy.yaml")
		name := flags.String("name", "", "new role name")
		scope := flags.String("scope", "cluster", "role scope: cluster or namespace (definitions have no namespace)")
		var allow, deny actionList
		flags.Var(&allow, "allow", "allowed action; repeat or use commas")
		flags.Var(&deny, "deny", "denied action; repeat or use commas")
		if err := flags.Parse(args[2:]); err != nil {
			return 2
		}
		denyPresent := false
		flags.Visit(func(f *flag.Flag) {
			if f.Name == "deny" {
				denyPresent = true
			}
		})
		if denyPresent {
			fmt.Fprintln(stderr, "DENY/--deny is unsupported; v1beta1 policy is allow-only")
			return 2
		}
		if flags.NArg() != 0 {
			fmt.Fprintln(stderr, "unexpected role arguments")
			return 2
		}
		if *path == "" || strings.TrimSpace(*name) == "" {
			fmt.Fprintln(stderr, "rbac role add requires --file and --name")
			return 2
		}
		if err := rbaclocal.AddRole(*path, *name, rbaclocal.RolePolicy{Allow: allow}, rbaclocal.RoleOptions{Scope: *scope}); err != nil {
			fmt.Fprintf(stderr, "Error adding role: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "Added role %q to %s\n", *name, *path)
		return 0
	case "binding add":
		flags := flag.NewFlagSet("rbac binding add", flag.ContinueOnError)
		flags.SetOutput(stderr)
		path := flags.String("file", "", "path to policy.yaml")
		subject := flags.String("subject", "", "identity subject")
		role := flags.String("role", "", "existing role name")
		scope := flags.String("scope", "cluster", "binding scope: cluster or namespace")
		namespace := flags.String("namespace", "", "explicit namespace required only for namespace bindings")
		subjectKind := flags.String("subject-kind", "User", "User, Group or ServiceAccount")
		roleKind := flags.String("role-kind", "ClusterRole", "ClusterRole or Role")
		if err := flags.Parse(args[2:]); err != nil {
			return 2
		}
		if *path == "" || strings.TrimSpace(*subject) == "" || strings.TrimSpace(*role) == "" {
			fmt.Fprintln(stderr, "rbac binding add requires --file, --subject, and --role")
			return 2
		}
		if flags.NArg() != 0 {
			fmt.Fprintln(stderr, "unexpected binding arguments")
			return 2
		}
		added, err := rbaclocal.AssignRole(*path, *subject, *role, rbaclocal.BindingOptions{Scope: *scope, Namespace: *namespace, SubjectKind: *subjectKind, RoleKind: *roleKind})
		if err != nil {
			fmt.Fprintf(stderr, "Error assigning role: %v\n", err)
			return 1
		}
		if added {
			fmt.Fprintf(stdout, "Assigned role %q to %q in %s\n", *role, *subject, *path)
		} else {
			fmt.Fprintf(stdout, "Role %q is already assigned to %q in %s\n", *role, *subject, *path)
		}
		return 0
	default:
		fmt.Fprintln(stderr, "Usage: gitctl rbac (role add | binding add) [flags]")
		return 2
	}
}

func runUsers(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "add" {
		fmt.Fprintln(stderr, "Usage: gitctl users add --file <users.yaml> --username <name> --password-stdin [--email <email>] [--display-name <name>]")
		return 2
	}
	flags := flag.NewFlagSet("users add", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("file", "", "path to users.yaml")
	username := flags.String("username", "", "unique username")
	email := flags.String("email", "", "optional email address")
	displayName := flags.String("display-name", "", "optional display name")
	passwordStdin := flags.Bool("password-stdin", false, "read the password from stdin")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if *path == "" || strings.TrimSpace(*username) == "" || !*passwordStdin {
		fmt.Fprintln(stderr, "users add requires --file, --username, and --password-stdin")
		return 2
	}
	password, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintf(stderr, "Error reading password from stdin: %v\n", err)
		return 1
	}
	plain := strings.TrimSuffix(strings.TrimSuffix(string(password), "\n"), "\r")
	if plain == "" {
		fmt.Fprintln(stderr, "Password must not be empty")
		return 2
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		fmt.Fprintf(stderr, "Error generating hash: %v\n", err)
		return 1
	}
	if err := staticusers.AddUser(*path, staticusers.UserEntry{
		Username: *username, PasswordHash: string(hash), DisplayName: *displayName, Email: *email,
	}); err != nil {
		fmt.Fprintf(stderr, "Error adding user: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Added user %q to %s\n", *username, *path)
	fmt.Fprintln(stdout, "Reminder: add a typed role binding in policy.yaml before reloading the API.")
	return 0
}

func printSecret(stdout, stderr io.Writer, key string) int {
	secret, err := randomBase64URLSecret()
	if err != nil {
		fmt.Fprintf(stderr, "Error generating secret: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s=%s\n", key, secret)
	return 0
}

func runProjectionAudit(args []string, stdout, stderr io.Writer) int {
	flags, cfg := newScyllaFlagSet("scylla-projection-audit", stderr)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	client, err := openProjectionRepair(cfg())
	if err != nil {
		fmt.Fprintf(stderr, "Error opening Scylla projection audit: %v\n", err)
		return 1
	}
	defer client.Close()

	plan, err := client.Audit(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "Error auditing Scylla projections: %v\n", err)
		return 1
	}
	if err := writeJSON(stdout, plan); err != nil {
		fmt.Fprintf(stderr, "Error writing audit result: %v\n", err)
		return 1
	}
	return 0
}

func runProjectionRepair(args []string, stdout, stderr io.Writer) int {
	flags, cfg := newScyllaFlagSet("scylla-projection-repair", stderr)
	dryRun := flags.Bool("dry-run", false, "print the repair plan without mutating Scylla")
	confirm := flags.Bool("confirm", false, "confirm conditional projection mutations")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if !*dryRun && !*confirm {
		fmt.Fprintln(stderr, "scylla-projection-repair requires --dry-run or explicit --confirm")
		return 2
	}

	client, err := openProjectionRepair(cfg())
	if err != nil {
		fmt.Fprintf(stderr, "Error opening Scylla projection repair: %v\n", err)
		return 1
	}
	defer client.Close()

	plan, err := client.Audit(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "Error planning Scylla projection repair: %v\n", err)
		return 1
	}
	if *dryRun {
		if err := writeJSON(stdout, plan); err != nil {
			fmt.Fprintf(stderr, "Error writing repair plan: %v\n", err)
			return 1
		}
		return 0
	}

	result, err := client.Apply(context.Background(), plan)
	if err != nil {
		fmt.Fprintf(stderr, "Error applying Scylla projection repair: %v\n", err)
		if writeErr := writeJSON(stdout, result); writeErr != nil {
			fmt.Fprintf(stderr, "Error writing partial repair result: %v\n", writeErr)
		}
		return 1
	}
	if err := writeJSON(stdout, result); err != nil {
		fmt.Fprintf(stderr, "Error writing repair result: %v\n", err)
		return 1
	}
	return 0
}

func runMigrate(args []string, stdout, stderr io.Writer) int {
	flags, cfg := newScyllaFlagSet("migrate", stderr)
	timeout := flags.Duration("timeout", 5*time.Minute, "maximum migration duration")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *timeout <= 0 {
		fmt.Fprintln(stderr, "migrate requires a positive --timeout and accepts no positional arguments")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	log := zap.New(zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(stderr), zap.InfoLevel))
	defer func() { _ = log.Sync() }()
	if err := migrateSchema(ctx, cfg(), log); err != nil {
		fmt.Fprintf(stderr, "Migration failed: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Schema migrations complete.")
	return 0
}

func newScyllaFlagSet(name string, stderr io.Writer) (*flag.FlagSet, func() config.ScyllaConfig) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	hosts := flags.String("hosts", envOrDefault("GITSTORE_API__DATASTORE__SCYLLA__HOSTS", "localhost:9042"), "comma-separated Scylla host:port endpoints")
	keyspace := flags.String("keyspace", envOrDefault("GITSTORE_API__DATASTORE__SCYLLA__KEYSPACE", "gitstore"), "Scylla keyspace")
	username := flags.String("username", os.Getenv("GITSTORE_API__DATASTORE__SCYLLA__USERNAME"), "Scylla username")
	tls := flags.Bool("tls", envBool("GITSTORE_API__DATASTORE__SCYLLA__TLS"), "enable TLS")
	disableShardAware := flags.Bool(
		"disable-shard-aware-port",
		envBool("GITSTORE_API__DATASTORE__SCYLLA__DISABLE_SHARD_AWARE_PORT"),
		"disable shard-aware port discovery",
	)
	return flags, func() config.ScyllaConfig {
		return config.ScyllaConfig{
			Hosts:                 splitNonEmpty(*hosts),
			Keyspace:              *keyspace,
			Username:              *username,
			Password:              os.Getenv("GITSTORE_API__DATASTORE__SCYLLA__PASSWORD"),
			TLS:                   *tls,
			DisableShardAwarePort: *disableShardAware,
		}
	}
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envBool(key string) bool {
	value, err := strconv.ParseBool(os.Getenv(key))
	return err == nil && value
}

func splitNonEmpty(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func randomBase64URLSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
