// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/rbaclocal"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/staticusers"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla"
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

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}

	switch args[0] {
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
		return printSecret(stdout, stderr, "GITSTORE_AUTH__JWT__SECRET")

	case "gen-hmac-secret":
		return printSecret(stdout, stderr, "GITSTORE_AUTH__GRPC__HMAC_SECRET")

	case "scylla-projection-audit":
		return runProjectionAudit(args[1:], stdout, stderr)

	case "scylla-projection-repair":
		return runProjectionRepair(args[1:], stdout, stderr)

	case "enroll-serviceaccount":
		return runEnrollServiceAccount(args[1:], stdout, stderr)

	case "generate-serviceaccount-key":
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
	fmt.Fprintln(output, "  hash-password <password>")
	fmt.Fprintln(output, "  users add --file <users.yaml> --username <name> --password-stdin [--email <email>] [--display-name <name>]")
	fmt.Fprintln(output, "  rbac role add --file <policy.yaml> --name <role> [--allow <action>] [--deny <action>]")
	fmt.Fprintln(output, "  rbac binding add --file <policy.yaml> --subject <subject> --role <role>")
	fmt.Fprintln(output, "  gen-jwt-secret")
	fmt.Fprintln(output, "  gen-hmac-secret")
	fmt.Fprintln(output, "  scylla-projection-audit [Scylla flags]")
	fmt.Fprintln(output, "  scylla-projection-repair (--dry-run | --confirm) [Scylla flags]")
	fmt.Fprintln(output, "  enroll-serviceaccount --api-url <url> --admin-token <token> --namespace <namespace> --name <name> --key-id <id> --private-key-path <path> [--replace-existing-key]")
	fmt.Fprintln(output, "  generate-serviceaccount-key --private-key-path <path>")
	fmt.Fprintln(output, "  validate-local-config --config-file <config.toml> --policy-file <policy.yaml>")
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
		var allow, deny actionList
		flags.Var(&allow, "allow", "allowed action; repeat or use commas")
		flags.Var(&deny, "deny", "denied action; repeat or use commas")
		if err := flags.Parse(args[2:]); err != nil {
			return 2
		}
		if *path == "" || strings.TrimSpace(*name) == "" {
			fmt.Fprintln(stderr, "rbac role add requires --file and --name")
			return 2
		}
		if err := rbaclocal.AddRole(*path, *name, rbaclocal.RolePolicy{Allow: allow, Deny: deny}); err != nil {
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
		if err := flags.Parse(args[2:]); err != nil {
			return 2
		}
		if *path == "" || strings.TrimSpace(*subject) == "" || strings.TrimSpace(*role) == "" {
			fmt.Fprintln(stderr, "rbac binding add requires --file, --subject, and --role")
			return 2
		}
		added, err := rbaclocal.AssignRole(*path, *subject, *role)
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
	fmt.Fprintln(stdout, "Reminder: add a role_bindings entry in policy.yaml before reloading the API.")
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

func newScyllaFlagSet(name string, stderr io.Writer) (*flag.FlagSet, func() config.ScyllaConfig) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	hosts := flags.String("hosts", envOrDefault("GITSTORE_DATASTORE__SCYLLA__HOSTS", "localhost:9042"), "comma-separated Scylla host:port endpoints")
	keyspace := flags.String("keyspace", envOrDefault("GITSTORE_DATASTORE__SCYLLA__KEYSPACE", "gitstore"), "Scylla keyspace")
	username := flags.String("username", os.Getenv("GITSTORE_DATASTORE__SCYLLA__USERNAME"), "Scylla username")
	tls := flags.Bool("tls", envBool("GITSTORE_DATASTORE__SCYLLA__TLS"), "enable TLS")
	disableShardAware := flags.Bool(
		"disable-shard-aware-port",
		envBool("GITSTORE_DATASTORE__SCYLLA__DISABLE_SHARD_AWARE_PORT"),
		"disable shard-aware port discovery",
	)
	return flags, func() config.ScyllaConfig {
		return config.ScyllaConfig{
			Hosts:                 splitNonEmpty(*hosts),
			Keyspace:              *keyspace,
			Username:              *username,
			Password:              os.Getenv("GITSTORE_DATASTORE__SCYLLA__PASSWORD"),
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
