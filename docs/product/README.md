# Product design map

**Status:** Design only; not an implementation or live-integration claim. This is the entry point for the split technical design. Read only the concerns needed for a task; product rules and unresolved business choices remain authoritative outside this folder.

| Document | Owns / read when |
|---|---|
| [Architecture overview](../ARCHITECTURE.md) | Selected topology, ownership, integrations, security, worker recovery, tradeoffs and implementation order. Start here for system context. |
| [Data and transaction contracts](data-contracts.md) | Record identities/relationships, money/unit modeling, independent states and atomic confirmation/revision/control operations. Read before persistence or domain design. |
| [PostgreSQL schema](postgres-schema.md) | Planned business tables, keys, constraints, indexes and migration ownership. Read after data contracts and before writing SQL/migrations; not applied DDL. |
| [API and tool contracts](api-contracts.md) | Staff routes, result/error/idempotency conventions, webhook ingestion boundary, internal tool catalog and rule traceability. Read before HTTP/BFF/AI adapters. |
| [Staff API wire schemas](http-schemas.md) | Exact JSON fields/types, action variants, endpoint results, validation limits and literal examples. Read with API contracts before BFF/Go/dashboard implementation. |
| [Bounded staff reads](bounded-reads.md) | Header/collection separation, response-byte budgets, snapshot-bound cursors and complete message-text retrieval. Read with wire schemas before dashboard/read handlers. |
| [Requirements](../REQUIREMENTS.md) | All 28 behavior rules, acceptance examples and pending product choices; read before implementing or testing a rule. |
| [MVP](../MVP.md) / [PRD](../PRD.md) | Scope and product outcomes; read before expansion. |
| [Original brief](../PROJECT_BRIEF.md) | Immutable historical blueprint; broader proposals are not automatically approved. |

The overview keeps its existing `docs/ARCHITECTURE.md` filename by the user's earlier request. Detailed concerns are split here to respect the architect skill's size limit; this map is navigation, not another specification. Nothing here authorizes code, migrations, provisioning or live calls.
