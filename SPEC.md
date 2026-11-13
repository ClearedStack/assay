# Assay specification

> Technical specification for the deterministic Assay worker that executes five binary knockout gates, the 100-point rubric, and five tie-breakers using YAML/Go rules.

**Status: design.** This is a specification. It describes how Assay is meant to work, not something that exists yet.

The same text is published at [clearedstack.com/platform/assay/](https://clearedstack.com/platform/assay/). To change the specification, change it here and say why.

## Overview & Role

Assay (`v0.1.0-draft`) is the deterministic screening and scoring worker in ClearedStack v3.1, replacing the original Stages 4 (Screen) and 5 (Score). Implemented as an MIT-licensed Go service in the Assay repository, it evaluates `observed` candidates using a pure YAML/Go rule engine with zero AI models and zero token costs.

Given identical observed facts, Assay produces the exact same gate outcome, 100-point rubric score, preliminary recommendation, and tie-breaker ordering on any cluster.

## Runtime & Resource Budget

Assay runs in the `cs-workers` Kubernetes namespace under the **Standard (Workers)** priority class.

| Attribute | Value |
| --- | --- |
| **Worker ID / Repo** | Assay (`assay`) |
| **Namespace** | `cs-workers` |
| **Kind / Version** | `worker` · `v0.1.0-draft` |
| **RAM Budget** | `30 MiB` (always-on Go pod) |
| **Runtime AI Calls** | `0` (100% deterministic YAML/Go rule engine) |
| **Local Spool** | `/var/spool/cs` (buffers completed scoring payloads if Muster restarts) |
| **Build & Deploy** | Built with `ko` (no Docker) by Keelworks, signed with `cosign` in Lazarette, deployed by Shipkeeper via Server-Side Apply |

## Interfaces & State Transitions

Assay polls Muster in the `clearedstack` namespace over ServiceAccount-authenticated cluster-private HTTP (`CONTRACT_VERSION=v1`), draining `urgent` → `normal` → `backfill` lanes.

- **Inbound state:** `observed` (structured JSON facts produced by Sextant).
- **Evaluation stages:**
  1. **Five binary knockout gates:**
     - *Public source* — Rules, formulas, and thresholds must come from officially released public materials (statutes, FAR, DFARS, NIST SPs).
     - *Runs in the browser* — Must execute entirely client-side (HTML/CSS/JS/WASM) with no backend server or remote API.
     - *Private by design* — Must work offline, require no accounts, emit zero tracking, and enforce `connect-src 'self'`.
     - *One job* — Must perform a single focused task describable in one active verb-first sentence.
     - *Arithmetic, not advice* — Must compute numbers, structure data, or look up citations—never render legal advice.
  2. **100-point rubric:** Candidates clearing all five gates are rated `1`–`5` across seven weighted factors (composite `20`–`100`): Need for small businesses (`×4`, 20 pts), Gap (`×4`, 20 pts), Strength of public source (`×3`, 15 pts), Fits in a browser (`×3`, 15 pts), Small enough to read (`×2`, 10 pts), Can be checked (`×2`, 10 pts), and Low upkeep of reference data (`×2`, 10 pts).
  3. **Preliminary recommendation & split/merge flag:** Assigns `Build` (Score ≥ 75 and Gap > 1), `Link` (Score ≥ 75 and Gap = 1), `Park` (Score 55–74), or `Reject` (Score < 55). Flags **split/merge review** when Need is `4`–`5` and Small enough to read is `1`–`2`.
  4. **Five deterministic tie-breakers:** Roadmap phase fit → Gap severity → Low upkeep → Small code footprint → Community demand.
- **Outbound transitions:**
  - `screened-out` — Candidate failed one or more knockout gates (failure reason logged publicly).
  - `scored` — Candidate cleared all five gates and received its rubric breakdown and recommendation.
  - `needs_subtool` — Fail-closed pause (`E_NEEDS_SUBTOOL`) when an item's category lacks a matching rubric profile.

## Sub-Tools & Failure Modes

Domain-specific scoring thresholds and fixtures live in Assay under `tools/rubric-profiles/<category>/` (such as `pricing`, `proposal`, `cyber`, `market`, and `devsecops`).

| Sub-Tool | Responsibility |
| --- | --- |
| `rubric-profiles` | Category-specific YAML/Go rule sets and golden test fixtures defining deterministic gate checks and rubric factor mappings. |

### Sanitized Failure Modes

Assay fails closed on unknown fields or unsupported categories rather than falling back to defaults or heuristics:

| Error Code | Trigger | Platform Behavior |
| --- | --- | --- |
| `E_NEEDS_SUBTOOL` | Observed item carries a contracting category or unknown field not covered by `tools/rubric-profiles/<category>/`. | Pauses item in `needs_subtool`. Bellbuoy opens a deduplicated task in Assay on Plainforge; once a new profile and fixture deploy, Muster replays paused items automatically. |
| `E_PARSE` | Observed JSON payload fails contract schema validation or contains invalid types. | Halts item cleanly and reports a fingerprinted error via Bellbuoy. |
| `E_TIMEOUT` | Hub lease or completion call times out. | Completed score is retained in `/var/spool/cs` and retried on backoff. |

## Security & Clean-Room Boundaries

- **Zero raw observation access:** Assay operates strictly on sanitized structured JSON facts from `observed` records; it never reads raw vendor HTML, screenshots, or binary PDFs.
- **Public transparency:** All knockout gate outcomes, rubric weights, factor scores, and tie-breaker results are written through Muster to `core.*` and exposed read-only via `pub.*` views on `clearedstack.com` with no login wall.
- **Pod hardening & network policy:** Runs as an unprivileged non-root container with dropped capabilities, read-only root filesystem (writable only at `/var/spool/cs`), zero database credentials, and default-deny `NetworkPolicy` permitting only ServiceAccount HTTP to Muster.

## Related pages

- [How we decide what to build](https://clearedstack.com/platform/how-we-decide/)
- [An item’s life: fourteen states and three lanes](https://clearedstack.com/platform/item-lifecycle/)
- [The pipeline and its ten workers](https://clearedstack.com/platform/pipeline/)
- [Operating it: status views, failures and backups](https://clearedstack.com/platform/operations/)
