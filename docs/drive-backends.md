# Comparing Drive API backends

`GIT_GDRIVE_API_BACKEND=http|sdkv2|sdkv3` selects an implementation for `gdrive://` operations
and Drive asset recovery/smudge. Unset/empty means `sdkv3`. Other values fail rather
than silently selecting another client. Local filesystem operations bypass this
selection; if an asset must be recovered from a configured Drive source, that
source uses the selected API backend. Authentication and OAuth scopes are shared.

## Architecture and compatibility

```text
Git remote-helper / assets
            |
       drive.Store       (identity, manifest validation, expected-version CAS)
            |
        drive.API
       /    |    \
     http sdkv2 sdkv3
       \    |    |
       Drive v2  Drive v3
```

The interface covers Metadata, CreateFolder, FindArchive, UploadFile, Download,
and conditional Patch. It uses project-owned File/Property/Parent types, not SDK
types. Store contains no HTTP endpoints or generated SDK calls. Both SDK adapters
use official generated calls for every operation, including Media uploads.

All backends preserve PUBLIC properties, generated immutable file IDs, ETags,
conditional publication, bounded pack chains, assets, and refs-before-ZIP ordering.
They can read/write the same existing repository without migration. None
bypasses API quotas, OAuth scopes, or Workspace administration policy.

### Drive v3 mapping and conditional writes

The former `sdk` setting is now `sdkv2`; `sdk` is rejected with the accepted
values in the error message. Select `sdkv3` for the official Drive v3 SDK.
No credential or repository migration is needed.

v3 maps `name` to the internal title, parent ID strings to Parent records,
`properties` to PUBLIC properties, and `appProperties` to PRIVATE properties.
It preserves unrelated properties and keeps repository pointers publicly visible
across OAuth applications. Creates use `files.create`; metadata and media updates
use `files.update` (PATCH). ID allocation uses `count`, and lists use `pageSize`,
`files`, and the `name` search field.

v3 has no JSON `etag` field. The adapter reads `File.ServerResponse.Header` from
an individual file GET and sends that ETag in `If-Match` for root publication,
folder renames, and ZIP overwrites. It never substitutes the numeric `version`
field or a list-response ETag. ZIP lookup makes an additional GET for the selected
file, revalidating its name, parent, type, and trash state before using its ETag.
If a response omits the ETag, conditional writes fail closed; the adapter never
falls back to an unconditional update or silently switches to v2. Mock tests
verify this contract; live v3 header availability and conditional-write semantics
have not been verified against a real Drive account.

## Implementation tradeoffs

| Area | HTTP | SDK v2 / SDK v3 (default) |
| --- | --- | --- |
| Small files (<8 MiB) | Resumable session plus data request | Multipart request; adapter buffer sized to the actual file |
| Large files | Handwritten 8 MiB chunks | Official Media uploader with 8 MiB chunks |
| Chunk recovery | Explicit status probes and acknowledged offsets | SDK chunk replay and retry behavior |
| Metadata retries | Up to 6 attempts, quota classification, exponential backoff/jitter, Retry-After | Adapter applies the same metadata policy around generated calls |
| Upload retries | Explicit bounded no-progress retries | Native per-chunk retry deadline (32 seconds); eligible whole-operation errors may be retried by the adapter, up to 6 attempts |
| Lost upload response | Session status query | SDK recovery; if the operation still errors, adapter verifies identity, size, and SHA-256 of remote bytes before accepting success |
| Progress | Bytes consumed per request, absolute retry offsets | Small multipart byte reads; large upload chunk callbacks; shared aggregate task |
| Maintenance | Full responsibility for HTTP codecs and upload state machine | Generated API types/methods, with project-specific consistency and error handling still maintained here |
| Dependencies | Transport uses net/http plus the existing OAuth client | Adds the pinned Google API module and its transitive dependencies |

Small SDK uploads explicitly disable SDK chunk buffering because their size is
known. Each attempt gets an independent file-sized buffer, avoiding an 8 MiB
allocation for a tiny manifest and avoiding reader/seek races while a failed
multipart goroutine unwinds. Large uploads retain the SDK's bounded chunk buffer.
Whole-operation retries rewind the source and reuse the same generated ID or ZIP
ETag. Permission errors and precondition failures are not blindly retried.

SDK errors are normalized before diagnostics: raw bodies/session URLs are not
printed, and generated-client debug logging is disabled. The SDK transport also
checks resumable session URLs against the initiating request's origin before
following them with the authenticated client.

Choosing `http` at runtime does not remove SDK code/dependencies from the binary:
all three implementations are compiled in. Binary-size comparisons require separate
build variants, which this change does not introduce.

## Repeatable local measurements

```sh
make benchmark-drive
# Equivalent:
go test ./git-remote-gdrive -run '^$' -bench '^BenchmarkDriveAPI$' \
  -benchmem -benchtime=300ms -count=3
```

Benchmarks use identical payloads and an in-process HTTP mock, without credentials
or network access to Google. They report elapsed time, B/op, allocations, and
requests/op for metadata, a 1 KiB file, and a 9 MiB file. Upload measurements include
generated-ID allocation but exclude Store directory discovery/publication. B/op
includes allocations in the mock server, not just the client, and is cumulative
allocated memory rather than peak resident memory. SDK service construction and
payload creation are outside the measured loop.

With no injected failures the request counts are deterministic:

| Operation | HTTP requests/op | SDK v2 requests/op | SDK v3 requests/op |
| --- | ---: | ---: | ---: |
| Metadata | 1 | 1 | 1 |
| Upload 1 KiB | 3 | 2 | 2 |
| Upload 9 MiB | 4 | 4 | 4 |

The small-file difference is generated ID + session creation + data for HTTP,
versus generated ID + multipart for SDK. This reduces one round trip; it does not
prove a specific real-world speedup. Compare multiple runs on an otherwise idle
machine. Quotas, disk speed, network latency, retries, and caching affect live work.

Reference run on 2026-09-13, macOS ARM64 / Apple M2 Pro, 10 logical CPUs,
using the command above without concurrent builds/tests. Each value is the
median of three runs (300 ms target per benchmark):

| Backend | Operation | Time/op | Allocated/op |
| --- | --- | ---: | ---: |
| HTTP | Metadata | 50.8 µs | 8,542 B |
| HTTP | Upload 1 KiB | 160.0 µs | 32,032 B |
| HTTP | Upload 9 MiB | 5.04 ms | 51,674,253 B |
| SDK v2 | Metadata | 63.9 µs | 15,059 B |
| SDK v2 | Upload 1 KiB | 177.4 µs | 45,967 B |
| SDK v2 | Upload 9 MiB | 5.38 ms | 50,551,767 B |
| SDK v3 | Metadata | 68.0 µs | 16,115 B |
| SDK v3 | Upload 1 KiB | 179.8 µs | 47,277 B |
| SDK v3 | Upload 9 MiB | 5.49 ms | 50,448,065 B |

These are short loopback samples, not statistically established performance
rankings. Both SDK versions save one request for a small upload but have similar
local timing; whether that helps across a real network requires live measurement.
Large-upload allocations include the mock's in-memory file storage and request
buffering, so they do not describe the production uploader's peak memory footprint.

## Compare on a real repository

After normal OAuth configuration, first check all three can read the same refs:

```sh
GIT_GDRIVE_API_BACKEND=http git ls-remote gdrive://FOLDER_ID
GIT_GDRIVE_API_BACKEND=sdkv2 git ls-remote gdrive://FOLDER_ID
GIT_GDRIVE_API_BACKEND=sdkv3 git ls-remote gdrive://FOLDER_ID
```

Use fresh clone directories so a previous pack cache cannot skip transfers:

```sh
time env GIT_GDRIVE_API_BACKEND=http git clone --no-checkout gdrive://FOLDER_ID clone-http
time env GIT_GDRIVE_API_BACKEND=sdkv2 git clone --no-checkout gdrive://FOLDER_ID clone-sdkv2
time env GIT_GDRIVE_API_BACKEND=sdkv3 git clone --no-checkout gdrive://FOLDER_ID clone-sdkv3
```

For push comparisons, use separate dedicated Drive roots with identical starting
snapshots and push the same commits/assets. Sequential pushes to one root would
measure a real push followed by an up-to-date no-op. Swap run order and repeat;
record wall time, failure/retry messages, and final refs. Do not infer quota usage
or production reliability from the local benchmark alone.

## Verification boundary

Shared mock tests cover metadata retries, permission/precondition failures,
pagination, missing ETags, error sanitization, cancellation, and retry limits.
All three clients run repository integration tests for clone/fetch/push, concurrent
initialization, lost responses, partial uploads, compaction, ZIP overwrite/stale
writers, legacy folder rename, asset integrity, and progress. The mock implements
both multipart and resumable protocols, including the SDK's no-308 response header
convention and overlapping chunk replay. Cross-backend tests alternate writers
and readers over the same root and verify assets, pull, ZIP identity, and Git
object integrity. v3-specific tests cover property visibility, header-only ETags,
missing ETags, and changed or ambiguous ZIP search results.

No live Drive benchmark or conditional-update verification is claimed. References:
[official v2 SDK](https://pkg.go.dev/google.golang.org/api/drive/v2),
[official v3 SDK](https://pkg.go.dev/google.golang.org/api/drive/v3),
[v2/v3 field and method mapping](https://developers.google.com/workspace/drive/api/guides/v2-to-v3-reference),
[media options](https://pkg.go.dev/google.golang.org/api/googleapi#MediaOption).
