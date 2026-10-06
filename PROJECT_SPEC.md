# Metrology Assurance Suite

## Project Goal

Metrology Assurance Suite is a local HTTP service that keeps a traceable record
of laboratory measurement assurance work. It registers devices, opens a
verification case for one device, records technician measurements, requests a
repeat measurement when tolerance is exceeded, and seals the final evidence
record after review.

The service is intentionally self-contained. It uses an in-memory repository
with atomic copy-on-write mutations and does not require a database or an
external service.

## Users

- Laboratory steward: registers devices and opens verification cases.
- Measurement technician: records observed values for an open case.
- Quality verifier: reviews accepted measurements and seals the case.

## Core Entities

- Device: a laboratory asset with a unique asset tag, model, room, steward, and
  lifecycle state.
- VerificationCase: one bounded assurance activity for a device, with a nominal
  value, tolerance, current state, version, and assigned steward.
- Measurement: an observed value, computed deviation, acceptance decision,
  technician, and timestamp.
- EvidenceSeal: the immutable closing record for a reviewed case, including the
  selected accepted measurement and a frozen snapshot of the reviewer
  qualification basis in force when the seal was created.
- ReviewerQualification: a reviewer's eligibility to seal, with a review scope,
  a validity window, and an active or suspended status.
- AuditEvent: an append-only trace of meaningful case and device changes.

## Workflows

### 1. Register Device

The steward sends `POST /devices` with an asset tag, model, room, and steward.
The service validates the request, normalizes the asset tag, rejects duplicate
tags, and creates an active device.

### 2. Open Verification Case

The steward sends `POST /cases` with a device ID, nominal value, tolerance, and
owner. The service verifies that the device exists, is active, and has no
unfinished case. A valid request creates a case in `awaiting_measurement`.

### 3. Record Measurement

A technician sends `POST /cases/{id}/measurements`. The case must be
`awaiting_measurement`. The service computes the signed deviation from the
nominal value. An in-tolerance measurement moves the case to
`review_ready`; an out-of-tolerance measurement moves it to
`recheck_required`.

### 4. Reopen Verification Case

The steward sends `POST /cases/{id}/reopen` after an out-of-tolerance result.
Only `recheck_required` cases may move back to `awaiting_measurement`. The
service records the actor and reason in the audit trail.

### 5. Seal Verification

The verifier sends `POST /cases/{id}/seal` after an accepted measurement, naming
the reviewer and the reviewer's qualification. Only `review_ready` cases may be
sealed, and only by a reviewer whose qualification exists, is active, and covers
the decision instant within its validity window. The qualification check, the
case transition, and the seal write happen in one repository commit using one
decision instant: a rejected seal leaves no case state, seal, or audit event
behind. A valid request selects the accepted measurement with the smallest
absolute deviation, freezes the qualification basis (id, reviewer, scope,
validity window, capture time) into the seal, creates exactly one evidence
seal, moves the case to `sealed`, and rejects later mutations. The frozen basis
is what every later read returns; suspension or expiry of the qualification
after sealing never rewrites it.

### 6. Register Reviewer Qualification

The steward sends `POST /qualifications` with the reviewer name, review scope,
and a validity window. The service validates the window (`valid_until` after
`valid_from`) and creates an active qualification.

### 7. Suspend Reviewer Qualification

The steward sends `POST /qualifications/{id}/suspend` with a reason. Only an
active qualification can be suspended. Suspension and sealing serialize under
the repository mutation lock, so when they arrive together exactly one order
wins: a seal committed while the qualification is active stays valid (with its
frozen basis) even though suspension follows, while suspension committed first
makes the seal request fail with no effect.

## State And Rules

Device state:

- `active -> retired`
- `retired` is terminal.

VerificationCase state:

- `awaiting_measurement -> review_ready`
- `awaiting_measurement -> recheck_required`
- `recheck_required -> awaiting_measurement`
- `review_ready -> sealed`
- `sealed` is terminal.

Required invariants:

- Asset tags are unique after case-insensitive normalization.
- A device has at most one unfinished verification case.
- Measurements are accepted only while a case is awaiting measurement.
- A tolerance must be positive.
- A case can have at most one evidence seal.
- A sealed case cannot accept measurements, reopen, or seal again.
- A seal can only be created by the reviewer named on a qualification that is
  active and within its validity window at the single decision instant of the
  sealing commit.
- Qualification authorization and all seal writes share one repository commit;
  a rejected seal leaves no case, seal, authority, or audit trace.
- An evidence seal carries an immutable snapshot of the qualification basis at
  decision time, which later suspension or expiry never changes.
- Every mutation that changes a case also appends an audit event in the same
  repository commit.

## Modules

- `cmd/metrology-service`: process entrypoint and graceful HTTP shutdown.
- `cmd/workflowcheck`: bounded smoke checks that call the public HTTP API.
- `internal/httpapi`: routing, request decoding, and stable error mapping.
- `internal/assurance`: workflow orchestration and atomic decisions.
- `internal/domain`: entities, validation, state transitions, and errors.
- `internal/repository`: mutex-protected in-memory persistence with copy-on-write
  commits.
- `internal/contracts`: request and response contracts shared by the HTTP layer.

## Public Interfaces

- `GET /healthz`
- `POST /devices`
- `GET /devices/{id}`
- `POST /cases`
- `GET /cases/{id}`
- `POST /cases/{id}/measurements`
- `POST /cases/{id}/reopen`
- `POST /cases/{id}/seal`
- `GET /cases/{id}/events`
- `POST /qualifications`
- `GET /qualifications/{id}`
- `POST /qualifications/{id}/suspend`

## Validation Plan

The baseline build command is `go build ./...`. Each declared workflow has a
bounded production smoke command under `cmd/workflowcheck`. The command starts
an HTTP test server in the same process, calls the public routes, and verifies
both success and failure behavior. The seal check also races two concurrent
seal requests to prove that the repository mutation lock permits exactly one
seal.

Tests are intentionally deferred for this initialization baseline. The later
engineering-task stage will add unit and integration tests, including red/green
verification for the workflows and their failure paths.
