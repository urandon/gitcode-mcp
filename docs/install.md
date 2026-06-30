# Install

## Prerequisites

- Go 1.22 or later
- Git (optional, for source builds)

## From release binary

Download the archive and `checksums.txt` from the GitHub release mirror:

```sh
curl -LO https://github.com/urandon/gitcode-mcp/releases/download/v0.1.0/gitcode-mcp_v0.1.0_darwin_arm64.tar.gz
curl -LO https://github.com/urandon/gitcode-mcp/releases/download/v0.1.0/checksums.txt
grep 'gitcode-mcp_v0.1.0_darwin_arm64.tar.gz$' checksums.txt | shasum -a 256 -c -
tar -xzf gitcode-mcp_v0.1.0_darwin_arm64.tar.gz
install -m 0755 gitcode-mcp_v0.1.0_darwin_arm64/gitcode-mcp /usr/local/bin/gitcode-mcp
```

Release artifacts are built for:

- `darwin/arm64`
- `linux/amd64`
- `linux/arm64`
- `windows/amd64`

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
  --timeout DURATION    CLI operation and GitCode request timeout
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
