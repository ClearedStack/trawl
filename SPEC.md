# Trawl specification

> First deterministic worker in cs-workers that discovers public candidates, normalizes URLs, deduplicates via SHA-256, and assigns persistent PA-XXXX IDs with zero runtime AI.

**Status: design.** This is a specification. It describes how Trawl is meant to work, not something that exists yet.

The same text is published at [clearedstack.com/platform/trawl/](https://clearedstack.com/platform/trawl/). To change the specification, change it here and say why.

## Overview & Role

Trawl (`v0.1.0-draft`, owned by the Trawl repository) is the first of ten **100% deterministic** Go workers in the `cs-workers` namespace. It replaces the conceptual Stages 1 and 2 (Discover + Record) from earlier ClearedStack designs.

With **zero AI at runtime**, Trawl ingests candidate URLs from public government feeds, GitHub issues, scheduled web sweeps, and anonymous Portal submissions; normalizes every address through repeatable Go rules; deduplicates against existing records using **SHA-256**; and mints persistent `PA-XXXX` tracking identifiers before handing items to Sextant.

## Runtime & Resource Budget

| Property | Value |
| --- | --- |
| **Worker / Repo** | Trawl (`trawl`) |
| **Namespace** | `cs-workers` (`worker`) |
| **Version** | `v0.1.0-draft` |
| **RAM Budget (Requested)** | `30 MiB` |
| **Priority Class** | Standard (Workers) |
| **Local Spool** | `/var/spool/cs` (survives Muster restarts) |
| **Sub-Tools** | `sam-gov`, `github-issues`, `web-sweep`, `site-form` |

## Interfaces & Data Contracts

- **Queue Lease Contract (Muster, `CONTRACT_VERSION=v1`):** Pulls work via `lease`, maintains progress with `heartbeat`, and reports outcomes via `complete`, `fail`, or `needs_subtool` over cluster-private HTTP using its Kubernetes `ServiceAccount` identity. Workers always drain priority lanes in order: `urgent` -> `normal` -> `backfill`.
- **Deterministic URL Normalization:**
  - Lowercases scheme (`https`) and hostname.
  - Strips tracking and session query parameters (`utm_*`, `ref`, `fbclid`, `gclid`, session IDs) and `#` fragments.
  - Removes redundant trailing slashes and default index filenames.
  - Enforces public-only scheme and host validation.
- **SHA-256 Deduplication & State Transitions:** Computes the SHA-256 digest of the canonical URL and checks `core.*` state via Muster:
  - **Existing hash:** Logs the sighting or mirror link against the existing `PA-XXXX` record without creating a duplicate.
  - **New hash:** Mints a persistent `PA-XXXX` ID (transitioning `discovered` -> `recorded`) and queues the item for Sextant.
- **Local Spool (`/var/spool/cs`):** Stages completed intake payloads locally before acknowledging to Muster; if the hub is restarting, spooled results are flushed in order on recovery.

## Sub-Tools & Failure Modes

Each source adapter lives under `tools/<case>/` in the Trawl repository (registered in `tools/registry.yaml` with `match.yaml`, deterministic Go logic, and golden test fixtures):

| Sub-Tool (`tools/<case>/`) | Source | Deterministic Responsibility |
| --- | --- | --- |
| `sam-gov` | SAM.gov public notices | Extracts public tool links, agency calculators, and attachment URLs from SAM.gov feeds. |
| `github-issues` | GitHub public mirror | Parses structured tool and paperwork suggestions opened on the public repository. |
| `web-sweep` | Public `.gov`, `.mil`, APEX, and vendor sites | Runs scheduled, `robots.txt`-respecting sweeps across public contracting resources. |
| `site-form` | `clearedstack.com` Portal | Validates and normalizes direct candidate submissions and `✎` intake proposals. |

| Failure Mode | Trigger | System Behavior & Recovery |
| --- | --- | --- |
| **Unknown source format (`E_NEEDS_SUBTOOL`)** | Input matches none of the registered `tools/<case>/` rules | **Fails closed:** pauses the item in `needs_subtool`; Bellbuoy opens a deduplicated task in Trawl. Once a sub-tool + golden fixture ship via Keelworks and Shipkeeper, Muster replays all paused items. |
| **Upstream timeout (`E_TIMEOUT`)** | External `.gov` feed or target URL times out | Item remains safe in the queue for deterministic retry without losing state. |
| **Malformed payload (`E_PARSE`)** | Corrupted feed entry or invalid submission schema | Worker halts that specific item cleanly and emits a sanitized `E_PARSE` fingerprint. |
| **Hub unavailable** | Muster restarts mid-batch | Completed intake outputs are preserved in `/var/spool/cs` and flushed automatically when connectivity returns. |

## Security & Clean-Room Boundaries

- **Zero direct database access:** Trawl holds no Postgres credentials and cannot reach Logbook, Plainforge, or Lazarette under default-deny `NetworkPolicy` rules; all state mutations pass through Muster.
- **5-layer anonymous submission shield (`site-form` & Portal `✎`):** Enforces ~1s browser SHA-256 Proof-of-Work, honeypot field, daily-salted IP hash rate limiting (never storing raw IPs), strict 4 KB schema validation against `ownership_map.json` with HTML/script stripping, and owner approval in Countersign before any edit touches Plainforge.
- **Passive, public-only boundary:** Never creates third-party accounts, requests sales demos, bypasses paywalls or CAPTCHAs, ignores `robots.txt`, or accepts Controlled Unclassified Information (CUI) or proprietary bid figures.
- **Public hosts only:** Address normalization refuses loopback, private, IP-literal and single-label hosts; the one test-only exception is the single local origin named by `TRAWL_LOCAL_TEST_ORIGIN`, which `deploy/` never sets (see the README).
- **Unprivileged pod:** Runs as non-root with dropped Linux capabilities and a read-only root filesystem (writable only at `/var/spool/cs`).

## Related pages

- [The pipeline and its ten workers](https://clearedstack.com/platform/pipeline/)
- [Cluster architecture](https://clearedstack.com/platform/cluster-architecture/)
- [Public edits and what stays private](https://clearedstack.com/platform/public-edits/)
