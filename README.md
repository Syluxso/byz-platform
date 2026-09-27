# byz-platform

Paved-road platform libraries for Byzantine Go services and product APIs.

Not an SDK. Not a framework. A small set of composable packages with frozen
verbs so every Byz service shares the same auth, HTTP, logging, event and
process-lifecycle shape without adopting a monolith or a forced router.

- Platform services (`byz-kan`, `byz-ingest`, `byz-search`, ...) use these
  packages directly with `net/http`.
- Product APIs (`bb-api`, `hc-api`, `per-api`, `up-api`) keep Gin and use the
  `ginx` submodule, which wraps the same packages in thin Gin middleware.
- The core module never imports Gin, Echo, Chi, the MCP SDK, or any service or
  product module. `depguard` enforces this in CI.

Tracked on the Byzantine board: BYZ-16 (epic) and its children.

## Core packages (module `github.com/Syluxso/byz-platform`)

| Package | Frozen verbs | Status |
|---|---|---|
| `config` | `Env`, `Bool`, `Int`, `Duration`, `List`, `Load` | ready |
| `httpx` | `WriteJSON`, `WriteProblem`, `WriteUnauthorized`, `Page`, `RequestID`, `CORS`, `Health` | ready |
| `obs` | `New`, `LogsHandler`, `Metrics`, `HTTPMetrics`, `AuthRejected`, `RequestLogger` | ready |
| `iam` | `NewVerifier`, `Validate`, `FromRequest`, `ClaimsFrom`, `BearerFrom` | ready |
| `iamclient` | `NewTokenSource`, `Token` | ready |
| `tenant` | `From`, `RequireOrg`, `RequireTenant`, `RequireUser`, `AllowService` | ready |
| `downstream` | `New`, `NewRequest`, `Do`, `JSON` | ready |
| `shutdown` | `Run` | ready |
| `store` | `Open`, `WithTx`, `Health`, `IsUniqueViolation` | planned (BYZ-21) |
| `migrate` | `Run` over an embedded FS, name-keyed table | planned (BYZ-30) |
| `events` | `Emit` with the live flat camelCase envelope | planned (BYZ-18) |
| `fail` | `Report` slog + async Kafka | planned (BYZ-19) |
| `mcpx` | `Mount` with RFC 9728 metadata | planned (BYZ-48) |

## Submodules (own `go.mod`, pinned independently of core)

| Module | Purpose | Status |
|---|---|---|
| `ginx` | Gin adapters for iam, httpx, tenant | planned (BYZ-49) |
| `notify` | client for byz-notifications | planned (BYZ-27) |
| `files` | client for byz-file-service, caller bearer only | planned (BYZ-28) |
| `directory` | read-only display-name lookup | planned (BYZ-29) |

## Rules

- Token issuance and revocation live in byz-iam only. `iam` here verifies.
- `iam.FromRequest` does not require a tenant. Route policy is `tenant.RequireTenant`, `tenant.RequireOrg`, `tenant.RequireUser`.
- Bad or missing tokens are metrics, never `fail.Report` faults.
- `events.Emit` freezes the shape live consumers already parse. A breaking shape means a new topic name.
- SQL stays in the service. No ORM, no generated SQL in this module.
- `X-Request-Id` on the wire is `request_id` in logs and `traceId` on events. One value.

## Release policy (BYZ-50)

- Semver tags on core (`vX.Y.Z`) and on each submodule (`ginx/vX.Y.Z`, ...).
- `v0.x` until byz-kan adopts the module. `v1.0.0` freezes the verbs.
- Removing an envelope or `Claims` field is a major bump.
- CI runs `go vet`, `go test -race` on every module, and `golangci-lint` with `depguard` on core.

## Environment keys read by `config.Load`

| Key | Default | Field |
|---|---|---|
| `BIND` | `0.0.0.0` | `Bind` |
| `PORT` | per service | `Port` |
| `PUBLIC_URL` | per service | `PublicURL` |
| `IAM_JWKS_URL` | `https://iam.byzantineapp.dev/.well-known/jwks.json` | `JWKSURL` |
| `IAM_ISSUERS` | empty (no issuer check) | `Issuers` |
| `IAM_AUDIENCE` | empty (no audience check) | `Audience` |
| `KAFKA_BOOTSTRAP` | `127.0.0.1:9092` | `KafkaBrokers` |
| `BYZ_KAFKA_ENABLED` | `true` | `KafkaEnabled` |
| `DB_URL` | per service | `DBURL` |

Service-specific keys stay in the service.
