# TODO — Ownership Tokens, Invite Codes, Mutual-QR Handshake (feature #33)

> Actionable checklist for implementing `CONCEPT.md` §7.1–§7.4 in bleak-node.
> Full design & rationale: `docs/PLAN-ownership-tokens.md` (WP-1…WP-7).
> Work in `feat/#33-*` branches; check items off as you land them.
> After WP-6, sync `docs/swagger.json` and notify bleak-web (its api-client
> reconciles the TBD field names against your swagger — see its
> ARCHITECTURE.md §10.13.1).

## WP-1 — Schema & storage
- [ ] `users`: add `token_hash TEXT` (nullable), `mutual_code_hash TEXT` (nullable — presence = live code; NULL = consumed)
- [ ] `rooms`: add `token_hash TEXT` (nullable)
- [ ] new table `create_tokens (hash TEXT PK, created_at, used_at NULL)` — one-time room-creation tokens
- [ ] new table `room_invites (code TEXT PK, room_address, created_at, expires_at, revoked_at NULL)` — 6-digit codes, stored plain
- [ ] hashes are SHA-256 hex; raw secrets never stored/returned/logged

## WP-2 — Auth middleware (internal/server)
- [ ] parse `Authorization: Bearer <token>`; hash; compare via `crypto/subtle` (constant-time)
- [ ] helpers: `requireUserToken(room, addr)` · `requireRoomToken(addr)` · `requireCreateToken()`
- [ ] errors: 401 missing/invalid, 403 valid-but-wrong-record/rule; plain-text bodies (house style)

## WP-3 — Gate the existing endpoints
- [ ] `PUT/DELETE /api/room/{address}` → room token
- [ ] `PUT/DELETE …/user/{address}`, `PUT …/greeting`, `POST …/greeting/regenerate` → that persona's token
- [ ] `POST /api/handshake` → the `from` persona's token
- [ ] `GET /api/handshake/user/{address}`, `GET /api/user/{address}/chat` (+ room-scoped equivalents) → that persona's token
- [ ] `GET /api/room/{room}/user/{address}` → public; return `private_bio` ONLY with a matching token
- [ ] `GET /api/chat/{address}`, `GET /api/chat/{address}/message` (+ room-scoped) → participant token OR room token
- [ ] `DELETE /api/chat/{address}/message`, `POST /api/chat/{address}/kickoff` → participant OR room token (full room control)
- [ ] `POST /api/message` → the `from` persona's token (agent runtime keeps its internal token-free path)
- [ ] `POST /api/room` → one-time creation token in header (header name your call — bleak-web proposes `x-create-token`), consumed on use
- [ ] `POST /api/room/{room}/user` → valid, unexpired, unrevoked `invite_code` in body; body also carries the client-generated raw persona `token` (hash it, store it)
- [ ] rate-limit join/invite-code attempts per source (6-digit space is brute-forceable)

## WP-4 — New endpoints
- [ ] `POST /api/handshake/mutual` — Bearer = scanner's persona token; body `{user_address, mutual_code}`; verify hash present & match → **clear `mutual_code_hash`** (burn even if the pair already exists; no second chat ever); income>outcome applies to the caller's A→B (rejection does NOT burn the code); create both directed handshakes (idempotent) + auto-chat if new. Errors: 401 / 404 / 410 "code already used" / 403
- [ ] `POST /api/room/{room}/user/{address}/mutual-code` — Bearer = that persona; body = the NEW code's **hash** (client-generated code, server never sees raw); storing the hash makes the code live
- [ ] `POST /api/room/{room}/user/{address}/token` — Bearer = current persona token; body = new RAW token (hash once); old token dies
- [ ] `POST /api/room/{address}/token` — same for the room token
- [ ] invite management (room token): `POST /api/room/{address}/invite` (optional `expires_at`, default 24 h) · `GET /api/room/{address}/invite` · `DELETE /api/room/{address}/invite/{code}`
- [ ] operator CLI/make target to mint one-time creation tokens (raw printed once, hash stored)
- [ ] room creation response returns the room (NOT the token — the client generated it)

## WP-5 — Legacy migration (test data)
- [ ] script: for every room & persona — generate random token, store hash, append `raw → record` to a **gitignored** file (e.g. `data/legacy-tokens.txt`)
- [ ] register an initial live mutual-code hash per persona
- [ ] mint one default invite code per room
- [ ] after the run, gating is universal

## WP-6 — Contract & tests
- [ ] update `docs/swagger.json` + `swagger.yaml` (new endpoints, request fields, 401/403/410)
- [ ] tests: middleware accept/reject/wrong-record; mutual-code lifecycle (register → use → replay 410 → refresh → live); pair idempotency + never-a-second-chat; income-rule rejection without code burn; invite expiry/revocation; creation-token single use
- [ ] ⚠️ bleak-web depends on this: field names for creation-token header, mutual-code hash body, invite response

## WP-7 — Docs sync
- [ ] update `CONCEPT.md` §4 API surface + §6 ledger checkboxes
- [ ] bleak-web's `ARCHITECTURE.md §10.13` TBDs reconciled once swagger lands

## Two amendments to confirm while implementing (bleak-web flagged, defaults recorded)
- [ ] **6.9a:** let room-scoped per-user reads (e.g. `GET /api/room/{room}/user/{address}/chat`) accept the **room token** — without it the host console can read chats but cannot enumerate a member's chats; with it the host also gets per-member handshake counts
- [ ] **6.9b:** pick a code→room resolution shape for the join flow (codes unique globally + resolve route, or a small public resolve endpoint) — bleak-web's `resolveInviteCode` seam adapts either way
