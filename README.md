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
| --- | --- | --- |
| `config` | `Env`, `Bool`, `Int`, `Duration`, `List`, `Load` | ready |
| `httpx` | `WriteJSON`, `WriteProblem`, `WriteUnauthorized`, `Page`, `RequestID`, `CORS`, `Health` | ready |
| `obs` | `New`, `LogsHandler`, `Metrics`, `HTTPMetrics`, `AuthRejected`, `RequestLogger` | ready |
| `iam` | `NewVerifier`, `Validate`, `FromRequest`, `ClaimsFrom`, `BearerFrom` | ready |
| `iamclient` | `NewTokenSource`, `Token` | ready |
| `tenant` | `From`, `RequireOrg`, `RequireTenant`, `RequireUser`, `AllowService` | ready |
| `downstream` | `New`, `NewRequest`, `Do`, `JSON` | ready |
| `shutdown` | `Run` | ready |
| `store` | `Open`, `WithTx`, `Health`, `IsUniqueViolation`, `Problem`, null helpers | ready |
| `migrate` | `Run`, `FromFS`, `Applied` over kan's name-keyed table | ready |
| `events` | `Emit`, `EmitAsync`, `Encode` with the live flat camelCase envelope | ready |
| `fail` | `Report` slog + bounded async Kafka, `SetDefault` | ready |
| `mcpx` | `Mount`, `MetadataFor` with RFC 9728 metadata | ready |

## Submodules (own `go.mod`, pinned independently of core)

| Module | Purpose | Status |
| --- | --- | --- |
| `ginx` | `Wrap`, `Auth`, `RequireTenant`, `RequestID`, `CORS`, `Metrics`, `Logger`, `Recovery`, `Health`, `Problem`, `Scope` | ready |
| `notify` | `Create`, `List`, `UnreadCount`, `MarkRead`, `MarkAllRead`, `Delete` | ready |
| `files` | `List`, `Meta`, `Upload`, `Content`, `Delete`, caller bearer only | ready |
| `directory` | `Profile`, `Profiles`, `Tenant`, `OrgProfile`, `WhoAmI`, `Lookup`, `Names` | ready |

Submodules carry a `replace github.com/Syluxso/byz-platform => ../` for local
development. Remove it once core is tagged and pin a version instead.

## Rules

- Token issuance and revocation live in byz-iam only. `iam` here verifies.
- `iam.FromRequest` does not require a tenant. Route policy is `tenant.RequireTenant`, `tenant.RequireOrg`, `tenant.RequireUser`.
- Bad or missing tokens are metrics, never `fail.Report` faults.
- `events.Emit` freezes the shape live consumers already parse. A breaking shape means a new topic name.
- SQL stays in the service. No ORM, no generated SQL in this module.
- `X-Request-Id` on the wire is `request_id` in logs and `traceId` on events. One value.
- Faults go to topic `byz.faults` (type `fault.reported`). byz-events must bootstrap that topic alongside the others.

## Testing

`go test ./...` runs everywhere. Kafka packages test against franz-go's in-process
fake cluster, so no broker is needed. `store` and `migrate` integration tests
run only when `BYZ_TEST_DB_URL` points at a Postgres and skip otherwise; CI
provides one.

## Release policy (BYZ-50)

- Semver tags on core (`vX.Y.Z`) and on each submodule (`ginx/vX.Y.Z`, ...).
- `v0.x` until byz-kan adopts the module. `v1.0.0` freezes the verbs.
- Removing an envelope or `Claims` field is a major bump.
- CI runs `go vet`, `go test -race` on every module, and `golangci-lint` with `depguard` on core.

## Environment keys read by `config.Load`

| Key | Default | Field |
| --- | --- | --- |
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
