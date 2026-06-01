# Controller Manager

Controller manager service skeleton for GitStore control loops.

## Tech Stack

- **Language**: Go 1.25
- **Module**: `github.com/gitstore-dev/gitstore/controller-manager`

## Project Structure

```text
gitstore-controller-manager/
├── cmd/
│   └── controller/   # Controller manager entrypoint
└── go.mod
```

## Running

From the repository root:

```bash
go run ./gitstore-controller-manager/cmd/controller
```

Or from the module directory:

```bash
cd gitstore-controller-manager
go run ./cmd/controller
```

## Development

```bash
go build -v $(go list -f '{{.Dir}}/...' -m)
cd gitstore-controller-manager
go test ./...
```

## License

AGPL-3.0-or-later — see [LICENCE](../LICENSE).
