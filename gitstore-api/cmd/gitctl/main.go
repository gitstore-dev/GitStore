// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

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

	case "gen-jwt-secret":
		return printSecret(stdout, stderr, "GITSTORE_AUTH__JWT__SECRET")

	case "gen-hmac-secret":
		return printSecret(stdout, stderr, "GITSTORE_AUTH__GRPC__HMAC_SECRET")

	case "scylla-projection-audit":
		return runProjectionAudit(args[1:], stdout, stderr)

	case "scylla-projection-repair":
		return runProjectionRepair(args[1:], stdout, stderr)

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
	fmt.Fprintln(output, "  gen-jwt-secret")
	fmt.Fprintln(output, "  gen-hmac-secret")
	fmt.Fprintln(output, "  scylla-projection-audit [Scylla flags]")
	fmt.Fprintln(output, "  scylla-projection-repair (--dry-run | --confirm) [Scylla flags]")
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
