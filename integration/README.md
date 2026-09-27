# Server and CLI connection test

This opt-in suite starts a real `tunnel-server` and two `tunnelx-cli` processes. It verifies administrator login, account creation, automatic key enrollment, HTTP traffic through the SSH relay, update-policy grace and expiry, cancellation/reconnection, and preservation of keys and configuration.

The fixture provisions only the initial administrator in a temporary database before the server starts. Account creation and policy changes use the authenticated admin HTTP API; client login uses the same token-protected local API as the desktop application. All listeners use loopback. No production service or existing credentials are used, and child processes are cleaned up on failure.

Run on Linux from the repository root:

```bash
export TUNNELX_TEST_BIN_DIR="$(mktemp -d)"
version="$(tr -d '\r\n' < CLI_VERSION)"
go build -trimpath -ldflags "-X main.Version=$version" -o "$TUNNELX_TEST_BIN_DIR/tunnel-server" ./cmd/tunnel-server
go build -trimpath -ldflags "-X main.Version=$version" -o "$TUNNELX_TEST_BIN_DIR/tunnelx-cli" ./cmd/tunnelx-cli
go test -tags=integration ./integration -v -count=1 -timeout=3m
```

The reusable `.github/workflows/linux-connection.yml` runs this suite for both security checks and desktop releases. Desktop packaging waits for it to pass. Private test credentials, database files and endpoint tokens are not uploaded as artifacts.
