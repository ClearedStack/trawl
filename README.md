<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="brand/logo-dark.svg">
    <img src="brand/logo-light.svg" alt="Trawl" height="56">
  </picture>
</p>

<p align="center">
  <strong>Finds candidate tools and records each one exactly once.</strong><br>
  Part of <a href="https://clearedstack.com">ClearedStack</a>
</p>

---

> **Status: design.** One small piece is built: the first milestone, described under [What works today](#what-works-today). The worker itself is not built.

Trawl is stage 01 of the ClearedStack pipeline. ClearedStack is an open pipeline that finds ideas for government-contracting tools, screens and scores them, and builds each one from the public rule. Trawl is meant to be understood and used without the rest.

## What works today

One command. It reads addresses on standard input, one per line, and for each prints its normalized form, its SHA-256 hash and whether it has been seen before. What has been seen is kept in a plain file.

```
$ go build -o trawl ./cmd/trawl
$ printf '%s\n' \
    'HTTPS://Tools.Example.ORG/Calculators/index.html?utm_source=news&id=7#top' \
    'https://tools.example.org/Calculators/?id=7' \
    'http://10.1.2.3/admin' | ./trawl normalize -store seen.tsv
ok	https://tools.example.org/Calculators?id=7	3d1534ddd8f995002325faf3ec4880fcdd92976df2ac096d13511e27d0155ed5	new
ok	https://tools.example.org/Calculators?id=7	3d1534ddd8f995002325faf3ec4880fcdd92976df2ac096d13511e27d0155ed5	seen
E_PARSE	host_not_public
```

Each input line gives exactly one output line, tab-separated: `ok`, the canonical address, the hash and `new` or `seen`; or an error code and a fixed reason word. A refused line never stops the lines after it, and an error never repeats any part of the input. The exit status is 0 when every line was accepted, 1 when at least one was refused, and 2 when the command could not run.

The normalization rules, each with saved examples in [`internal/normalize/testdata/rules/`](internal/normalize/testdata/rules):

| Rule | What it does |
| --- | --- |
| `well-formed-input` | Trims surrounding whitespace. Refuses empty lines, lines over 4096 bytes, and whitespace or control characters inside an address. |
| `lowercase-scheme-host` | Lowercases the scheme and the hostname. Nothing else changes case. |
| `strip-tracking-params` | Removes `utm_*`, `ref`, `fbclid`, `gclid`, `phpsessid`, `jsessionid`, `sessionid`, `session_id` and `sid`, in any letter case. Other parameters keep their order and exact bytes. |
| `strip-fragment` | Removes `#` and everything after it. |
| `trailing-slash` | Removes trailing slashes from the path. The root path is always written `/`. |
| `default-index` | Removes a final `index.html`, `index.htm`, `index.php`, `index.asp`, `index.aspx`, `index.jsp`, `default.html`, `default.htm`, `default.asp` or `default.aspx`. |
| `public-scheme` | Accepts `http` and `https` only. |
| `public-host` | Accepts multi-label DNS names only. Refuses IP addresses, single-label names, names ending in a private-use label such as `.local` or `.internal`, and addresses carrying a username or password. Hosts with non-ASCII characters or a trailing dot are paused as `E_NEEDS_SUBTOOL`, because no rule covers them yet. |
| `leave-unlisted-alone` | Everything the specification does not list is left byte for byte: ports, dot segments, percent-encoding, `www.`, parameter order. `http` is not turned into `https`. |

Run the tests, which include every saved example:

```
go test ./...
```

### Not yet done

Everything else in [`SPEC.md`](SPEC.md):

- Minting `PA-XXXX` identifiers and the `discovered` to `recorded` transition.
- The Muster lease contract (`lease`, `heartbeat`, `complete`, `fail`, `needs_subtool`), priority lanes, and an in-memory stand-in for Muster. The plain file store sits behind a small interface (`seen.Store`) that Muster will later answer.
- Logging a repeat sighting against the existing record. Today a repeat is only reported as `seen`.
- The local spool at `/var/spool/cs`.
- The four sub-tools (`sam-gov`, `github-issues`, `web-sweep`, `site-form`), `tools/registry.yaml` and their matching rules.
- `E_TIMEOUT`. Nothing built so far touches the network.
- The anonymous submission shield, `robots.txt` handling, the `ko` image build and the pod settings.

The file store holds every hash in memory. Measured on 50,000 made-up addresses, a run that loads them all peaked at about 21 MiB, inside the 30 MiB budget but not far inside. It is a stand-in for the milestone, not the final design.

## In this repository

| File | What it is |
| --- | --- |
| [`SPEC.md`](SPEC.md) | The specification: what Trawl does, its interfaces, how it fails and how it is kept safe. |
| [`PROMPT.md`](PROMPT.md) | The prompt to start a build session with. It says what to build and the rules to build it under. |
| [`component.json`](component.json) | This component’s entry in the ClearedStack naming registry. |
| [`brand/`](brand) | The Trawl logo and mark, for light and dark backgrounds. |
| [`cmd/trawl/`](cmd/trawl) | The `trawl` command. |
| [`internal/normalize/`](internal/normalize) | The normalization rules and their saved examples. |
| [`internal/seen/`](internal/seen) | The record of what has been seen: a small interface and a plain-file store. |

## At a glance

| | |
| --- | --- |
| **Stage** | 01 of 10 in the pipeline |
| **Formerly** | `cs-01-intake` |
| **Namespace** | `cs-workers` |
| **Container image** | `lazarette.clearedstack.svc.cluster.local:5000/trawl` |
| **Memory budget** | 30 MiB |
| **Depends on** | [Muster](https://github.com/ClearedStack/muster) |
| **Depended on by** | Nothing else in the platform |
| **Rules for particular cases** | `sam-gov`, `github-issues`, `web-sweep`, `site-form` |

### Boundaries

- Leases work from Muster over cluster-private HTTP
- Reads public sources: SAM.gov notices, GitHub issues, scheduled site sweeps and the site's own form

## Names

`trawl` is this component’s identifier everywhere: the repository, the workload, the container image. The one place names are written down is the [naming registry](https://clearedstack.com/platform/registry.json), kept in [`clearedstack.com`](https://github.com/ClearedStack/clearedstack.com/blob/main/src/data/registry.json). `component.json` here is a copy of this component’s entry. If the two disagree, the registry is right.

## Ground rules

- **No AI when it runs.** The same input gives the same output on any machine.
- **Stop instead of guessing.** A case with no rule is paused and reported, never improvised.
- **Clean room.** Written from scratch. No code, text or design is copied from another project.
- **Public sources and made-up data only.** Nothing classified, controlled or taken from a real contract, in code, tests, issues or commit messages.

## License

[MIT](LICENSE). ClearedStack is an independent personal project. It is not affiliated with, endorsed by, or sponsored by the U.S. Department of Defense or any other government agency.
