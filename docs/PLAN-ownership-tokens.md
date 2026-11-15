# bleak-node — Implementation Plan: Ownership Tokens, Invite Codes, Mutual-QR Handshake

Feature branch: `feat/#33-ownership-tokens`. Authority:
`CONCEPT.md` §7.1 (tokens) · §7.2 (invite codes) · §7.3 (mutual-QR) ·
§7.4 (resolved defaults). Contract source of truth stays
`docs/swagger.json` — it must be updated by WP-6.

Work packages are ordered; each is independently testable. One package (or
a coherent slice) per `feat/#33-*` / `bug/#33-*` branch round, per the repo
git rules.

---

## WP-1 — Schema & storage

New columns / tables (SQLite migrations in `internal/db`):

| Target | Change |
| --- | --- |
| `users` | `+ token_hash TEXT` (nullable), `+ mutual_code_hash TEXT` (nullable; presence = live code) |
| `rooms` | `+ token_hash TEXT` (nullable) |
| new `create_tokens` | `hash TEXT PK, created_at, used_at NULL` — one-time room-creation tokens |
| new `room_invites` | `code TEXT PK, room_address, created_at, expires_at, revoked_at NULL` — 6-digit join codes, stored plain |

Rules: hashes are SHA-256 hex of the raw secret; raw secrets are never
stored, returned, or logged. `mutual_code_hash` has no companion flag —
NULL means "consumed / none registered".

## WP-2 — Auth middleware (`internal/server`)

- Parse `Authorization: Bearer <token>`; hash; look up by record.
- Helpers: `requireUserToken(room, address)`, `requireRoomToken(address)`,
  `requireCreateToken()`.
- Errors: `401` missing/invalid token; `403` valid token, wrong record or
  rule; plain-text bodies (matching existing error style).
- Constant-time hash comparison (`crypto/subtle`).

## WP-3 — Gate the existing endpoints

| Endpoint | Required credential |
| --- | --- |
| `PUT/DELETE /api/room/{address}` | room token |
| `PUT/DELETE /api/room/{room}/user/{address}`, `PUT …/greeting`, `POST …/greeting/regenerate` | that persona's token |
| `POST /api/handshake` | the `from` persona's token |
| `GET /api/handshake/user/{address}`, `GET /api/user/{address}/chat`, room-scoped equivalents | that persona's token |
| `GET /api/room/{room}/user/{address}` | open; **`private_bio` returned only with that persona's token** |
| `GET /api/chat/{address}`, `GET /api/chat/{address}/message`, room-scoped chat/message reads | a participant persona's token **or** the room token |
| `DELETE /api/chat/{address}/message`, `POST /api/chat/{address}/kickoff` | participant or room token (full room control) |
| `POST /api/message` | the `from` persona's token (agent runtime keeps its internal path — no token) |
| `POST /api/room` | one-time creation token (header), consumed on use |
| `POST /api/room/{room}/user` | valid room invite code (request field `invite_code`); body carries the client-generated persona token (raw; hashed server-side) |

## WP-4 — New endpoints

1. **`POST /api/handshake/mutual`** (§7.3) — Bearer = *scanner's* persona
   token; body `{ user_address, mutual_code }`. Verify target's
   `mutual_code_hash` present and equal to SHA-256(code) → **clear the
   hash** (code consumed even if the pair already exists — one chat per
   pair, no second chat); income>outcome applies to the caller's A→B
   direction (a rule rejection does **not** consume the code); create both
   directed handshakes (idempotent) + auto-chat if new. Errors: 401 /
   404 unknown user / 410 "code already used" / 403 income rule.
2. **`POST /api/room/{room}/user/{address}/mutual-code`** — Bearer = that
   persona's token; body carries the **hash** of the new one-time code
   (owner's wording); stores it → code live again.
3. **`POST /api/room/{room}/user/{address}/token`** — Bearer = current
   persona token; body `{ token }` (raw, hashed server-side) → rotation;
   old token dead.
4. **`POST /api/room/{address}/token`** — same for the room token.
5. **Invite management (room token):** `POST /api/room/{address}/invite`
   (mint, optional `expires_at`, default 24 h) · `DELETE
   /api/room/{address}/invite/{code}` (revoke) · `GET
   /api/room/{address}/invite` (list active).
6. **Creation-token minting:** operator CLI (small `cmd/` utility or make
   target) → prints the raw token once, stores only the hash.

## WP-5 — Legacy migration (test data)

Script: for every existing room and persona — generate a random token,
store its hash, append `raw → record` to a **gitignored** file (e.g.
`data/legacy-tokens.txt`); register an initial live mutual-code hash per
persona; mint one default invite code per room. After the run, gating is
universal.

## WP-6 — Contract & tests

- Update `docs/swagger.json` / `swagger.yaml`: request fields
  (`invite_code`, client tokens, `mutual_code`), new endpoints, 401/403/410
  semantics.
- Unit tests: middleware (accept/reject/wrong-record), constant-time
  compare, mutual-code lifecycle (register → use → consumed → replay 410 →
  refresh → live), idempotent pair + no-second-chat, income-rule
  rejection without code burn, invite expiry/revocation, creation-token
  single use.
- Rate-limit join/invite-code attempts per source (6-digit space is small).

## WP-7 — Docs sync

- CONCEPT.md §4 API surface + ledger checkboxes updated post-implementation;
  swagger is the contract, CONCEPT.md the *why*.

---

## Client consequences (bleak-web v2 scope, for the next concept pass)

1. Persona + room tokens in `localStorage`; `Authorization: Bearer` on every
   mutation and gated read; token rotation UI.
2. Persona badge QR becomes **live**: encodes `?u={address}&m={code}`,
   polls `mutual_code_live`, re-renders on rotation.
3. Scanner persona-context calls `POST /api/handshake/mutual` (own token +
   scanned `u` + `m`); stale-code scans surface an explicit "code already
   used" state. The v1 client-side two-POST routine is deleted.
4. Join flow uses **native invite codes** (bleak-node resolves them); the
   helper's code→address mapping is retired — helper keeps only QR PNG
   rendering until that moves server-side too.
5. Host flow: obtains a one-time creation token (operator-granted), sends
   it + the client-generated room token; room-token powers (observe/clear/
   kickoff chats) replace the per-member drill-down reads.
