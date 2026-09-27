# Data server recordings

What a real multitenant HAPI data server answered this package's seeder, captured on 2026-09-26
(local time; timestamps inside the answers are UTC, 2026-09-27) through an exact-bytes recording
proxy from the data-plane HAPI that `make validate` boots (HAPI FHIR 8.10.0, URL-partitioned
multitenant), freshly booted. That server's configuration is the platform's, not the Kit's own
data server child (which also enables clinical reasoning and loads the Kit's IG pins); the
warm-up answer is expected to be the same, but that is not verified. The requests are the
seeder's own and carry committed synthetic fixtures. No participant or patient data.

- `warm-provider.json`: `WarmValidate` on the `provider` tenant, with the server already warm:
  200 with an OperationOutcome holding one warning (`dom-6`: the resource has no narrative).
  On the same server, the first `$validate` after boot took 1.84 s and gave the same answer
  byte for byte; this one took 24 ms. A replay carries bytes, not timing, so a test that needs
  the slow first answer adds the delay itself and says so.
- `warm-unknown-tenant.json`: the warm-up against `nosuchtenant`, a tenant the server has no
  partition for, captured with the gateway seed loader's warm-up, whose request is
  byte-identical to this package's (the payload is the same constant in both). The server
  answered 200 with the same OperationOutcome: a type-level `$validate` does not resolve the
  partition. So the warm-up warms the server whatever the tenant, and its success says nothing
  about the tenant.
- `freshen-provider.json`: `FreshenPersonas` once, on a server that had just created the
  `provider` partition and held no data: the warm-up, 14 transaction Bundles posted to the
  `provider` tenant (each answered 200 with a `transaction-response` Bundle whose entries are
  `201 Created`, or `200 OK` for a resource an earlier bundle had already created), and the
  `seed-complete` marker PUT (answered 201 with the Basic as stored). Four of the transaction
  requests carry Observation `effectiveDateTime` values the seeder stamps with the current time
  (the capture's clock, as sent), so a replay on a later day cannot match them strictly. Tests
  serve these answers from a test-side server that matches every request on method, path,
  Content-Type and the whole body, with that one clock-stamped field (each Observation entry's
  `effectiveDateTime`) blanked on both sides after checking the sent value is a time inside the
  test's run, and that answers each recorded exchange once per freshen.

Scrub: answers are stored decoded (the client asked for gzip); headers are kept only where a
client reads them (`Content-Type`); `Date`, `X-Request-Id`, `X-Powered-By`, `Content-Location`,
`ETag` and `Last-Modified` are dropped. Each transaction answer also carried a `Location` header,
the answer Bundle's absolute URL on the capture's own loopback address and port; no client reads
it, so it is dropped here too, and the test-side server sets it from its own address and the
Bundle's id, as the server does. Each transaction answer's `self` link named the same address:
it is replaced with `http://data-server.invalid/fhir/provider` and declared in `scrubbed` with
the captured answer's size and SHA-256. Every exchange is kept in capture order and none is
marked `repeat`. Nothing else is changed.
