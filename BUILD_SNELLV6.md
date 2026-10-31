# Latest-stable Snell v6 custom build

This branch refreshes the existing kitdine fork; it is not a patch on the old alpha.50 binary.

## Audited baselines (2026-10-31)

- Latest stable sing-box: **v1.14.4**, `c005f35300ce76427dc285481ec5695f5b179cd2`.
- Latest sing-snell main, already selected by stable sing-box: `bc5a12ac736f235b2de2926ecd2791cc925e6b8c`.
- Custom source used by the build: `a6a9f78f8172f75e7c686accca662fe9b58ae37e`.
- Output version: **1.14.4-snellv6fix.2**, Linux amd64 and arm64.

The old custom branch differs from its upstream c43fbee baseline by exactly one commit, f2a0337: shaped-record buffer headroom fallback and its regression tests. That fix is still absent from current upstream. It is reapplied here while retaining newer rc1 packet chunk-state logic. Domain UDP first-packet routing (f8ab802) and IP-only UDP responses in sing-box (33e63af) are already upstream and are not reapplied.

## Build safety and verification

The dedicated Actions workflow pins both source commits, Go 1.26.10, and action revisions. It uses read-only repository permission, does not persist checkout credentials, and requires no secrets. It replaces the sing-snell module with the exact checked-out custom commit, asserts the replacement directory, runs race-enabled library regressions and sing-box route/Snell compilation checks, then builds with upstream stable's default feature tags. Artifacts include source/build provenance, module metadata, and SHA256 checksums. No release, deployment, config change, or automatic merge is performed.

The custom regressions cover shaped buffer fallback and v6 first-packet routing across all three modes, domain/IPv4/IPv6 and single/multi-user services. An amd64 Actions-only real-binary test starts an isolated server with synthetic credentials and hosts DNS on loopback. It checks 30 domain/literal-IP echo round trips across three v6 modes and payload sizes 1/136/512/1400/4096 bytes; no production servers or public DNS are contacted. The local development container could compile the binary but could not start it because netlink sockets are disabled; an escalation attempt failed during sandbox initialization, and no protected path was accessed. arm64 is cross-compiled, not executed.

Additional checks cover packet preservation and domain-to-IP one-way NAT replies. A Linux loopback test demonstrates why an unresolved destination in the underlying batch writer is unsafe; ordinary UDP writes reject it. This control test is not a claim that arbitrary per-packet multi-domain UDP routing is fixed.

Local/CI tests do not replace a controlled Surge + carrier IKE/call validation. Preserve the working always-real-ip exceptions until a separately authorized production rollout and client validation. Dedicated new v5/reused-session regressions are not included; existing upstream integration tests remain unchanged.

Future updates must move both immutable pins deliberately, rerun all checks, and bump the custom version. Do not silently build from a moving latest/main ref.
