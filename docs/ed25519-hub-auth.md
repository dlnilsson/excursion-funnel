# Ed25519 Authentication for Remote Quack Hubs

## Summary

When `EF_HUB_ADDR` is configured, all EF clients authenticate to `ef hub`
using an allowed Ed25519 SSH key. Keep `EF_HUB_TOKEN` as a hub-side Quack
configuration value, but never distribute or accept it as a remote client
credential.

`EF_HUB_ADDR` exposes a single EF gateway that:

- Handles Ed25519 challenge-response endpoints.
- Proxies Quack traffic to a loopback-only internal Quack listener.
- Issues five-minute Quack session credentials after successful
  authentication.

Standalone mode and its local Quack authentication remain unchanged.

This is a hard cutover: existing deployments that rely on a shared
`EF_HUB_TOKEN` client credential must reconfigure clients with Ed25519 keys.
There is no dual-auth or migration path — see Assumptions.

## Configuration and Interfaces

- Add required hub setting `EF_HUB_AUTHORIZED_KEYS` /
  `--hub-authorized-keys`.
  - Use OpenSSH `authorized_keys` format.
  - Load the file once during hub startup.
  - Accept only ordinary `ssh-ed25519` keys.
- Add `EF_HUB_QUACK_ADDR` / `--hub-quack-addr`, defaulting to
  `127.0.0.1:9495`.
  - Use it only for the private `ef hub` Quack listener.
  - Require it to resolve to a loopback address.
- Keep `EF_HUB_TOKEN` / `--hub-token`.
  - Require it only on the hub.
  - Pass it to `quack_serve`, but ignore it in the custom remote
    authentication callback.
  - Possessing this token must not allow remote authentication.
- Whenever a client has `EF_HUB_ADDR` configured, require Ed25519
  authentication.
- Add optional `EF_HUB_KEY` / `--hub-key` to prioritize a specific client
  private-key file while retaining SSH-agent fallback (Unix-like clients
  only — see Client Key Discovery).
- Retain the existing `-insecure` / `EF_HUB_INSECURE` flag as-is; HTTPS
  remains required unless explicitly disabled. Do not introduce a
  differently-named `--hub-insecure` flag.
- New dependency: `golang.org/x/crypto/ssh` and `golang.org/x/crypto/ssh/agent`
  for OpenSSH/PKCS#8 key parsing, `authorized_keys` parsing, fingerprinting,
  and SSH-agent signing. Neither exists in `go.mod` today. No Windows
  named-pipe agent library is needed, since Windows clients do not get
  agent/discovery support (see below).
  - The hub's actual signature check does not go through `x/crypto/ssh`'s own
    verification path. Extract the raw 32-byte Ed25519 public key from the
    parsed `ssh.PublicKey` (via its `CryptoPublicKey`/marshaled form) and
    verify with stdlib `crypto/ed25519`:
    `if ed25519.Verify(pub, challenge, sig) { ... }`. `x/crypto/ssh` is used
    only for format parsing/agent I/O, not as the source of truth for
    whether a signature is valid.

## Authentication Design

The EF gateway listens on `EF_HUB_ADDR`. It handles:

- `GET /api/v1/auth/challenge`
- `POST /api/v1/auth`

All other requests are reverse-proxied to loopback Quack with streaming
preserved and the upstream host rewritten appropriately. The internal Quack
port is never exposed externally and starts with
`allow_other_hostname=false`.

Note: DuckDB's Quack remote protocol (`quack_serve`, `ATTACH ... TOKEN`,
`quack_authentication_function`) is a beta feature as of the pinned driver
(`duckdb-go v2.10505.0` / DuckDB 1.5.5), shipped in DuckDB 1.5.3 with GA
targeted alongside DuckDB v2.0 later in 2026. Function names, settings, and
defaults may still change across DuckDB upgrades; track this when bumping
the driver version.

### Challenge Flow

1. The client requests a challenge.
2. The hub returns a random 32-byte base64url challenge, opaque ID, and
   60-second expiration.
3. The client signs a domain-separated payload containing the protocol
   version, challenge ID, and challenge bytes.
4. The client submits its canonical OpenSSH public key and base64url
   signature.
5. The hub confirms that:
   - The challenge exists and has not expired.
   - The key is an allowed `ssh-ed25519` key.
   - The Ed25519 signature is valid.
6. The challenge is atomically consumed after successful authentication.
7. The hub returns a cryptographically random Quack session credential valid
   for five minutes.

Rate-limit failed authentication attempts and bound outstanding challenges.
Never log challenges, signatures, private material, or session credentials.

### Quack Session Validation

- Maintain an in-process hub-auth session map (not a persisted table),
  keyed by SHA-256 credential hash, storing:
  - Authorized-key fingerprint.
  - Creation and expiration timestamps.
  - A persisted table buys no durability here: all sessions are invalidated
    on hub restart anyway (below), so persistence would only add write I/O
    on every 5-minute-TTL session mint for no benefit.
- Store no plaintext session credentials.
- Delete expired sessions from the map periodically.
- Install a Quack authentication macro and set it through
  `quack_authentication_function`.
- Have the macro hash `client_token` and accept it only when a matching,
  unexpired session exists.
- Deliberately ignore `server_token` in the macro, preventing
  `EF_HUB_TOKEN` from being used directly.
- Invalidate all sessions when the hub restarts (trivial: the map is
  in-process and does not survive restart).

## Client Key Discovery

Behavior differs by client platform:

**Unix-like clients:**

- Scan regular files under `$HOME/.ssh`.
- Skip directories, sockets, public/configuration files, and oversized files.
- Parse unencrypted OpenSSH and PKCS#8 private keys.
- Retain only standard Ed25519 signers.
- Skip encrypted files without prompting; use the SSH agent
  (`SSH_AUTH_SOCK`) for encrypted keys.
- Deduplicate candidates by SSH fingerprint.
- Try candidate keys until the hub accepts one, reusing an unconsumed
  challenge for failed key attempts.
- Return a clear error listing attempted fingerprints when no authorized key
  is available.

**Windows clients:**

- No automatic discovery and no SSH-agent (named pipe) support.
- Require `--hub-key` / `EF_HUB_KEY` pointing to an explicit unencrypted
  OpenSSH or PKCS#8 Ed25519 private-key file.
- If `EF_HUB_ADDR` is configured and `--hub-key` is unset, fail fast with a
  clear error instructing the user to set `--hub-key`.

On both platforms, `--hub-key` / `EF_HUB_KEY` takes priority when set.

The forwarder and remote reporter cache the session credential and
automatically repeat challenge-response after expiration or authentication
failure.

## Integration Changes

- `ef hub` starts:
  - The loopback Quack listener.
  - The EF authentication/proxy gateway on `EF_HUB_ADDR`.
  - The existing dashboard/health listener on `EF_ADDR`.
- Coordinate startup failure and graceful shutdown across all listeners.
- Note: today `EF_HUB_ADDR` is the raw Quack listener itself
  (`cmd/ef/main.go` `runHub`, `allow_other_hostname=true`), with TLS
  typically terminated externally (e.g. Tailscale). This change makes
  `EF_HUB_ADDR` an EF-owned gateway instead — any external TLS termination
  or reverse proxy must be repointed at the gateway, not the old Quack port.
- Update the forwarder to obtain a session before `store.OpenRemote` and pass
  the temporary session as Quack's `TOKEN`.
- Apply the same authenticated connection flow to remote `usage`, `tools`,
  `inspect`, and the serve-side dashboard.
- Preserve outbox retry, backoff, idempotency, and acknowledgement behavior.
- Keep standalone `ef serve`, local reports, and `LocalQuackToken` unchanged.
- Update help, README, service examples, and environment examples to explain
  that clients configure `EF_HUB_ADDR` and an SSH key (or, on Windows,
  `--hub-key`), while only the hub configures `EF_HUB_TOKEN`. Existing
  documentation instructing clients to share `EF_HUB_TOKEN` is replaced, not
  kept as a compatibility option.

## Test Plan

- Parse valid authorized-key files with comments/options; reject malformed
  files and every non-Ed25519 key.
- Verify startup-only key loading and restart-based revocation.
- Test unencrypted OpenSSH/PKCS#8 discovery, encrypted-file skipping, and
  SSH-agent discovery on Unix-like clients; test deduplication and
  explicit-key priority.
- Test the Windows path: explicit `--hub-key` succeeds; missing `--hub-key`
  with `EF_HUB_ADDR` set fails with a clear, actionable error and no
  discovery/agent attempt.
- Test valid login, unauthorized keys, altered signatures, expired/replayed
  challenges, bounded challenge storage, session expiration, renewal, and
  credential hashing.
- Confirm `EF_HUB_TOKEN` cannot authenticate through the gateway.
- Confirm the internal Quack listener rejects non-loopback configuration and
  cannot be reached through an unintended public bind.
- Test transparent Quack proxying, streaming responses, TLS defaults, and
  insecure opt-in.
- End-to-end test authorized forwarding, unauthorized outbox retention,
  outage recovery, idempotent replay, session renewal, remote reports, and
  the serve-side dashboard.
- Run `go test ./...`, Quack integration tests, `go vet ./...`, and
  `git diff --check`.

## Assumptions

- Remote hub access supports Ed25519 only; RSA, ECDSA, security-key variants,
  and token-only compatibility are out of scope.
- This is an intentional hard cutover: there is no backward compatibility
  with the current shared-`EF_HUB_TOKEN` client credential, and no dual-auth
  or migration path is provided. Existing client deployments must be
  reconfigured with Ed25519 keys as part of upgrading.
- Windows clients do not get automatic key discovery or SSH-agent support;
  they must configure `--hub-key` / `EF_HUB_KEY` explicitly.
- `EF_HUB_TOKEN` remains an internal implementation/configuration value and
  is never returned to clients.
- DuckDB's Quack protocol is a beta feature; its API may change before GA
  (targeted with DuckDB v2.0 later in 2026). This is an accepted dependency
  risk, tracked at DuckDB driver upgrade time.
- The hub dashboard retains its existing access policy; this change
  authenticates Quack-backed EF clients.
- Existing unrelated README changes remain untouched.
