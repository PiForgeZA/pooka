# Unit test audit: 2026-10-28

## Implementation follow-up

The baseline measurements and inventory below describe commit `d1a134a`.
The follow-up on `test/cleanup-unit-tests` adds 29 top-level tests, including table
cases. The suite now has 150 host-free top-level tests. All pass with `-race`.
`go vet ./...` and `git diff --check` also pass.

| Minimum group | Added evidence |
|---|---|
| Destructive guards | Restore wrong name, EOF, typed name and force; eight LXC operations reject an existing untagged container |
| Failure ordering | Failure at every restore stage; destroy stop/purge errors; checkpoint create/delete success and failure |
| Proxy activity | Running and stopped environments report the correct name; unauthorized requests do not wake; real proxy activity delays daemon idle stop |
| Body bounds | File write accepts exactly 16 MiB and preserves contents; larger known/unknown-length bodies fail before a write; exec stdin stops at 10 MiB |
| Idle safety | Failed connection probes cannot stop; cancellation ends the loop; channel barriers reproduce request/session activity during the probe |
| Exec contract | Resize reaches the backend; kill is environment-scoped; close before exit returns an error and restores the terminal; raw HTTP chunks retain independent frame boundaries |
| Authentication and config | Protected route families reject bad/missing tokens; real client construction uses configured CA and token; invalid or missing explicit CA cannot connect; malformed config fails |
| Update failures | Download/checksum/archive/cancellation failures preserve the executable; a directory at the replacement destination exercises rename error cleanup; failed install or version verification returns failure |
| Transport and runtime | LXC transfer errors and cleanup; recording SSH/scp commands and cancellation; generated certificate, proxy API routing and graceful HTTP-server shutdown with a fake backend |

All three audit mutations now fail their new regression tests. No source mutation
remains in the working tree.

The new tests exposed two production defects. Both have small fixes and specification
updates:

- Idle stop used the activity state from before the TCP probe. A request or exec
  session arriving during that probe did not prevent the stop. The daemon now checks
  activity and sessions again after the probe.
- Daemon update reported success when the installed version was unavailable or still
  differed from the requested release. It now returns an error in those cases.
  A local `--pookad` binary has no known release tag, so only readability is required.

These tests do not prove that activity arriving after the final idle check can cancel
an already-started backend stop. They also do not execute `main`'s signal handling,
systemd installation, actual SSH, or Proxmox commands. The runtime smoke composes the
real daemon, proxy and certificate loader around a fake backend. Live checks remain
as described below. The rename-failure case uses a directory destination; it does not
simulate storage failure while replacing a valid executable.

## Baseline recommendation

Keep the current host-free suite. Add the nine contract groups in the minimum-suite
matrix below. Start with destructive operations, proxy activity, upload limits, and
idle-stop decisions. These have the greatest risk of data loss, interruption, or
resource exhaustion.

Do not use a statement-coverage target as the acceptance condition. Three deliberate
regressions passed their complete package suites. The missing assertions matter more
than the aggregate percentage.

The original audit proposed tests. The implementation follow-up above records the
subsequent tests and fixes.

## Scope and evidence

- Baseline: `d1a134a` (`feat: setup joins a host that already runs pookad (#14)`).
- Environment: Linux development container; Go `go1.27.3 linux/amd64`.
- Read all 18 Go test files, their production paths, the current specification, and CI.
- Found 122 top-level test functions. One requires the `integration` build tag.
- Ran all 121 host-free top-level tests with the race detector. All passed.
  The JSON report has 125 passing test events because four named subtests also appear.
- Ran cross-package coverage separately. All host-free tests passed.
- Ran four targeted mutation probes through Go build overlays. Tracked source files
  stayed unchanged. Each probe used `-count=1` to disable cached results.
- Did not run Proxmox integration tests, an installed daemon, or the official Sprite CLI.
- Found no Go fuzz tests or benchmarks. Unnamed table cases are not separate test counts.

“Host-free” includes more than pure unit tests. The suite uses real local HTTP and
WebSocket servers, subprocesses, PTYs, and files. Those tests give useful evidence at
module boundaries. Proxmox commands usually use fakes.

Commands used for the two coverage runs:

```sh
GOCACHE=$PWD/dist/go-cache go test -race -coverprofile=dist/unit-coverage.out -json ./... > dist/unit-test-results.jsonl
GOCACHE=$PWD/dist/go-cache go test -coverpkg=./... -coverprofile=dist/all-package-coverage.out -json ./... > dist/all-package-test-results.jsonl
```

The sandbox blocked local HTTP listeners on the first attempt. The runs above used
permission to open local listeners. A preliminary cross-package run also discovered
probe `.go` files in `dist/`. The probe files now use `.txt` extensions. The subsequent
cross-package run passed. These were audit setup failures, not repository test failures.

Raw profiles, JSON logs, and overlay definitions are local, ignored files in `dist/`.
They are not required to read this report.

## Package coverage matrix

Counts refer to top-level host-free tests declared in each package. Coverage measures
executed statements, not assertions, branch combinations, or conformance to the spec.

| Package | Tests | Default coverage | Cross-package coverage | Assessment |
|---|---:|---:|---:|---|
| `internal/backend/httpapi` | 7 | 78.2% | 78.2% | Strong client/server round trips; weak protocol failure cases |
| `internal/backend/lxc` | 23 | 77.4% | 77.4% | Lifecycle command sequences; several operations never execute |
| `internal/cli` | 41 | 76.1% | 76.1% | Broad command tests; actual backend construction bypassed |
| `internal/config` | 1 | 37.0% | 66.7% | CLI tests exercise saving; validation and active environment gaps remain |
| `internal/daemon` | 28 | 81.4% | 82.1% | Routes and idle policy; missing resource bounds and interleavings |
| `internal/host` | 5 | 45.1% | 61.7% | Real local subprocess tests; actual SSH adapter untested |
| `internal/pki` | 6 | 84.1% | 84.1% | Trust, renewal, fallback, concurrency; runtime TLS wiring absent |
| `internal/proxy` | 10 | 90.8% | 90.8% | Routing, auth, wake, cache; activity callback not asserted |
| `internal/backend` | 0 | 0.0% | 100.0% | Validators run through callers |
| `internal/backend/backendtest` | 0 | 0.0% | 88.2% | Test support code; coverage is not product assurance |
| `cmd/pooka` | 0 | 0.0% | 0.0% | Entry point untested |
| `cmd/pookad` | 0 | 0.0% | 0.0% | Startup, configuration, TLS selection, shutdown untested |
| **Total** | **121** | **69.3%** | **75.9%** | Includes test-support statements |

Default Go coverage does not credit a package for execution from another package's
tests. For example, the backend validators are exercised despite their default 0%.
Cross-package coverage corrects this measurement. It does not make assertions stronger.
Neither total excludes the compiled fake backend and host helper code.

## Behavior and gap matrix

“Covered” below means that the named behavior has assertions. It does not mean that
every failure branch is tested. Representative test names link to their source files.

| Contract | What is tested | What is missing or weak | Impact |
|---|---|---|---|
| Destructive CLI confirmation | [TestDestroyNeedsTypedNameOrForce](../internal/cli/cli_test.go): wrong name, correct name, force. Restore argument forms with force. | Restore without force, wrong input, EOF, and zero backend calls on refusal. Removing restore confirmation passes. | Accidental data loss |
| Managed-container boundary | [TestListFiltersByTag / TestGetNotFound](../internal/backend/lxc/lxc_test.go): exclude unrelated containers; reject unknown names. | Apply the boundary to each mutating operation. Assert that an existing untagged container causes no mutation. List filtering alone does not prove each caller uses it. | Modification of unrelated containers |
| Create lifecycle | [LXC tests](../internal/backend/lxc/lxc_test.go): clone/set/resize/start/readiness, options, duplicate and invalid names, occupied VMIDs, missing template, readiness timeout. | VMID exhaustion; failures at each stage; cancellation; concurrent allocation. Partial-create cleanup needs an explicit policy before a test requires it. | Partial resources or conflicting allocation |
| Stop, start, destroy, restore | [LXC tests](../internal/backend/lxc/lxc_test.go): graceful-stop flags, start idempotence, stop before purge, LVM-thin MMP clearing, ZFS alternative. | Inject failure at stop, rollback, storage lookup, MMP clear, and purge. Assert no later unsafe command and useful error context. | Data loss or unsafe recovery |
| Checkpoints | [HTTP client](../internal/backend/httpapi/client_test.go) and [daemon](../internal/daemon/server_test.go): create/list/restore/delete round trips; latest by timestamp. LXC lists snapshots and omits current state. | LXC `Checkpoint` and `DeleteCheckpoint` have 0% coverage. Stream failure/truncation and empty latest set need checks. Fakes do not prove disk restoration. | Failed recovery |
| Exec lifetime and exit status | [Local tests](../internal/host/local_test.go), [supervisor test](../internal/backend/lxc/exec_test.go), [HTTP client](../internal/backend/httpapi/client_test.go): cancellation, inherited ignored HUP, stdin EOF independence, exit codes, stdout/stderr, root extension, stopped environment refusal. | Runtime SSH adapter and non-Linux parent-death behavior are not covered here. Backpressure, unexpected WebSocket close, positive session-kill behavior and environment isolation need tests. | Orphan processes or hidden command failure |
| TTY and resize | Local PTY test observes `stty`; HTTP TTY test checks raw-mode entry and restoration. WebSocket tests send resize messages. | Sending a resize is not an assertion that the backend receives it. Check terminal restoration on transport failure. Real terminal adapter is not exercised. | Broken interactive sessions |
| File transfer and limits | [TestPushPull](../internal/backend/httpapi/client_test.go): contents, mode, ownership command, missing file and directory rejection. [Daemon tests](../internal/daemon/server_test.go): read/write/list/chown parsing. | LXC `Push` and `Pull` have 0% coverage. Daemon 16 MiB write boundary is untested. Shell-script fakes do not execute permission behavior. Exec request-body limit also lacks boundary tests. | Resource exhaustion or wrong file state |
| API authentication | [TestAuthRequired / TestPookaExtensions](../internal/daemon/server_test.go): absent/wrong token rejected; health open; sample extension protected. Log test excludes query secrets. | Route-family table, including WebSocket and filesystem mutations, with zero backend calls on rejection. Pair real transport construction with TLS trust. | Unauthorized access |
| API compatibility and errors | Client tests run against the real daemon handler with a fake backend. Cover lifecycle, settings, old route hint, unreachable server and missing resource. | Both sides can agree on an incorrect protocol. POST exec tests flatten the HTTP body. Add independent framing fixtures, malformed/truncated streams and close-before-exit cases. | Sprite client incompatibility |
| Activity classification | [TestActivityFromRequests / TestActivityFromHTTPExec](../internal/daemon/phase3_test.go): active and excluded route table; filesystem reads now included. | Proxy harness records activity only in a cleanup log. No assertion protects the callback or its connection to idle policy. | Active environment stops |
| Idle decisions | [Idle tests](../internal/daemon/phase3_test.go): elapsed time, activity reset, established TCP, active exec, per-environment override, disabled policy, creation floor. | Failed connection probe, list/stop errors, activity or exec arriving during a check, long requests, and `RunIdle` cancellation. Race detector cannot prove correct decision ordering. | Active environment stops or shutdown hangs |
| URL routing and authentication | [Proxy tests](../internal/proxy/proxy_test.go): host matching, unknown names, upstream forwarding, bearer/query token, public access, signed host-bound cookie and flags. | Assert no wake on unauthorized requests. Real daemon API routing, WebSocket upgrade and streaming need boundary checks. Auth revocation through cached metadata needs a stated timing expectation. | Access control or routing regression |
| Wake and caching | Proxy tests cover cold start, five concurrent callers, no listener, cache expiry, negative cache and external-start race. | Fake start returns immediately. Use a barrier to force callers to wait on the same start. Cancellation test starts with an already-canceled context; it does not cover cancellation during a blocked start. | Duplicate starts or hung requests |
| CLI arguments and defaults | [CLI tests](../internal/cli/cli_test.go) and [phase 3 tests](../internal/cli/phase3_test.go): environment selection, exec forms, exit status, resource overrides, URL, idle settings, URL column. | `NewApp` and `openBackend` have 0% coverage. Transport-selection tests inject `Open`; they do not verify the actual authenticated HTTP/TLS backend. | Tested commands fail in real use |
| Setup and join | [16 setup tests](../internal/cli/setup_test.go): fresh install, join, host defaults, flag precedence, version mismatch/reinstall, domain propagation, CA/token/config files, template/storage refusal, SSH config idempotence. | Host copy/install errors, partial local writes and failed health checks need systematic checks. Mocked command strings do not test the installer. | Broken installation or credentials |
| Update and version | [10 update tests](../internal/cli/update_test.go): real local HTTP archive/checksum flow, replace/no-op/pin, bad checksum preserves binary, unwritable directory, remote architecture and local daemon binary paths, version output. | Missing checksum, corrupt/truncated archive, absent executable, HTTP failure, cancellation, rename failure, remote install failure and failed post-update verification. Assert old binary and temporary-file cleanup on every local failure. | Unusable CLI or false update success |
| Configuration | [Direct config test](../internal/config/config_test.go): token-file loading and environment overrides. Setup tests exercise saving. | Malformed/invalid config, active environment file, explicit missing/invalid CA, precedence edge cases and actual constructor use. | Wrong target or failed authentication |
| Certificates | [PKI tests](../internal/pki/pki_test.go): trust, wildcard limits, reuse, changed domain, renewal, empty domain. [Reloader tests](../internal/pki/reloader_test.go): interval, last-good fallback, initial error, concurrent load. | Key permissions, corrupt files, custom mismatched key pair and real TLS handshake through runtime wiring. | Credential exposure or unavailable daemon |
| Host adapter | [Host tests](../internal/host/host_test.go): quoting. Local tests execute subprocesses and PTYs. | `SSH.Run`, `scp`, SSH option construction and local copy operations have 0% coverage. Test quoting through a recording executable without a real host. | Wrong remote command, copy or cancellation |
| Process and scripts | CI compiles packages; cross-builds CLI; shellchecks installer/template scripts. | No daemon startup/shutdown smoke, actual installer behavior test, or template toolchain verification. Shellcheck proves neither provisioning nor recovery. | Release/install failure |

The previous filesystem-read activity gap and setup-domain propagation gap now have
regression tests. This audit does not treat those old findings as current omissions.

## Demonstrated assertion gaps

Each overlay changed one production behavior while leaving the original source intact.
The full relevant package suite ran for the three gap probes.

| Deliberate change | Test run | Result | Conclusion |
|---|---|---|---|
| Remove restore's call to `confirm` | `go test -count=1 -overlay=… ./internal/cli` | Pass | Tests do not protect restore confirmation |
| Remove proxy's call to `Touch(name)` | `go test -count=1 -overlay=… ./internal/proxy` | Pass | Tests do not protect proxy activity reporting |
| Read write-body without `http.MaxBytesReader` | `go test -count=1 -overlay=… ./internal/daemon` | Pass | Tests do not protect the upload limit |
| Control: remove destroy's call to `confirm` | Target `TestDestroyNeedsTypedNameOrForce` | Fail: wrong name reached destroy | Existing destroy assertion detects its intended regression |

These are test-sensitivity checks, not production bug reproductions. The current
implementation contains all three protections. Four selected probes cannot establish
a repository-wide mutation score.

## Minimum suite for the important contracts

Keep all 121 existing tests. They cover useful regressions and are small enough for
normal CI. Removing them would save little and discard evidence. Here, “minimum” means
the smallest practical set of additional contract groups to address the major risks.
It is a risk-based recommendation, not a proof of complete coverage.

Implement each row as a table-driven test or a small related group. Do not create one
large scenario whose first failure hides all later assertions.

| Order | Contract group | Minimum additional cases and acceptance assertions | Suggested location |
|---:|---|---|---|
| 1 | Destructive operation guards | Restore: wrong name and EOF cause zero calls; correct name and force proceed once. Parameterize existing destroy checks with restore where useful. LXC mutators reject an existing untagged target with zero mutation commands. | CLI and LXC tests |
| 2 | Destructive failure ordering | Inject stop, rollback, storage/MMP and purge errors. Check returned error and exact allowed call sequence. Never run a later destructive/restart step after a failed prerequisite. Cover checkpoint create/delete success and backend failure. | LXC tests with strict host fake |
| 3 | Proxy activity contract | Valid request for running and stopped environments records the correct name. Connect proxy callback to a real daemon's fake-clock idle policy in one host-free test; a request prevents the next idle stop. Define failed-upstream activity expectations from the policy. | Proxy plus daemon boundary test |
| 4 | Body bounds | File write at 16 MiB succeeds; one byte more returns 413 and performs no write. Repeat above-limit case without Content-Length. Verify the exec request-body cap separately. | Daemon tests with backend call recorder |
| 5 | Idle safety and cancellation | Connection probe error/nonzero exit must not authorize stop. Use a barrier to introduce activity or an exec session while the probe is blocked. Define the decision boundary and assert no stale stop. Cancel the idle loop and assert completion. | Daemon tests; fake clock and channels |
| 6 | Exec protocol and session cleanup | Assert backend receives resize. Disconnect cancels blocked exec and removes its session. Explicit kill affects only the selected environment/session. Unexpected close before exit returns an error. Raw terminal restoration also occurs on failure. Use an independent POST framing fixture. | Daemon and HTTP client tests |
| 7 | Authentication and real client construction | Table of protected route families rejects wrong/missing credentials before backend calls. Use actual `NewApp`/backend construction against a local TLS server: configured CA works; wrong CA fails; token arrives. Reject malformed config and explicitly invalid CA input. | CLI, config and daemon tests |
| 8 | Update failure preserves installation | Missing/bad checksum, corrupt/missing archive member, failed download and failed replacement preserve old executable and remove temporary files. Failed remote install or version check cannot report success. | Existing update test harness |
| 9 | Filesystem and transport boundaries | LXC push/pull execute the correct ordered commands, preserve quoting/mode/owner semantics and propagate failures with cleanup. Recording SSH/scp executable checks argv, cancellation and copy failure. Add one daemon startup/TLS/shutdown smoke with a fake backend. | LXC, host and daemon runtime tests |

Rows 1–5 are the first delivery. Rows 6–9 complete the proposed minimum for a tool that
runs remote commands and destructive operations. Keep existing URL authorization,
certificate renewal, setup/join and lifecycle happy-path tests as part of that minimum.

Some cases can share a harness, but should not share mutable global state across
parallel tests. Where a test exposes an unresolved behavior, record that decision
before changing production behavior. In particular, do not assume partial-create
cleanup, cache revocation timing, or an idle race policy without specifying it.

## Test design improvements

1. Use `host.Fake.Strict` for safety tests. Unmatched fake commands currently succeed
   by default. A successful return can hide a missing fixture or an unexpected call.
2. Check both the required effect and forbidden effects. A call-order subsequence
   does not prove that no extra purge, start, or copy occurred.
3. Add narrow fault injection to fakes for copy errors, backend errors, blocked exec,
   and start completion. Current fake copy methods report success without copying.
4. Replace scheduling sleeps in new concurrency tests with readiness channels and
   barriers. Keep timeouts only as failure bounds. Five goroutines do not guarantee
   that all five encounter an in-progress start.
5. Keep real local HTTP, WebSocket, PTY and filesystem tests. They catch defects that
   argument-only fakes miss. Close readers and resize channels so test helpers end.
6. Add independent protocol fixtures where both client and server share this code.
   Their agreement does not prove compatibility with an external client.
7. Use package coverage to find unexecuted paths. Use mutation probes and effect
   assertions to assess the tests that already execute those paths.

Avoid adding tests solely for trivial getters, generated data, or already-tested
standard-library crypto. Prioritize the user-visible guarantees above.

## What still requires integration checks

The host-free suite cannot prove that Proxmox accepts command syntax, that rollback
restores disk contents, that MMP clearing works, or that real SSH disconnect kills the
remote process tree.

The existing tagged [TestExecDisconnectIntegration](../internal/backend/lxc/exec_integration_test.go)
checks real exec disconnection across daemon/SSH, TTY/non-TTY and user/root combinations.
It also checks job control, stdin and exit status. Optional official Sprite CLI checks
need their configured binary. This test is not part of ordinary CI and was not run here.

Use these three integration groups before a release or after changes in their paths:

| Group | Minimum proof |
|---|---|
| Existing exec matrix | Real client death leaves no remote session/process; stdin, exit status and terminal signals work |
| Lifecycle and filesystem | Create a scratch environment, write data, checkpoint, modify data, restore, verify old contents, stop/start, push/pull, then destroy |
| Installed service boundary | On disposable infrastructure, verify setup/join, TLS trust, authenticated API, proxy wake/activity/idle, update failure handling and service restart |

Create scratch containers with `pooka`. Use `local-lvm`, `pooka` and `pooka-test` tags,
and guaranteed cleanup. Never use unrelated containers. Host changes still require
the authorization required by AGENTS.md. Unit-test changes do not require these live
checks unless they change the corresponding integration behavior.

## CI assessment

[Current CI](../.github/workflows/ci.yml) runs formatting, `go vet`, `go test -race -cover`,
shellcheck and GoReleaser configuration validation. It cross-builds the CLI for Linux
and macOS on amd64 and arm64. It does not execute tagged integration tests or macOS
runtime tests. The daemon compiles during native tests, but has no startup test.

Keep the current PR checks. Add the minimum contract groups to the same host-free run.
Publish a cross-package coverage artifact for diagnosis; do not gate on 100% coverage.
Run the live groups on an explicit integration/release workflow with isolated resources.
A race-detector pass is evidence against exercised data races. It is not proof against
incorrect idle/wake decision ordering or process leaks.

## Complete test inventory

The inventory below lists top-level test functions. Tables inside those functions add
cases. The one integration function is excluded from the 121-test host-free count.

### [internal/backend/httpapi/client_test.go](../internal/backend/httpapi/client_test.go)

- `TestListGetAndErrors` (line 32)
- `TestLifecycle` (line 68)
- `TestCheckpoints` (line 102)
- `TestExec` (line 140)
- `TestExecTTY` (line 187)
- `TestPushPull` (line 206)

### [internal/backend/httpapi/settings_test.go](../internal/backend/httpapi/settings_test.go)

- `TestSettingsRoundTrip` (line 17)

### [internal/backend/lxc/exec_integration_test.go](../internal/backend/lxc/exec_integration_test.go) — integration build tag

- `TestExecDisconnectIntegration` (line 26)

### [internal/backend/lxc/exec_test.go](../internal/backend/lxc/exec_test.go)

- `TestExecSupervisorEndsSessionAfterParentDeath` (line 20)

### [internal/backend/lxc/lxc_test.go](../internal/backend/lxc/lxc_test.go)

- `TestListFiltersByTag` (line 48)
- `TestGetNotFound` (line 71)
- `TestCreateSequence` (line 78)
- `TestCreateSkipsVMIDsWithConfigs` (line 103)
- `TestCreateRefusesDuplicateAndBadName` (line 117)
- `TestCreateMissingTemplate` (line 129)
- `TestCreateReadyTimeout` (line 138)
- `TestExecRunsAsPookaUser` (line 148)
- `TestExecRefusesStopped` (line 165)
- `TestRestoreClearsMMPOnLVMThin` (line 172)
- `TestRestoreSkipsMMPOnZFS` (line 186)
- `TestStopUsesShutdownWithForce` (line 203)
- `TestStartActsOnLiveStatus` (line 213)
- `TestDestroyStopsThenPurges` (line 230)
- `TestListCheckpointsSkipsCurrentAndSortsByTime` (line 238)
- `TestPctErrorCarriesStderr` (line 251)
- `TestParsePctList` (line 260)
- `TestParseRootfs` (line 267)
- `TestExecTTYKeepsSession` (line 288)
- `TestListReadsSettingsFromDescription` (line 317)
- `TestUpdateMergesIntoDescription` (line 336)
- `TestCreateWritesIdleTimeout` (line 373)

### [internal/cli/cli_test.go](../internal/cli/cli_test.go)

- `TestExecFormsAndExitCode` (line 68)
- `TestExecWithoutEnvFails` (line 92)
- `TestDestroyNeedsTypedNameOrForce` (line 102)
- `TestRestoreArgs` (line 119)
- `TestLsMarksActive` (line 132)
- `TestCreatePrintsSummary` (line 144)
- `TestTransportSelection` (line 155)
- `TestCreateOptionsPerTransport` (line 188)
- `TestLsShowsURLColumnOnlyWhenOneExists` (line 205)

### [internal/cli/phase3_test.go](../internal/cli/phase3_test.go)

- `TestURLCommand` (line 44)
- `TestIdleCommand` (line 88)
- `TestCreateIdleTimeoutFlag` (line 130)

### [internal/cli/setup_test.go](../internal/cli/setup_test.go)

- `TestSetupSavesCAAndDefaultsToHTTPSURL` (line 41)
- `TestSetupAddsDomainToExistingPookadYAML` (line 66)
- `TestSetupFollowsTheInstalledDomain` (line 88)
- `TestSslipDomain` (line 110)
- `TestSetupYesBuildsTemplateAndInstallsDaemon` (line 139)
- `TestSetupInteractiveAnswers` (line 206)
- `TestSetupRefusesNonTemplateVMID` (line 227)
- `TestSetupNeedsThinStorage` (line 235)
- `TestSetupPushesLocalPookad` (line 247)
- `TestThinStorages` (line 266)
- `TestReleaseTag` (line 273)
- `TestSetupJoinsAHostThatRunsPookad` (line 297)
- `TestSetupFlagsBeatTheHostDefaults` (line 323)
- `TestSetupReinstallsAnotherVersion` (line 335)
- `TestSetupReinstallFlag` (line 349)
- `TestTemplateStorageBridge` (line 362)

### [internal/cli/ssh_test.go](../internal/cli/ssh_test.go)

- `TestIPCommand` (line 63)
- `TestSSHProxyForwardsAndWakes` (line 75)
- `TestSSHConfigPrintAndInstall` (line 104)

### [internal/cli/update_test.go](../internal/cli/update_test.go)

- `TestUpdateCLIReplacesTheBinary` (line 80)
- `TestUpdateCLISkipsWhenAlreadyAtTheRelease` (line 105)
- `TestUpdateCLIPinsAVersion` (line 122)
- `TestUpdateCLIRefusesABadChecksum` (line 133)
- `TestUpdateCLIRefusesAnUnwritableBinary` (line 147)
- `TestUpdateDaemonFetchesAndInstalls` (line 166)
- `TestUpdateDaemonPushesALocalBinary` (line 201)
- `TestUpdateBothByDefaultWhenAHostIsConfigured` (line 222)
- `TestVersionShowsCLIAndDaemon` (line 239)
- `TestVersionOverSSH` (line 258)

### [internal/config/config_test.go](../internal/config/config_test.go)

- `TestLoadDaemonSettingsAndEnv` (line 10)

### [internal/daemon/phase3_test.go](../internal/daemon/phase3_test.go)

- `TestSpriteURLWithDomain` (line 18)
- `TestUpdateSpriteStoresURLAuth` (line 39)
- `TestSettingsExtension` (line 59)
- `TestIdleStopsAfterTimeout` (line 118)
- `TestIdleActivityResetsClock` (line 136)
- `TestIdleKeepsEnvWithConnections` (line 150)
- `TestIdleKeepsEnvWithExecSession` (line 170)
- `TestIdlePerEnvironmentTimeout` (line 187)
- `TestActivityFromRequests` (line 205)
- `TestActivityFromHTTPExec` (line 248)
- `TestIdleCreationCountsAsActivity` (line 263)
- `TestIdleDisabledByDefault` (line 278)

### [internal/daemon/server_test.go](../internal/daemon/server_test.go)

- `TestAuthRequired` (line 45)
- `TestListAndGetSprites` (line 58)
- `TestCreateAndDelete` (line 81)
- `TestCheckpointStreamAndList` (line 106)
- `TestExecPost` (line 141)
- `TestExecAttachNotSupported` (line 162)
- `TestExecParamsSize` (line 179)
- `TestExecWebSocket` (line 192)
- `TestFsEndpoints` (line 247)
- `TestParseFind` (line 272)
- `TestNotImplemented` (line 283)
- `TestPookaExtensions` (line 291)
- `TestFsChown` (line 344)
- `TestExecRootOnlyViaExtension` (line 363)
- `TestExecWebSocketDisconnectAfterStdinEOF` (line 378)
- `TestLogRequestsOmitsQuery` (line 411)

### [internal/host/host_test.go](../internal/host/host_test.go)

- `TestShellQuote` (line 5)

### [internal/host/local_test.go](../internal/host/local_test.go)

- `TestLocalCancelEndsChild` (line 17)
- `TestLocalRunPipes` (line 75)
- `TestLocalRunPTY` (line 83)
- `TestLocalRunDoesNotWaitForStdinEOF` (line 135)

### [internal/pki/pki_test.go](../internal/pki/pki_test.go)

- `TestEnsureCertCreatesAndReuses` (line 11)
- `TestEnsureCertNeedsDomain` (line 65)

### [internal/pki/reloader_test.go](../internal/pki/reloader_test.go)

- `TestReloaderCallsLoadAtMostOncePerInterval` (line 11)
- `TestReloaderKeepsTheOldPairWhenLoadFails` (line 44)
- `TestReloaderFailsWhenNothingWasEverLoaded` (line 80)
- `TestReloaderIsSafeForConcurrentUse` (line 87)

### [internal/proxy/proxy_test.go](../internal/proxy/proxy_test.go)

- `TestRoutesByFirstLabel` (line 83)
- `TestSplitHost` (line 111)
- `TestURLAuth` (line 129)
- `TestWakeOnRequest` (line 174)
- `TestConcurrentWakesShareOneStart` (line 191)
- `TestNothingListening` (line 215)
- `TestLookupCache` (line 235)
- `TestUnknownNameIsCachedAndRunningWithoutIPFailsFast` (line 250)
- `TestWakeRaceWithExternalStart` (line 275)
- `TestContextCancelDuringWake` (line 292)
