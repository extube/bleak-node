# TODO — Ownership Tokens, Invite Codes, Mutual-QR Handshake (feature #33)

> Actionable checklist for implementing `CONCEPT.md` §7.1–§7.4 in bleak-node.
> Full design & rationale: `docs/PLAN-ownership-tokens.md` (WP-1…WP-7).
> Work in `feat/#33-*` branches; check items off as you land them.
> After WP-6, sync `docs/swagger.json` and notify bleak-web (its api-client
> reconciles the TBD field names against your swagger — see its
> ARCHITECTURE.md §10.13.1).
>
> **Status: implemented 2026-11-16** on `feat/#33-ownership-tokens`.
> Contract field names as landed: creation-token header `x-create-token`;
> mutual-code registration body `{"mutual_code_hash": "<sha256-hex>"}`;
> invite response `{"room", "code", "created_at", "expires_at"}`;
> join body carries `invite_code` + client-generated `token`;
> room creation body carries client-generated `token` (header carries the
> one-time creation token), response = room + first `invite_code`;
> token-gated persona reads add the derived `mutual_code_live` boolean.

## WP-1 — Schema & storage
- [x] `users`: add `token_hash TEXT` (nullable), `mutual_code_hash TEXT` (nullable — presence = live code; NULL = consumed)
- [x] `rooms`: add `token_hash TEXT` (nullable)
- [x] new table `create_tokens (hash TEXT PK, created_at, used_at NULL)` — one-time room-creation tokens
- [x] new table `room_invites (code TEXT PK, room_address, created_at, expires_at, revoked_at NULL)` — 6-digit codes, stored plain
- [x] hashes are SHA-256 hex; raw secrets never stored/returned/logged

## WP-2 — Auth middleware (internal/server)
- [x] parse `Authorization: Bearer <token>`; hash; compare via `crypto/subtle` (constant-time)
- [x] helpers: `requireUserToken(room, addr)` · `requireRoomToken(addr)` · `requireCreateToken()`
- [x] errors: 401 missing/invalid, 403 valid-but-wrong-record/rule; plain-text bodies (house style)

## WP-3 — Gate the existing endpoints
- [x] `PUT/DELETE /api/room/{address}` → room token
- [x] `PUT/DELETE …/user/{address}`, `PUT …/greeting`, `POST …/greeting/regenerate` → that persona's token
- [x] `POST /api/handshake` → the `from` persona's token
- [x] `GET /api/handshake/user/{address}`, `GET /api/user/{address}/chat` (+ room-scoped equivalents) → that persona's token
- [x] `GET /api/room/{room}/user/{address}` → public; return `private_bio` ONLY with a matching token
- [x] `GET /api/chat/{address}`, `GET /api/chat/{address}/message` (+ room-scoped) → participant token OR room token
- [x] `DELETE /api/chat/{address}/message`, `POST /api/chat/{address}/kickoff` → participant OR room token (full room control)
- [x] `POST /api/message` → the `from` persona's token (agent runtime keeps its internal token-free path)
- [x] `POST /api/room` → one-time creation token in header (`x-create-token`), consumed on use
- [x] `POST /api/room/{room}/user` → valid, unexpired, unrevoked `invite_code` in body; body also carries the client-generated raw persona `token` (hash it, store it)
- [x] rate-limit join/invite-code attempts per source (6-digit space is brute-forceable)

## WP-4 — New endpoints
- [x] `POST /api/handshake/mutual` — Bearer = scanner's persona token; body `{user_address, mutual_code}`; verify hash present & match → **clear `mutual_code_hash`** (burn even if the pair already exists; no second chat ever); income>outcome applies to the caller's A→B (rejection does NOT burn the code); create both directed handshakes (idempotent) + auto-chat if new. Errors: 401 / 404 / 410 "code already used" / 403
- [x] `POST /api/room/{room}/user/{address}/mutual-code` — Bearer = that persona; body = the NEW code's **hash** (`mutual_code_hash`; client-generated code, server never sees raw); storing the hash makes the code live
- [x] `POST /api/room/{room}/user/{address}/token` — Bearer = current persona token; body = new RAW token (hash once); old token dies
- [x] `POST /api/room/{address}/token` — same for the room token
- [x] invite management (room token): `POST /api/room/{address}/invite` (optional `expires_at`, default 24 h) · `GET /api/room/{address}/invite` · `DELETE /api/room/{address}/invite/{code}`
- [x] operator CLI/make target to mint one-time creation tokens (`bin/tokenctl mint`, `make tokenctl`; raw printed once, hash stored)
- [x] room creation response returns the room + the first `invite_code` (NOT the token — the client generated it)

## WP-5 — Legacy migration (test data)
- [x] script: for every room & persona — generate random token, store hash, append `raw → record` to a **gitignored** file (`data/legacy-tokens.txt`) — `bin/tokenctl migrate`, `make migrate-legacy`
- [x] register an initial live mutual-code hash per persona
- [x] mint one default invite code per room
- [x] after the run, gating is universal

## WP-6 — Contract & tests
- [x] update `docs/swagger.json` + `swagger.yaml` (new endpoints, request fields, 401/403/410)
- [x] tests: middleware accept/reject/wrong-record; mutual-code lifecycle (register → use → replay 410 → refresh → live); pair idempotency + never-a-second-chat; income-rule rejection without code burn; invite expiry/revocation; creation-token single use
- [x] ⚠️ bleak-web depends on this: field names for creation-token header, mutual-code hash body, invite response (recorded in the status block above)

## WP-7 — Docs sync
- [x] update `CONCEPT.md` §4 API surface + §6 ledger checkboxes
- [ ] bleak-web's `ARCHITECTURE.md §10.13` TBDs reconciled once swagger lands (bleak-web side — pending its reconciliation round)

## Two amendments to confirm while implementing (bleak-web flagged, defaults recorded)
- [x] **6.9a applied:** room-scoped per-user reads (e.g. `GET /api/room/{room}/user/{address}/chat`) accept the **room token** in addition to that persona's token — the host console keeps its per-member drill-down
- [x] **6.9b applied:** codes are globally unique (6-digit PKs) + a small public resolve endpoint `GET /api/invite/{code}` → `{room}` (rate-limited); bleak-web's `resolveInviteCode` seam can call it directly
