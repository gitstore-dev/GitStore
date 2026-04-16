# gitstore Development Guidelines

Auto-generated from all feature plans. Last updated: 2026-03-26

## Active Technologies

- (001-git-backed-ecommerce)

## Commands

### Workspace

## Code Style

: Follow standard conventions

## Recent Changes

- 001-git-backed-ecommerce: Added

<!-- MANUAL ADDITIONS START -->
Before a PR is raised:

```bash
# Check formatting and clippy for Rust
cd git-server
cargo fmt --all -- --check
cargo clippy --all-targets --all-features -- -D warnings
cargo build --verbose
cargo test --verbose

# Check formatting and linting for Go
cd ../api
if [ "$(gofmt -s -l . | wc -l)" -gt 0 ]; then
	echo "The following files need formatting:"
	gofmt -s -l .
	exit 1
fi
go vet ./...
# Setup $GOPATH/bin in PATH if not already
go install honnef.co/go/tools/cmd/staticcheck@latest
staticcheck ./...
go build -v ./...
go test -v -race -coverprofile=coverage.txt -covermode=atomic ./...
```
<!-- MANUAL ADDITIONS END -->
