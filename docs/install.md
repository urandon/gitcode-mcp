# Install

## Prerequisites

- Go 1.22 or later
- Git (optional, for source builds)

## From source

```sh
git clone <repository-url> gitcode-mcp
cd gitcode-mcp
go build -o gitcode-mcp ./cmd/gitcode-mcp
```

Move the binary to your PATH:

```sh
cp gitcode-mcp /usr/local/bin/
```

## Using `go install`

```sh
go install ./cmd/gitcode-mcp
```

## Verify installation

```sh
gitcode-mcp --version
```

Expected output:

```text
gitcode-mcp 0.1.0
```

## Verify help

```sh
gitcode-mcp --help
```

Expected output includes:

```text
Usage: gitcode-mcp [global flags] <command> [args]

Global flags:
  --mcp                 run stdio MCP server
  mcp serve             run MCP server with stdio or HTTP/SSE transport
  --cache-path PATH     cache database path
  --timeout DURATION    startup default timeout
  --max-size BYTES      maximum GitCode response size
  --format FORMAT       default output format
  --version             print version
  -h, --help            show help
```

## Verify test suite

```sh
go test ./...
```

Expected: all tests pass. No network access is required for the test suite.
