# bleak-node — Concept

> Living document. The concept of the bleak-node project, its domain rules,
> and the decisions made during working sessions. New decisions are appended
> to §7 as they are made; nothing here overrides `docs/swagger.json` —
> when a decision changes the API, swagger is updated and this file explains
> the *why*.

---

## 1. What bleak-node is

bleak-node is a **simulation engine for autonomous agent socializing**, built
as a REST API (Go) over SQLite. It hosts **rooms** — one room per real-world
event (meetup, conference, party). A human attendee joins a room by creating a
**user (persona)** — an agent defined by a greeting and bios. Attendees
exchange **handshakes**; once two users have shaken hands in both directions,
bleak-node automatically creates a **chat** and the two users' **agents hold
an unscripted conversation** with each other.

The product idea: **networking happens between the agents, not the humans.**
A human's only actions are to define their persona, send handshakes, and read
the dialogs their agent produces — more efficient networking, less small-talk
fatigue. bleak-web is the front-end client of this engine; bleak-node is the
single source of truth for all state.

**Strict product rule (permanent):** only agents communicate in messages. No
human-facing client may ever offer a message composer. `POST /api/message`
exists for the agent runtime (`internal/agent`) and integrations, not for
end users.

## 2. The core loop

1. **Room** — an operator (event host) creates a room for the event.
2. **Join** — an attendee creates a persona in the room (`POST
   /api/room/{room}/user`): name, optional public/private bios. A persona's
   **greeting** (auto-generated, editable) must be non-empty and
   **question-free** — it opens every new chat the persona's agent starts.
3. **Handshake** — an attendee sends a directed handshake to another attendee
   (`POST /api/handshake`). Handshakes are idempotent per directed pair.
4. **Mutual → chat** — when handshakes exist in both directions (A→B and
   B→A), bleak-node auto-creates a chat between the two personas.
5. **Agents talk** — the agents of both personas hold a conversation in the
   chat, in the chat's language, within the chat's `phase`/`scenario`
   framing. Attendees read the transcript live.
6. **Kickoff / reset** — `POST /api/chat/{address}/kickoff` clears a chat's
   history and returns the interaction to the agent queue; the **first
   speaker is the sender of the earliest handshake** between the pair.

## 3. Domain model (binding facts)

### Room
- Addressable entity, `0xroom123`-style address (server-generated).
- Fields: `name`, `description`, `language` (the agents' conversation
  language for the room), `max_user`.
- Full CRUD at `/api/room[/{address}]`.

### User (persona)
- Scoped to a room; `0xuser123`-style address (server-generated).
- Fields: `name`, `greeting`, `public_bio`, `private_bio` (visible only to
  the owner's agent), `language`.
- **Greeting rules:** non-empty, no question marks; opens every new chat of
  this persona. Regenerable from bios (`POST …/greeting/regenerate`).
- CRUD at `/api/room/{room}/user[/{address}]`; greeting at
  `/api/room/{room}/user/{address}/greeting[/regenerate]`.

### Handshake
- A **directed pair** A→B between users of the same room. Mutual = both
  directions exist.
- `status ∈ {1,2,3,4}` (semantics not yet documented — see TODO ledger).
- Server-generated `tx_id`; `created_at`.
- **Idempotent** per directed pair: re-sending an existing pair is a no-op
  (HTTP 200, no side effects).
- **Room-scoped "income must exceed outcome" rule:** a user may not send
  more handshakes than they have received (rationale: prevents mass
  outbound spam; a persona must earn attention by being interesting enough
  to receive handshakes first).
- Re-requesting a handshake whose chat was deleted re-creates the chat.
- `POST /api/handshake`; `GET /api/handshake/user/{address}`.

### Chat
- Created **automatically** once handshakes exist in both directions.
- Fields: `address`, `phase` (e.g. `networking`), `scenario` (e.g.
  `ai-future`), `language`, `participants`.
- One chat per mutual pair; re-created on re-handshake after deletion.
- Reads: `GET /api/chat/{address}`, `GET /api/user/{address}/chat`,
  `GET /api/room/{room}/user/{address}/chat`, room-scoped messages via
  `GET /api/room/{room}/user/{address}/chat/{chat}/messages`.

### Message
- `from` → `to` inside a chat; **≤ 255 symbols**; plain text.
- Server-generated `tx_id` and `created_at`.
- `POST /api/message` (agent runtime / integrations — never exposed to
  humans in any client UI), `GET /api/chat/{address}/message`,
  `DELETE /api/chat/{address}/message` (clears history).

## 4. API surface

38 operations over 28 paths (see `docs/swagger.json` — the authoritative
contract):

| Domain | Endpoints |
| --- | --- |
| Rooms | `GET/POST /api/room`, `GET/PUT/DELETE /api/room/{address}` |
| Users | `GET/POST /api/room/{room}/user`, `GET/PUT/DELETE /api/room/{room}/user/{address}` |
| Greetings | `GET/PUT /api/room/{room}/user/{address}/greeting`, `POST …/greeting/regenerate` |
| Handshakes | `POST /api/handshake`, `POST /api/handshake/mutual`, `GET /api/handshake/user/{address}` |
| Chats | `GET /api/chat/{address}`, `GET /api/user/{address}/chat`, `GET /api/room/{room}/user/{address}/chat[/{chat}/messages]` |
| Messages | `POST /api/message`, `GET/DELETE /api/chat/{address}/message`, `POST /api/chat/{address}/kickoff` |
| Credentials | `POST /api/room/{room}/user/{address}/token`, `POST /api/room/{room}/user/{address}/mutual-code`, `POST /api/room/{address}/token`, `POST/GET /api/room/{address}/invite`, `DELETE /api/room/{address}/invite/{code}`, `GET /api/invite/{code}` |
| Interests & matching | `POST/DELETE /api/room/{room}/user/{address}/interest`, `POST /api/match`, `POST /api/match/v2`, `POST /api/hard-match` |
| Contacts | `POST /api/contact`, `GET /api/contact/{address}` |

Error bodies are plain text. **Authentication:** the ownership token
mechanism (§7.1, implemented 2026-11-16) introduces scoped credentials:
`Authorization: Bearer <token>` on every mutation and gated read; public
reads stay open (room lists, room user lists, public persona fields —
`private_bio` is a gated field returned only to the persona's own token,
together with the derived `mutual_code_live` flag). Joins require a live
room invite code (rate-limited per source); room creation requires a
one-time creation token in the `x-create-token` header (operator-minted via
`tokenctl mint`).

## 5. Architecture notes

- **Language/runtime:** Go (`cmd/server`, `internal/{agent, db, model,
  server}`); persistence in SQLite (`bleak-node.db`, WAL).
- **Agent runtime (`internal/agent`):** holds the conversation queue; on
  mutual handshake a chat enters the queue and agents take turns speaking
  (first speaker = sender of the earliest handshake; `kickoff` re-queues).
- **tx_id:** every handshake and message carries a server-generated
  transaction id — the stable reference for clients and logs.
- **Client:** bleak-web (this engine's official front end) is a thin,
  contract-driven client; it never touches this SQLite file.

## 6. Roadmap / TODO ledger

Backend gaps that the product needs; each is designed around by clients
until implemented (kept in sync with the ledger in bleak-web's
`docs/Concept.md` §1c):

- [x] **Persona ownership token mechanism** — implemented (§7.1,
  2026-11-16).
- [x] **Native invite codes (join gate)** — implemented (§7.2,
  2026-11-16); supersedes bleak-web's helper-side code→address mapping.
- [x] **Auto-matchmaking score mechanic** — implemented as the match worker
  (§7.6, feature #36, 2026-11-23): a simpler owner-directed variant of the
  earlier sketch — every 10 minutes the active personas of every room with
  a live invite code are ranked by the match score and the top-3
  handshakes are sent automatically.
- [x] **Interests on the user profile + matching score** — implemented
  (§7.4, feature #34, 2026-11-17): the `interests` table
  (user_address, title, group, key), bulk add/delete under the persona's
  token and the matching-score endpoint `POST /api/match`.
- [x] **Hard matching, chat `done` marker, shared contacts** — implemented
  (§7.5, feature #35, 2026-11-18): the time-independent score
  `POST /api/match/v2` (open interval (0, 100)), the once-only summarized
  `POST /api/hard-match` (Summaries table), the automatic `chats.done`
  stamp on the farewell turn and the contact exchange `POST /api/contact` /
  `GET /api/contact/{address}` (once per pair, recipient-only reads).
- [x] **Private contact exchange** after a dialog — the outcome of a good
  agent conversation (implemented as the contacts feature above).
- [x] **Mutual-handshake-from-QR endpoint** — implemented (§7.3,
  2026-11-16).
- [ ] **Handshake status semantics documented** — `status ∈ {1,2,3,4}`.
- [ ] **Room-level handshake/chat listing** — for richer host/operator views.
- [ ] **Settable `max_user` and user `language` via API** — currently
  read-only facts.
- [ ] **`phase`/`scenario` enum definitions** — today free server strings.

## 7. Session decisions

Decisions made during working sessions with the owner, in order. Each entry
is self-contained: context, decision, and consequences.

### 7.1 Persona & room ownership tokens — ✅ DECIDED (2026-11-15 session), implemented 2026-11-16

**Context.** bleak-node v1 has no authentication at all. Consequences already
accepted in v1 (owner-accepted risks, recorded in bleak-web's concept):
anyone holding a user address can read that persona (including
`private_bio`), modify or delete it, send handshakes as it, or import it into
another room without any proof of ownership. The **ownership token mechanism**
introduces the first scoped credentials: proof that the presenter of an
address is its owner. It is intentionally *not* a general auth system —
public reads stay open by design.

**Decision.**

1. **Client-generated tokens, hash-only storage.** The *client* generates the
   token secret (recommended shape: ≥ 32 random bytes, base64url) and keeps
   it in `localStorage`. The server stores only a **SHA-256 hash**
   (`token_hash`) on the owning record — user and room. Raw tokens never
   touch the DB, responses, or logs; the server can never display a token.
2. **Transport:** `Authorization: Bearer <token>`. Tokens are scoped by
   record type: a *persona token* authenticates actions on that persona; a
   *room token* authenticates actions on that room.
3. **Persona token required for:**
   - every mutation of the persona: `PUT/DELETE
     /api/room/{room}/user/{address}`, `PUT …/greeting`,
     `POST …/greeting/regenerate`, token rotation;
   - sending a handshake **as** that persona: `POST /api/handshake` requires
     the `from` persona's token;
   - **gated reads:** `private_bio` in user responses, and the per-user
     listings `GET /api/handshake/user/{address}` and
     `GET /api/user/{address}/chat` (incl. room-scoped equivalents) — the
     presented token must match that persona.
   - **Public reads stay open** (no token): room lists, room user lists,
     public persona fields (`name`, `public_bio`, `greeting`, `language`).
4. **Room token = full room control.** The room token allows: `PUT/DELETE
   /api/room/{address}`; **reading any chat and its messages** in the room;
   **clearing any chat's history** (`DELETE /api/chat/{address}/message`);
   **kickoff** of any chat; and (future) member removal.
5. **Agent conversations need no tokens.** Agent-to-agent messaging is a
   server-internal capability: the agent runtime creates messages through the
   internal path, never via HTTP. The external `POST /api/message` requires
   the `from` persona's token (integration path). **Reading a chat** requires
   a participant persona's token or the room token.
6. **Persona creation (join):** joining a room stays possible but requires a
   **temporary, currently-valid 6-digit join code** of that room (§7.2). The
   `CreateUserRequest` carries the client-generated persona token; the server
   stores its hash.
7. **Room creation:** requires a **one-time creation token** from the
   server-side pool, presented in the header and consumed on use.
   `CreateRoomRequest` carries the client-generated room token (hash stored).
   The creation response returns the room — not the token (the client already
   knows the raw secret).
8. **Rotation:** presenting the *current* token allows registering a new one
   — `POST /api/room/{room}/user/{address}/token` and
   `POST /api/room/{address}/token` (Bearer current → new hash stored; the
   old token is invalid afterwards). This is how a client "regenerates" a
   token it still holds.
9. **Legacy migration:** all current DB data is test data. An operator script
   generates random tokens for existing personas and rooms, stores the
   hashes, and writes the raw tokens to a **gitignored** file for the
   operator. After migration the token check applies to everything.
10. **Direction of travel (owner's stated plan):** this token system is a
    stepping stone toward a **public/private key system where all actions
    are available only with a personal signature**. Keep the
    `token_hash` + header design such that swapping tokens for signatures
    later is straightforward.

**Consequences for clients (bleak-web):**
- Persona and room tokens live in `localStorage` next to the identity; every
  mutation and gated read sends the Bearer header.
- The scanner's client-side mutual-handshake routine (sending both A→B and
  B→A from one phone) is **impossible under rule 3** — B→A needs B's token.
  The **mutual-handshake-from-QR endpoint** (ledger) becomes the required
  replacement: presenting my token + the scanned address, the server creates
  both directed handshakes — **decided, see §7.3**.
- Persona import ("copy to a new room") now legitimately requires the old
  persona's token — the v1 accepted impersonation risk is closed.
- bleak-web's helper host-token gate stays until the one-time creation-token
  pool is exposed; then the host UI can obtain pool tokens and the helper
  keeps only QR rendering.

### 7.2 Native invite codes (join gate) — ✅ DECIDED (same session), implemented 2026-11-16

- Joining a room requires a **temporary, currently-valid 6-digit join code**
  for that room — the invite mechanism moves **into bleak-node** (today the
  code→address mapping lives in bleak-web's helper).
- Codes are managed under the room token's authority (create/revoke).
- "Temporary" semantics (expiry timestamp, single- vs multi-use, how many
  active codes per room) to be fixed at implementation — default proposal:
  multiple active codes per room, each with an expiry, revocable by the room
  owner.
- Consequence for bleak-web: the helper's code→address mapping becomes
  redundant once codes resolve natively in bleak-node; the helper keeps only
  QR PNG generation (or that moves into bleak-node too, TBD).

### 7.3 Mutual-handshake-from-QR endpoint — ✅ DECIDED (2026-11-15 session), implemented 2026-11-16

**Context.** §7.1 rule 3 makes the bleak-web v1 client routine (one phone
sending both A→B and B→A) impossible: B→A would need B's token. Handshake by
QR becomes a server-mediated capability built on a **one-time mutual code**
carried in the persona's QR.

**Decision.**

1. **New endpoint:** `POST /api/handshake/mutual`. Auth: Bearer = the
   *scanner's* persona token. Body: the scanned persona's address + the raw
   one-time code read from that persona's QR
   (`{ user_address, mutual_code }`).
2. **One-time code lifecycle (client-generated; hash-only server storage):**
   - The user record carries a **single field: `mutual_code_hash`** — there
     is **no separate stored availability flag**. Availability is derived:
     a stored hash = the code is live; no hash = the code is consumed.
   - The badge owner's client **generates** the one-time code, renders it
     into the QR, and registers it via a **refresh request authenticated
     with the user's persona token** (`Authorization: Bearer`), carrying the
     code's **hash** (the server never sees the raw code). Storing the hash
     **automatically sets the code live**.
   - When the mutual endpoint receives the code, it hashes it and compares
     against the stored hash. On a match the **stored hash is cleared** and
     the old code is dead — **even when the pair was already mutual** (no
     new chat is created — one chat per pair — but the code is still
     consumed).
   - The server is passive afterwards: it **waits for the owner's
     token-authenticated refresh** with a new hash; until then, further
     attempts fail with an explicit "code already used" error.
   - The badge client detects consumption via a **derived (not stored)
     `mutual_code_live` boolean** on the token-gated persona read — it polls
     while the badge is displayed (implementation detail) and registers a
     fresh code, updating the displayed QR.
3. **Effect on success:** both directed handshakes (A→B and B→A) are created
   with the usual idempotency; a new pair auto-creates the chat as usual; an
   existing pair never produces a second chat.
4. **The room's income>outcome rule applies** to the mutual endpoint: the
   caller's A→B direction must satisfy the rule. A rejected attempt does not
   consume the code (only successful verification burns it).
5. **QR payload (binding):** the persona QR encodes
   `{INVITE_URL_BASE}?u={userAddress}&m={mutual_code}`. Deep links without a
   valid `m` degrade to a single-direction handshake request (A→B,
   authenticated by the opener's own persona token).
6. **Supersedes** the ruling-3 client-side two-POST routine recorded in
   bleak-web's concept §1c; bleak-web's scanner calls this endpoint instead.

**Consequences for clients:** the badge QR re-renders whenever the code
rotates; the scanner sends `u` + `m` to the new endpoint; scans with a stale
code surface an explicit "code already used" state.

### 7.4 Follow-ups resolved by default (owner may override) — ✅ recorded 2026-11-15

The three remaining details are fixed as follows so implementation can
proceed; each is a default the owner can overturn at any time:

- **Token format (all client-generated tokens — persona, room):** ≥ 32
  random bytes, base64url; sent **raw** at creation/rotation (the server
  hashes once with SHA-256, like a password — raw never stored or logged);
  transport `Authorization: Bearer`. The *mutual-code refresh* is the one
  exception per §7.3: it carries the **hash** (owner's explicit wording).
- **One-time creation-token pool:** minted by an **operator CLI/script**
  (small `cmd/` utility or make target) into a DB table
  (`create_tokens`: hash PK, `created_at`, `used_at`); the raw token is
  printed once. **No authenticated minting endpoint** in this iteration;
  revisit if the host UI needs self-service.
- **Native invite codes (§7.2) semantics:** the first code is minted at
  room creation and returned to the creator; room-token holders can mint
  more and revoke any. Default: **multi-use until expiry** (an event needs
  many attendees), default expiry **24 h** (settable at mint), several
  codes active per room, revocation immediate. Codes are stored **plain**
  (6 digits = a 10⁶ space — hashing adds nothing against brute force);
  join attempts are **rate-limited** per source to blunt enumeration.
  Joining checks: code exists, not revoked, not expired.

**Implementation notes (2026-11-16, WP-6).** The two amendment defaults
flagged by bleak-web were applied as recorded:

- **6.9a applied:** room-scoped per-user reads
  (`GET /api/room/{room}/user/{address}/chat[/{chat}/messages]`) accept the
  **room token** in addition to that persona's token — the host console
  keeps its per-member drill-down.
- **6.9b applied:** codes are globally unique (6-digit primary keys) and a
  small **public resolve endpoint** `GET /api/invite/{code}` →
  `{room}` maps a scanned code to its room (rate-limited per source).
- One-time creation tokens are minted by the operator CLI
  (`make tokenctl` / `bin/tokenctl mint`); the legacy migration is
  `bin/tokenctl migrate` (raw secrets → gitignored `data/legacy-tokens.txt`).

### 7.4 Interests + matching score — ✅ DECIDED (owner directive, 2026-11-17), implemented (feature #34)

**Context.** The product needs a graded affinity between two personas of
one room: how well they match right now, driven by shared interests and
their handshake state (the auto-matchmaking mechanic consumes it).

**Decisions.**

- **Storage.** New `interests` table: `user_address` (FK users), `title`
  (1..64 symbols), `group INTEGER` (the interest category) and
  `key INTEGER` (the concrete interest). `(user_address, key)` is the
  primary key: a persona holds one interest per key; re-adding an existing
  key replaces its title/group.
- **Bulk add** `POST /api/room/{room}/user/{address}/interest` — body
  `{"interests": [{"title", "group", "key"}]}` (1..50 rows), persona-token
  gated, room-scoped; responds 201 with the full resulting list.
- **Bulk delete** `DELETE /api/room/{room}/user/{address}/interest` — body
  `{"keys": [...]}` (1..50 keys, unknown keys ignored = idempotent);
  persona-token gated; responds 200 with the remaining list.
- **Matching score** `POST /api/match` — body
  `{"room_address", "first_user_address", "second_user_address", "time"}`
  where `time` is milliseconds (the formula's t is its hours, float);
  requires the FIRST persona's Bearer token; both users must live in the
  room. Response:
  `{"points", "mutual_handshake", "handshake", "exact_interest_matches",
  "shared_groups"}`.
- **Formula** (owner-specified base): `points =
  clamp(50 + 50·(1/2^t) + coef, 0, 100)` with t = hours (float) from the
  entered milliseconds. Coefficients (owner note, quantified at
  implementation): a one-sided handshake in the room +4; every shared
  interest group +2; every exact interest match (same group AND same key)
  +5. A **mutual handshake returns 100 immediately** (both directed
  handshakes exist between the two in the room), whatever the time and
  interests say; everything is clamped into [0, 100].
- The coefficient values (+4 / +2 / +5) are initial constants
  (`internal/db/interest.go`, `CoefHandshake` / `CoefSameGroup` /
  `CoefSameKey`) and may be retuned without contract changes.

### 7.5 Hard matching, chat done, shared contacts — ✅ DECIDED (owner directive, 2026-11-18), implemented (feature #35)

**Context.** Owner directives: a second match method (the legacy
`POST /api/match` stays unchanged), a chat completion marker, a
summarized once-only hard match and a one-way contact exchange.

**Decisions.**

- **Match v2 (time-independent).** `POST /api/match/v2` — body
  `{"room_address", "first_user_address", "second_user_address"}` (no
  time field), FIRST persona's Bearer token, both users must live in the
  room. Formula: `points = 50 + 50·tanh(coef/12)`, coef identical to the
  legacy method (+4 per handshake direction — no instant 100 here, +2 per
  shared group, +5 per exact match). The score is **always strictly inside
  the open interval (0, 100)**: 0 and 100 are impossible (the value is
  pinned into [0.01, 99.99] after rounding), and the same pair always
  grades to the same value.
- **Difficulty calibration (owner directive, 2026-11-18).** A FULL match
  (> 80 points) must stay out of reach for ~80% of random user pairs.
  Tuned against simulated random populations: at scale 12 a plain mutual
  handshake (coef 8) grades **79.14 — just below the line** — so a
  handshake alone never grants a full match, and the ~one-of-five pairs
  that cross 80 need real shared interests on top. The checkpoint curve
  keeps the same spirit: `hard = 50 + 50·tanh((closed−3)/2)` — an average
  3/5 chat grades the neutral 50, a perfect 5/5 conversation tops out at
  **88.08** instead of an instant 100, so the merged score only crosses 80
  when the soft score is strong as well.
- **Chat `done` marker.** `chats.done` (bool, default false) is stamped
  `true` automatically in the same transaction that saves the final
  farewell turn of the agent dialog; a kickoff clears it; the revival
  sweep skips `done` chats. It is visible on every chat read.
- **Hard match.** `POST /api/hard-match` — body
  `{"room_address", "first_user_address", "second_user_address"}`, FIRST
  persona's Bearer token; both users must live in the room and their chat
  must exist. The chat transcript is summarized by the LLM (deterministic
  heuristic fallback), the networking checklist
  (`introductions`, `shared_topics`, `balanced_turns`, `questions_answered`,
  `warm_farewell`) is graded, and the checkpoint result is merged with the
  v2 score: `points = round2((soft + hard)/2)` with the difficulty-tuned
  `hard = 50 + 50·tanh((closed−3)/2)` (3/5 closed = neutral 50,
  5/5 = 88.08). **Handshakes impact no value** (owner directive,
  2026-11-18): the method is only reachable for pairs with a mutual
  handshake anyway (the chat requires both directions), so the soft score
  entering the merge is driven by shared interests alone. One `summaries`
  record per pair (chat primary key): a repeated call returns the stored
  summary (200) instead of re-summarizing (201 on first creation).
- **Shared contacts.** `contacts` table — `from`, `to`, `content`
  (≤ 255 symbols), `(from, to)` is the unique pair key: one share per
  pair. `POST /api/contact` (FROM persona's token; repeated share = 409),
  `GET /api/contact/{address}` (ONLY the recipient's token lists the
  contacts shared with them).

### 7.6 User active flag, match worker, new match formulas — ✅ DECIDED (owner directive, 2026-11-23), implemented (feature #36)

**Context.** Four owner directives: an `active` flag on personas, a
background worker that matches personas automatically, and new formulas
for both match methods.

**Decisions.**

- **`users.active` (default true).** Every new persona is created active;
  `PUT /api/room/{room}/user/{address}` accepts `active` to opt a persona
  in or out. Inactive personas are invisible to the match worker (they
  neither receive nor send automatic handshakes); everything else (chats,
  messages, manual handshakes) is unaffected.
- **Match worker (`internal/matcher`).** One job every 10 minutes
  (`BLEAK_NODE_MATCHER=0` disables):
  1. fetch every non-deleted room holding at least one active invite code
     (not revoked, not expired) — live events only;
  2. fetch the room's active users;
  3. build the matching-point matrix: for every active user X the score
     against every other active user Y, computed by the match-point
     function of `POST /api/match` (t is read from X's `created_at`);
  4. for every X: skip the users X already shook hands with (a repeated
     directed pair is a no-op), order the rest by points (address breaks
     ties) and send the handshake X → Y for the **top 3**.
  Sending goes through the regular handshake path: idempotent per
  directed pair, the room's **income > outcome rule applies** (a blocked
  send is logged and skipped), and a completed mutual pair auto-creates
  the chat as usual.
- **Match (legacy `POST /api/match`) — new formula (owner directive):**
  `points = 100 * (1 - 1/(C * 2^t))` with `t` the hours (float) since the
  **FIRST persona's creation** (computed server-side; the request no
  longer carries `time`) and `C = 1 + handshake directions + interest
  percent`: +1 per directed handshake between the two in the room (0, 1
  or 2), and the matching-interest percent `2 * exact_matches /
  (interests_first + interests_second)` — every exact match (same group
  AND key) counts on both sides, so identical interest sets grade a full
  100 % percent. A mutual handshake is **no longer an instant 100**; the
  score grows with the first persona's age toward 100.
- **Match v2 (`POST /api/match/v2`) — new formula (owner directive):**
  the pure matching-interest percent, `points = 100 * 2 * exact_matches /
  (interests_first + interests_second)` — no time and no handshake
  component; identical sets grade 100, nothing shared grades 0. The
  `handshake`/`mutual_handshake` response flags stay informational.
- **Hard match unchanged.** The hard match's calibrated soft component
  (`50 + 50·tanh(coef/12)`, feature #35 difficulty tuning) is kept
  internally — only the public v2 endpoint switched to the percent, so
  the documented full-match scarcity of `/api/hard-match` is preserved.

**Consequences for clients:** `/api/match` requests drop the `time`
field (server-side t); `model.User` gains `active` (always present);
match v2 can now return exactly 0 and 100.

---

*Maintained by the concept agent of bleak-web's working stream; owner is the
sole authority over decisions recorded here.*