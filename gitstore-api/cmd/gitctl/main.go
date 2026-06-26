// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package main

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: gitctl <subcommand> [args]\n")
		fmt.Fprintf(os.Stderr, "Subcommands:\n")
		fmt.Fprintf(os.Stderr, "  hash-password <password>\n")
		fmt.Fprintf(os.Stderr, "  gen-jwt-secret\n")
		fmt.Fprintf(os.Stderr, "  gen-hmac-secret\n")
		os.Exit(2)
	}

	switch os.Args[1] {
	case "hash-password":
		var password string
		if len(os.Args) >= 3 {
			password = os.Args[2]
		} else {
			// Read from stdin to avoid exposing the password in the process list.
			scanner := bufio.NewScanner(os.Stdin)
			if !scanner.Scan() {
				fmt.Fprintf(os.Stderr, "Error reading password from stdin\n")
				os.Exit(1)
			}
			password = strings.TrimRight(scanner.Text(), "\r\n")
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error generating hash: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(hash))

	case "gen-jwt-secret":
		secret, err := randomBase64URLSecret()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error generating secret: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("GITSTORE_AUTH__JWT__SECRET=%s\n", secret)

	case "gen-hmac-secret":
		secret, err := randomBase64URLSecret()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error generating secret: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("GITSTORE_AUTH__GRPC__HMAC_SECRET=%s\n", secret)

	default:
		fmt.Fprintf(os.Stderr, "Unknown subcommand: %s\n", os.Args[1])
		os.Exit(2)
	}
}

func randomBase64URLSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
