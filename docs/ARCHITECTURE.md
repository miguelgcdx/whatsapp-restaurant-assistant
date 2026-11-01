# WhatsApp Restaurant Assistant — Architecture baseline

**Status: Local foundations; product incomplete.** Synthetic backend and [staff access](local-auth.md) have local checks/proof limits, not production approval. Orders/AI/live integrations remain gated; no real accounts/deployment. [Design map](product/README.md).

## Product Definition

WhatsApp text ordering and staff dashboard for one test restaurant/number: explicit pickup/restaurant-managed delivery confirmation, staff fulfillment/exceptions. Learning/testing, not production or validated commercial benefit.

[PRD.md](PRD.md) owns outcomes, [MVP.md](MVP.md) scope, [REQUIREMENTS.md](REQUIREMENTS.md) all 28 rules/examples. Those rules govern these abbreviated flows; broader brief proposals remain unapproved.

## Actors and Jobs

| Actor | Job / authority / surface |
|---|---|
| Customer | WhatsApp: browse/order, confirm summaries/revisions, request cancellation; no price/staff authority. |
| Staff | Dashboard: review coverage, accept/reject, progress fulfillment, record cash, converse, revise/cancel, return AI control. |
| Delivery person | Manual staff coordination: wait for food and deliver; no app/account/assignment system. |

A future buyer is not a separate dashboard role; no admin hierarchy is specified.

## Main Flows

| Trigger | Input → processing/state → output; failure/handoff |
|---|---|
| Webhook | Verify → persist event/job → acknowledge → worker context/Go operations → Meta reply. |
| Confirmation | Present full summary/total → ask if anything else → clear completion submits once; more items continue cart/summary. Go validates details → order/outbox/dashboard/receipt. No extra confirmation prompt; ambiguity never submits. |
| Staff action | Authoritative Next.js session/token → Go verification/membership → result; invalid authority denied. |
| Fulfillment/chat | Staff review coverage, progress fulfillment/payment, send exact text. Takeover pauses AI replies only; constrained status notifications continue until/after explicit return. |
| Revision | Preserve items/total, release capacity awaiting response; confirmation updates same order/requeues/recalculates wait, never returns AI. Silence/decline cannot apply/cancel. |
| Later additions | FR-24: unpaid/unaccepted → amendment; paid → linked unpaid order; accepted/preparing → staff review; ready/later → new order. Staff-approved same-trip extras share delivery fee; never reopen. |
| Cancellation | Unpaid/unaccepted → customer cancellation unless shared settlement; accepted/paid/shared → staff decision; resolve affected amounts, no automatic fee/transfer/refund. Terminal orders cannot cancel again. |

## Architecture Overview

**Selected:** Go modular monolith for business rules/AI orchestration; API and worker processes from one codebase; Next.js dashboard; PostgreSQL; direct Meta Cloud API; CommandCode model access. Personal-test topology: local Go, Vercel Next.js, remote Neon PostgreSQL.

Local backend: Go1.27.3 `net/http` ServeMux, pgx5.11.0/pgxpool, sqlc1.31.1 and Goose3.29.0; pins and runtime checks live in [backend](../backend/README.md). API/worker share operations; no ORM, generic repositories, microservices, full DDD/CQRS/event sourcing or wrapper per call. [Data](product/data-contracts.md) and [operation contracts](product/api-contracts.md) describe planned product behavior, not implemented domain tables.

## System Map

| Component/module | Input → responsibility/output | State / dependencies |
|---|---|---|
| Next.js/BFF | Staff requests → views/Go calls | Better Auth/Neon; tunnel. No browser DB writes/business authority. |
| Go API | Verified events → jobs; authorized requests → results | Domain operations; separate webhook/staff boundaries. |
| Catalog/restaurant | Queries/settings → menu/availability/open/fee | Catalog/settings; no editor/automatic coverage. |
| Cart/pricing | Edits/fulfillment → summary/totals | Cart/unit notes; catalog. |
| Orders/preparation | Confirmations/decisions → transitions/wait | Orders/revisions/workload/payment; cart/catalog. |
| Conversations/control | Messages → history/exact staff text/control | Order links/takeover/pending confirmation. |
| AI orchestration | Context → bounded requests/replies | Domain operations/CommandCode; no authority. |
| Worker/adapters | Jobs/outbox → claims/scheduling/sends | Leases/attempts/generations; Neon/Meta/CommandCode. |

Modules are not deployments. Domain owners own SQL/validation; sqlc generates access, not policy. Translate transport/DB/provider types at boundaries.

## Ownership

Go owns business validation/authorization/records/totals/transitions. **Better Auth in Next.js is the single identity/session authority**, owning Neon auth records as the explicit Go DB-ownership exception, not a second Go login system.

Orders owns fulfillment/revision/queue/payment; conversations owns AI/staff control. Shared transactions preserve FR-13–FR-24; dashboard/AI never duplicate rules. PostgreSQL is truth; local attempts do not prove Meta delivery. Better Auth owns auth migrations, Goose business migrations, never auth. Local `auth`/`app` ownership and explicit migration ordering are verified; production remains unselected.

## Data and State

[Data contracts](product/data-contracts.md) own planned product records/transitions/atomic operations. Local pipeline and identity/membership DDL exists separately; restaurant workflow DDL remains pending. Go owns catalog/cart, conversations/control, orders/revisions/workload/payment and durable jobs/outbox; Better Auth owns identity/session/key records.

Fulfillment, payment, control and revision stay independent. PostgreSQL stores confirmed snapshots, per-unit notes, history and recovery state; chat is not an order edit. Wait uses accepted waiting/preparing work and elapsed preparation (FR-09–FR-12), never automatically changes readiness. Pending revision releases capacity indefinitely; partly prepared effort and retention remain unresolved. Overdue Preparing work makes the estimate unavailable without automatic slot release.

## AI and Agent Responsibilities

One assistant uses stored menu/availability, cart, messages and order/control state; no invented long-term memory. [Tool contracts](product/api-contracts.md#2-webhooks-and-internal-tools) define bounded cart/fulfillment/summary/submission/revision/cancellation operations. Model compatibility and confirmation recognition remain gated.

AI interprets intent naturally like an employee, not a command parser. Go checks verified-message/candidate binding, authority and version; it does not independently determine language meaning. Bare tool assertions grant nothing. AI cannot invent items/prices/charges, mark paid, accept/fulfill, bypass takeover or authorize writes; staff text stays exact. Persist messages/outcomes for recovery; retention and automatic handoff triggers remain pending.

## External Integrations

Provider deadlines, retry counts, rate limits and concrete identity formats remain pending.

| Integration / owner | Purpose, protocol/auth/data | Validation / timeout, retry, idempotency |
|---|---|---|
| Meta / Go | Direct Cloud API/webhooks; text/status/reference data; server credentials. | Verify/persist before acknowledgement; deduplicate, obey windows/templates. Setup/recovery pending. |
| CommandCode / Go | Context/tools → output; **“deepseek 4.1 flash”** is an unverified label, not API ID; user config later. | Endpoint/model/route/protocol/SDK/auth/tools/terms/limits/retries unverified; no assumed SDK/live calls. |
| Neon / persistence | Remote PostgreSQL/TLS; separate business/auth ownership; server credentials. | pgx runtime pools within connection limits; Goose uses direct connection. Schema/pool settings pending; idle health SQL must not keep compute awake. |
| Better Auth / Next.js | Local1.7.12 library; uncached DB sessions and official server-only EdDSA/JWKS. | Exact issuer/audience, signed lifetime**≤60s**, current Go membership; bounded direct BFF. Production/elapsed rotation proof pending. |
| HTTPS tunnel | Meta/Next.js → local Go HTTPS. | Provider/stable URL/security/recovery pending; reachability is not authentication. |

References (local proof/limits: [staff access](local-auth.md)): Better Auth [sessions](https://www.better-auth.com/docs/concepts/session-management), [JWT/JWKS](https://www.better-auth.com/docs/plugins/jwt), [security](https://www.better-auth.com/docs/reference/security).

## Sync vs Async

Session checks/staff commits are synchronous; model/delivery async for latency/retries/recovery. Persist verified inbound work before acknowledgement; domain mutation/outbox intent commit atomically.

After commit API sends a best-effort hint via OS-permission-protected **local Unix-domain socket** to worker: no business data/authority, queue/broker or public endpoint. Coalesce hints; log failures/use bounded hint retries. Missing hints never erase work.

Worker checks durable state at startup, drains runnable jobs on hints, sets timers for persisted delayed retries and reloads schedules on restart; timers are not truth. Configurable **30-minute recovery scans** catch missed hints/crash between commit and wake. Normal events wake immediately; fallback may take the configured interval while processes/DB are available, not 24/7.

## Failure and Concurrency

- Bounded worker claims queued/due jobs in short `FOR UPDATE SKIP LOCKED` transactions, persisting lease/attempt/generation. Commit/release before LLM/Meta calls; never hold DB transactions across external I/O. Reclaim expired leases with conditional generation checks.
- Persist per-conversation single-flight lease/sequence. Staff operations atomically update control/version. Recheck lease ownership and conversation/order version before committing actions and again before dispatching AI replies; reject stale completion/confirmation. Lease expiry alone cannot fence external side effects.
- An already-in-flight send cannot be retracted. Takeover/in-flight ordering policy remains a design gate, not a guarantee supplied by leases.
- Unique event/submission identities prevent duplicate orders (NFR-02); concrete key scope/correlation remain pending. Unknown external send outcomes become **reconciliation-required operation results**, never blind retries. Outbox does not promise exactly-once sends or add a new UI feature.
- Worker owns recovery. Retry counts/backoff, provider deadlines, reconciliation capabilities and staff failure procedures remain pending. Every send obeys NFR-04; outside-window/failure policy remains open.

## Runtime and Deployment

```text
Staff browser -> Vercel Next.js/BFF -> HTTPS tunnel -> local Go API
Customer WhatsApp <-> Meta -> HTTPS tunnel -> local Go API
Local Go API + worker -> remote Neon PostgreSQL (TLS)
Local Go worker -> Meta / CommandCode
```

API/worker run on the **same local Linux machine** (`cmd/api`/`cmd/worker`: illustrative). Unix wakeup is local-only; moving hosts requires redesign, not a hidden broker/adapter. Windows portability is unselected.

No quiescent hot polling, DB LISTEN or persistent pool health queries. Close idle pools/disable background health SQL as supported; verify pgx settings allow Neon suspension. Known-job timers wake as needed. With the previously verified five-minute idle window, 30-minute scans suggest roughly four awake hours/day **before** wake latency/other work: not a hard bound/free-quota promise. Dashboard/auth activity keeps compute awake too; serverless Next.js auth calls have a separate DB lifecycle.

Free-tier limits/Vercel Hobby commercial terms and separate model costs remain risks. Local processes/network/tunnel must be available; hosted UI/DB is not always-on. Railway/Render/OCI unselected. Regions, pool/TLS/secrets/logs/retention/backups/restore/access remain pending.

## Security and Authority

Administrative provisioning; no public registration, customer dashboard accounts, social login or organization plugin.

- Same-origin protected Next.js handlers/BFF check authoritative sessions, not middleware alone; disable session cookie caching. HttpOnly browser cookie (Secure on deployed HTTPS); API tokens server-side, never local storage.
- Official short-lived JWT/JWKS, not raw token-table shortcuts/custom HMAC. Go validates signature, allowed algorithm, issuer, audience, expiry, application-owned membership/permissions; never trust unsigned identity headers/browser roles/restaurant IDs.
- Revocation stops issuance, but issued tokens may survive ≤60 seconds. Immediate revocation needs another authoritative check; not claimed.
- Local public auth exposes only GET JWKS. Server actions require explicit matching Origin; BFF rejects supplied foreign Origin, queries/body metadata and caller authority. Fixed direct Go path, bounded transport, strict DTO; production remains gated.
- Signed Meta webhook and authenticated staff API are separate boundaries. Tunnel/worker execution is not staff login. DB/Meta/CommandCode credentials stay server-side; Neon uses TLS.

### Fictional authentication fixtures

**Staff Alex** (`alex.staff@example.test`) and **Staff Sam** (`sam.staff@example.test`) are fictional local fixtures, generated only by explicit setup. Supported library hashing/provisioning, private generated credentials, crash-released locking and unchanged-state recovery are tested; no defaults, password resets or membership reactivation. Same test restaurant; no multi-restaurant/admin feature.

Local component/Go/browser checks include session isolation/logout, authoritative membership and failures. Final independent runtime was unavailable; human accepted independent static plus parent real runtime for local delivery, with PARTIAL retained. No elapsed60s rotation/cache proof, real emails/accounts or Neon seeding; see [evidence](local-auth.md#verification-and-evidence).

## Design Decisions

| Decision | Reason | Implication |
|---|---|---|
| ServeMux/pgxpool/sqlc/Goose | Direct typed SQL, no ORM/layers. | Local pins/guarded pools tested; production settings pending. |
| Next.js/Better Auth identity; Go business authority | No duplicate identity system. | Auth DB exception; BFF/JWT cannot replace Go authorization. |
| DB jobs/outbox + socket/timers/recovery scans | Immediate wake/recovery, no hot polling. | Durable leases; uptime/compute/send ambiguity. |
| Direct Meta/CommandCode | Selected messaging/model access. | Verify capabilities; label is not API ID. |
| Local Go/Vercel/Neon | Personal testing. | Uptime/tier/idle quota limits. |

## Tradeoffs and Alternatives

- We accept local uptime/tunnel dependence in exchange for personal testing instead of hosted Go.
- We accept remote compute cost and up-to-scan-interval missed-hint recovery in exchange for managed PostgreSQL without hot polling.
- We accept bounded revocation delay in exchange for short-lived verification without an extra authoritative check per request.
- We accept durable recovery complexity for provider latency/retries outside webhook lifetimes without a broker.

## Not Needed Yet

[MVP exclusions](MVP.md#outside-this-version): multi-restaurant/billing, online payment, driver/tracking/maps/delivery/POS integrations, menu editing/stock, voice, campaigns/advanced analytics. Scope must change before expansion.

No Redis, external queue/event bus, vector DB or multiple agents without demonstrated caching, buffering/fan-out, semantic retrieval or distinct-agent needs. PostgreSQL work/outbox and one assistant suffice; no microservices/full DDD/CQRS/event sourcing without requirements.

## Risks and Open Questions

| Classification | Concern / gate |
|---|---|
| Known risk | Local/tunnel downtime, missed-hint latency, in-flight sends, revocation window, tier/compute costs; no reliability/compliance claim. |
| Unverified assumption | CommandCode supports needed tool workflow/model compatibility. |
| Pending external research | CommandCode endpoint/model/route/protocol/auth/tools/terms; provider limits, WhatsApp windows/templates, reconciliation. |
| Pending product decision | [Behavior gaps](REQUIREMENTS.md#open-decisions-and-boundaries): sample menu/prices/currency/fee, delivery-to-pickup, closed browsing, partly prepared food/restart effort, late cancellation/refunds, handoff/notification/failure policy. |
| Pending design decision | Executable DDL, contextual intent evaluation/presentation correlation, lease durations/backoff/counts, in-flight takeover ordering and reconciliation. Internal routes/tools/identities are defined in the linked contracts, not implemented. |
| Remaining auth gate | Local identity/access is implemented, not order permissions. Production cookies/TLS/key rotation/operations and elapsed cache timing remain unverified; final independent runtime limitation explicitly accepted for local delivery only. |
| Pending design decision | Tunnel/static URL/security, TLS/pool/idle settings, retention/backups/operations; production CI and live-test approval. |

## Implementation Order

Current delivery sequence; only the scoped local increments were authorized. Read REQUIREMENTS before each later slice:

1. **Local foundation — verified:** synthetic signed ingress→durable job→separate worker→fake reply, including replay/order/fencing/recovery; no AI/live credentials. [Backend evidence](../backend/README.md).
2. **Protected dashboard — access foundation:** local auth/BFF/Go membership, generated fixtures and minimal shell checked with explicit limits. [Evidence](local-auth.md). Order views/workflows are NOT implemented; native/local delivery has separate outcomes.
3. **Confirmed ordering:** catalog/cart/pricing/explicit submission and dashboard receipt; integrate model only after capability verification. Gate confirmation identity and product gaps (FR-01–FR-14).
4. **Staff workflows:** coverage/fulfillment/payment/chat/takeover/revisions/cancellation, atomic control and race checks; gate remaining policies (FR-15–FR-24). Live Meta/tunnel/Vercel/Neon deployment/testing requires separate explicit authorization.

**Local checks:** Go normal/race/vet,22 auth/contract tests, real Playwright cookies→JWT/JWKS→Go/outages and build. Owned PostgreSQL/state only. Final independent runtime unavailable; static review plus parent runtime accepted for LOCAL delivery, retaining PARTIAL. [Tracker](../odd/tasks/protected-dashboard.md) records results and separate scratch-format limitation.

The 28 restaurant rules and later domain/race scenarios are NOT implemented/proved by these foundation checks. Keep future fake providers/isolated databases and domain/HTTP/browser tests; live API/deployment checks require separate explicit authorization.

## Canonical Documentation Map

[Product design map](product/README.md) is the split design entry point. **`docs/ARCHITECTURE.md`** retains the requested overview filename; details live under `docs/product/` to respect document size limits.

| Document | Owns / read when |
|---|---|
| [Root README](../README.md) | Navigation/existing setup; start here. |
| [PRD.md](PRD.md) | Purpose/users/goals/outcomes; product intent. |
| [MVP.md](MVP.md) | Scope/journeys; before expansion. |
| [REQUIREMENTS.md](REQUIREMENTS.md) | Authoritative 28 rules/examples/gaps; before behavior design/testing. |
| This architecture | Foundation/boundaries/gates; before implementation planning. |
| [Design map](product/README.md) | Entry point for overview and detailed contracts. |
| [Data contracts](product/data-contracts.md) / [Schema](product/postgres-schema.md) | Logical model and planned tables/keys/indexes; before persistence/migrations. |
| [API/tool contracts](product/api-contracts.md) | HTTP, tools, errors/idempotency and rule coverage; before adapter work. |
| [PROJECT_BRIEF.md](PROJECT_BRIEF.md) | Immutable original blueprint; historical proposals not automatically selected. |
