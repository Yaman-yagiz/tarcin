# Event pipeline and concurrency

This document specifies how data moves between the network, the
database and the UI, and which guarantees hold at each step. It is the
direct answer to the freezes of earlier terminal clients such as
[whatscli](https://github.com/normen/whatscli) in busy groups. It relies
on whatsmeow's event dispatch model: one node at a time, with handlers
called synchronously.

## 1. Guarantees

| # | Guarantee | Mechanism |
|---|---|---|
| G1 | **No message is acknowledged before it is in our database.** | Message handlers wait for the writer to commit. `SynchronousAck` and `AddEventHandlerWithSuccessStatus` withhold the receipt if persisting fails, so the server redelivers. `EnableDecryptedEventBuffer` keeps a redelivery decryptable. |
| G2 | **Duplicates and reordering are harmless.** | Every write is an idempotent upsert keyed by natural IDs. Edits, reactions, revokes and receipts are stored keyed by their *target*, so they apply correctly whichever arrives first. |
| G3 | **The UI never blocks on the network or the disk.** | The UI receives only coalesced messages, and all I/O runs in `tea.Cmd`s or workers. |
| G4 | **The whatsmeow dispatch goroutine never waits on the UI.** | Handlers only normalise the event and submit it to the writer. The UI is notified asynchronously after commit. |
| G5 | **Memory is bounded.** | Every queue has a fixed capacity, and UI notifications collapse into "reload" markers on overflow. |
| G6 | **The database is the source of truth.** | UI notifications are hints. Dropping one never loses data, because the UI can always reload from the DB. |

## 2. Overview

```text
                 whatsmeow (one node at a time)
                        │ events (handler goroutines)
                        ▼
              ┌───────────────────────┐
              │ wa.Adapter            │  normalise → domain events
              │  - resolve JIDs (LID) │  (pure, cheap, no network)
              └──────────┬────────────┘
         critical        │            ephemeral (typing, presence,
   (messages, receipts,  │             connection state)
    edits, group info)   │                      │
                         ▼                      │
              ┌───────────────────────┐         │
              │ store.Writer          │         │
              │  live queue  (cap 1k) │◄─ priority
              │  bulk queue  (cap 16) │◄─ history sync chunks
              │  one write conn       │         │
              │  batched transactions │         │
              └──────────┬────────────┘         │
                         │ committed changes    │
                         ▼                      ▼
              ┌──────────────────────────────────────┐
              │ ui.Bridge                            │
              │  dirty-chat set + bounded per-chat   │
              │  message buffer + latest ephemerals  │
              │  armed timer (50 ms) → one Send      │
              └──────────────────┬───────────────────┘
                                 │ tea.Msg (batch)
                                 ▼
              ┌──────────────────────────────────────┐
              │ Bubble Tea program (single owner of  │
              │ UI state; View is O(visible rows))   │
              └──────────────────┬───────────────────┘
                                 │ tea.Cmd (async)
                                 ▼
              read pool (DB)  ·  service ops (send, mark read, …)
              · jobs (group metadata, media, on-demand history)
```

## 3. Components

### 3.1 `wa.Adapter`

- Owns the `whatsmeow.Client`. It sets these client options: `SynchronousAck`,
  `EnableDecryptedEventBuffer`, auto-reconnect with a capped backoff
  through `AutoReconnectHook`, and `AutomaticMessageRerequestFromPhone`.
  whatsmeow's backoff grows by two seconds per failed attempt without
  bound; the hook caps it at 30 seconds. `UseRetryMessageStore` keeps
  sent messages in `session.db`, so that a recipient's retry request can
  be answered after a restart.
- A panic in a handler withholds the receipt. whatsmeow recovers handler
  panics but would then acknowledge the event, losing it.
- **Linking.** A device that is not linked yet is linked inside `Run`,
  after the handler is registered: the phone starts the history sync as
  soon as the link is made, and whatsmeow acknowledges an event that no
  handler waits for. QR codes and the other link steps reach the UI as
  ephemeral `LinkProgress` events; the account itself is stored from a
  durable `AccountLinked` event, which marks its identity as mine.
- **Message handlers**, registered with
  `AddEventHandlerWithSuccessStatus`:
  1. Normalise the event into a `domain.Message`: extract text, kind,
     quoted ID, mentions, media descriptor and timestamps.
  2. Resolve the chat and sender to canonical identities (LID-first, see
     [data model](data-model.md) §2). This uses only local lookups and
     never the network.
  3. Call `writer.SubmitLive(ctx, ops)` and wait for the commit,
     **bounded by a 5 s timeout**.
  4. Return `true` if the commit succeeded. Otherwise return `false`, so
     that no receipt is sent and the server redelivers.
- **Normalisation** (`internal/wa/normalize.go`) is pure: no network and
  no store lookups. Text stays raw until it is rendered.

  | whatsmeow | domain |
  |---|---|
  | text, extended text, image, video, voice, document, sticker, location, contact, poll | `MessageReceived` with the kind, the text or caption (a description for locations, contacts and polls), the media descriptor, the quoted ID, and the forwarded, view-once, ephemeral and mentions-me flags; non-text messages keep their protobuf as `raw` |
  | an unknown message kind | `MessageReceived` of kind `unsupported`, with `raw` |
  | `UndecryptableMessage` | an `unsupported` placeholder, replaced when the message is delivered again |
  | `ProtocolMessage` edit / revoke / ephemeral setting | `MessageEdited` / `MessageRevoked` / `ChatUpdated` |
  | `ReactionMessage` | `ReactionReceived` (an empty emoji removes) |
  | statuses (`status@broadcast`), pins, poll votes, other protocol messages | nothing; acknowledged |
  | `JoinedGroup` | `ChatUpdated` (name, disappearing timer) and the full member list, each member with both address forms |
  | `GroupInfo` | `ChatUpdated` for a new name or timer; members who joined, left, or were promoted or demoted |
  | app state: `Contact`, `PushName`, `BusinessName` | `ContactUpdated`, with `IdentityLinked` when both address forms are named |
  | app state: `Pin`, `Mute`, `Archive` | `ChatUpdated` (a mute end of -1 means forever) |
  | app state: `MarkChatAsRead` | `ChatRead`, or an unread count of one for "mark as unread" |

  `EmitAppStateEventsOnFullSync` is set, so a full app-state sync arrives
  as one event per item, like a later change. Deleting or clearing a chat
  on another device is handled like `:delete-local`.

  When a collection's patches no longer verify (`AppStateSyncError`, for
  example a mismatching LTHash after a missed patch), whatsmeow only
  reports it and the collection stops syncing. The adapter then drops
  the local copy and syncs the collection in full, off the event
  handler; the store applies the repeated items idempotently. A failed
  full sync is logged and not retried, and a collection is synced in
  full at most once every ten minutes.

  A message sent to a broadcast list belongs to the direct chat with its
  sender.
- **Identity resolution** (`internal/wa/identity.go`) adds the second
  address form of a person wherever whatsmeow knows it: in the event
  (`SenderAlt`, `RecipientAlt`, group participants) or in its LID map, a
  local table in `session.db` with an in-memory cache. A direct chat's
  peer gets an `IdentityLinked` event ahead of the message, so the store
  merges before it files the message. The network is never asked.
- **History sync** (`internal/wa/history.go`). By default whatsmeow
  downloads a sync itself, ignores whether the handler stored it, and
  deletes it from the server right after dispatch: quitting during an
  import, or a full disk, would lose that history for good. tarcin sets
  `ManualHistorySyncDownload` instead:
  1. The notification arrives as a protocol message from this account's
     phone (from anyone else it is forged and ignored). The handler stores
     it as a durable `HistoryQueued` event before it is acknowledged.
  2. A worker downloads the sync and converts it: PN↔LID mappings and
     push names first, then each conversation's settings (unread count,
     pin, archive, mute, disappearing timer, group name and members,
     end of history) with its messages, in chunks of at most 500
     messages. Each chunk goes to the bulk queue and waits for its
     commit; one that fails is retried with a backoff.
  3. Only when every chunk is stored is the sync deleted from the server
     and removed from the queue (`HistoryDone`). A sync the server no
     longer has is dropped.
  4. After a restart, the syncs still queued (`Reader.PendingHistory`)
     are handed back to the adapter (`Adapter.Resume`). Writes are
     idempotent, so a sync cut short is simply written again.
  5. History is never notified to the user as "new".
- **Ephemeral events** (typing, presence, connection state) go straight
  to `ui.Bridge`. They are never persisted, and only the latest value
  per key is kept.
- **Forbidden in handlers:**
  - network calls such as `GetGroupInfo` (these are queued as jobs)
  - waiting on the UI
  - unbounded allocation
  - `RemoveEventHandler`, which deadlocks
- Handlers must be **thread-safe**, because whatsmeow calls them from
  several goroutines.

### 3.2 `store.Writer`: single writer

- Owns the **only** write connection to the message DB. Readers use a
  separate read-only pool (see [data model](data-model.md) §5).
- Loop:
  1. Always drain the **live** queue first.
  2. Take at most **one bulk chunk** between live batches, so that live
     latency stays low during a history sync.
- **Batching (group commit):** a live transaction
  (`BEGIN IMMEDIATE … COMMIT`) starts with the first queued submission
  and takes in every submission that queues up while it is open, up to
  256 events or 10 ms of filling, whichever comes first. The fill window
  starts after the first submission is applied, so on a slow disk the
  submissions that queued up behind it still share the transaction. The
  writer never waits to fill a batch, so a single message on an idle
  writer commits at once. Callers waiting on a batch are released when it commits. A bulk
  chunk is always a transaction of its own.
- **Isolation:** each submission runs in its own savepoint. A submission
  whose events fail (a constraint, malformed data) is rolled back alone
  and gets its error; the rest of the batch commits. Errors of the
  transaction itself fail every submission in it.
- **Cancellation:** a submitter's context never reaches a SQL call,
  because SQLite rolls back the whole transaction when a write statement
  is interrupted. A submission whose context has ended before it is
  applied is skipped. One that ends later still commits, and only the
  caller stops waiting; the redelivery that follows is idempotent.
- **Backpressure:**
  - If the live queue is full, `SubmitLive` blocks until there is room
    or its context expires. That pushes back on whatsmeow, which is
    correct: we would rather slow decryption than drop or lose messages.
  - If the bulk queue is full, the history handler waits.
  - Neither queue is ever silently drained or dropped.
- After each commit, the writer publishes a `Changes` value to
  `ui.Bridge` without blocking. `Changes` lists the new and updated
  messages per chat (each in its latest version, up to 200 per chat;
  beyond that the chat gets a reload marker), the chats whose chat-list
  entry changed and the people whose names changed. Nothing from a
  failed submission is listed.
- **Errors:**
  - `SQLITE_BUSY` is retried with jitter, for at most the busy timeout
    (5 s) in total. The connection's busy handler already waits inside
    each attempt, so retries matter only for the cases SQLite reports
    without waiting.
  - `SQLITE_FULL` (disk full) moves the app into a degraded state with a
    UI banner. Receipts are withheld, so the server keeps the messages.
    The writer keeps trying and reports recovery on the next commit.
  - Corruption is reported and the app refuses to run, pointing to the
    recovery docs. The writer fails every later submission and never
    writes again. We never run blind repairs.
- The writer also runs `PRAGMA optimize` at start (`0x10002`), hourly
  and at close, on its own connection.
- On shutdown it stops accepting submissions and commits what is queued,
  live first. When the drain deadline passes, the remaining submissions
  fail and the transaction in progress is the last one.

### 3.3 `ui.Bridge`: coalescing

The bridge is the boundary between backend speed and UI speed.

- Pending state:
  - a set of dirty chats
  - for each chat, a buffer of new or updated rows (cap 200)
  - the latest value of each ephemeral key
  - the connection state
- On the first change after an idle period, the bridge arms a timer
  (50 ms by default). When the timer fires, it swaps out the pending
  state and calls `program.Send(BatchMsg)` **once**.
  - Key presses never wait for this timer; only backend updates do.
  - The interval was chosen by measurement. Under scenario S5, 50 ms
    halves the CPU that Bubble Tea's renderer spends re-diffing frames,
    compared with 16 ms, with unchanged key latency.
  - When nothing happens, no timer runs, which keeps idle CPU near 0
    (budget B8).
  - `Send` blocks until the UI receives the message. Only the bridge
    goroutine waits on it; backend goroutines never do.
- **Overflow:** if a chat's buffer exceeds its cap, the buffer is replaced
  with a `ReloadChat(id)` marker. The UI then reloads that chat's visible
  window from the DB. Memory stays bounded by the number of chats times
  the cap.

### 3.4 Service operations (UI → backend)

- The UI calls the service only through `tea.Cmd` functions. Each
  operation takes a `context.Context` with a deadline and returns its
  result as a `tea.Msg`.
- **Sending a message** (`internal/app/sender.go`):
  1. The message joins its chat's queue at once, from `Update`, with a
     locally generated ID and timestamp. Queuing does no I/O, so the order
     of the queue is the order of typing.
  2. A worker per chat, with at most 4 sends in flight across all chats,
     stores the message as `pending` through the writer (the UI shows it
     immediately) and calls `SendMessage`, with a 60 s deadline.
  3. The outcome is recorded: `sent` moves the message to the time the
     server stamped it with (`MessageSent`), by which every client orders
     messages, or `failed` comes with a notice that says how to retry.
     A computer whose clock is off would otherwise place the replies to
     a message before it. tarcin also estimates the server's clock from
     the sends' answers, and raises the estimate when a live message is
     stamped later than it arrived, never lowering it from one, since a
     message from an offline queue arrives long after its stamp. A new
     message is stamped with that clock while pending, and reads, edit
     windows, expiry and own reactions use it too. Offsets under two
     seconds are not corrected.
  4. **tarcin never re-sends automatically**, because that could
     duplicate a message the server already accepted. The user retries a
     failed message (`ctrl+r` on it); a retry keeps the message ID, so a
     recipient who did get the first attempt sees the message once.
  5. On shutdown, a send in flight is cut off and queued messages are
     stored as `failed` without being sent, so none is lost. At the next
     start, own messages left `pending` (`SendsInterrupted`) become
     `failed` too.
  6. **Edits and deletions** of sent messages go to the backend first
     and are stored once it accepted them, as `MessageEdited` and
     `MessageRevoked` events with this account as the actor, so the
     applier's rules (only the sender edits; the sender or a group
     admin deletes) apply to them as to every other. The app checks the
     windows WhatsApp enforces: 20 minutes for an edit, two days for a
     deletion for everyone, and the admin role for someone else's
     message.
  7. An **attachment** is stored as a pending media message and copied
     into the media cache before it is sent; `UploadReader` streams the
     encrypted copy through a temporary file in the cache, never holding
     the file in memory. Once sent, the protocol message, with its media
     key, is stored like a received one's.
  8. In a chat with **disappearing messages**, a message is sent with the
     chat's timer in its context, as WhatsApp's clients do, and stored
     with its expiry. Every five minutes, and at start, the app deletes
     the messages whose time is up (`MessagesExpired`) and then their
     media files. Like a deleted or cleared chat and a message deleted
     for everyone, the commit itself names the cache files no stored
     message uses any more (`Changes.Unlinked`), read in the same
     transaction, and the notifier deletes them. Files a commit named
     just before tarcin stopped go at the next start, which deletes
     every cache file no message uses before any download can begin.
  9. A **reply** is stored with the quoted message's ID and sent as
     extended text whose context names that message, its sender's
     canonical JID and its text, for the preview recipients see. A quote
     that is not stored is sent by ID alone. A retry sends the same
     quote.
  10. A **forward** joins the target chat's queue like a typed message.
      Its worker reads the source and stores the copy as pending, marked
      forwarded, with the source's text, media descriptor and protocol
      message; a source already in the media cache shares its file. The
      backend sends the source's protocol message with a new context
      (forwarded, one more forward counted, the target's timer), so
      media is not uploaded again. Deleted, view-once and unknown kinds
      of messages are refused before anything is stored. A retry forwards
      the stored copy again.
- **Mark as read:** opening a chat, and each new message while it stays
  open, records a `ChatRead` at once. While the terminal lacks focus
  (focus reporting, DECSET 1004), the open chat's new messages stay
  unread, as in the official clients; the chat is read when focus
  returns, or at the next key press. If the privacy setting
  `privacy.read_receipts` is on, the unread incoming messages (from the
  per-message unread flag) are then reported to their senders, one
  request per sender as groups require, off the UI goroutine. While
  offline, the receipts are not sent; only the other side's ticks miss
  them.
- **Receipts from others** move own messages to delivered, read or
  played: in a direct chat at once, in a group once every member has
  (the `receipts` table, [data model](data-model.md) §3). A receipt for
  someone else's message changes nothing.
- **Reads on this account's other devices** read the chat here too. The
  phone reports them as a "read" or "played" receipt from this account,
  or as "read-self" when the account has read receipts off.

### 3.5 Jobs: bounded background work

- A small worker pool (`internal/jobs`) with:
  - a concurrency limit (default 4 for network jobs, 2 for media)
  - `singleflight` deduplication by job key
  - a token-bucket rate limit for protocol requests, to stay within
    account-safety limits
  - per-job deadlines, and capped exponential backoff with jitter on
    retries
- Defaults: a queue of 256 jobs beyond the workers, a 30 s deadline per
  attempt, three retries after 1 s, 2 s and 4 s (±20 %, at most a
  minute). A full queue refuses new jobs instead of blocking: they can be
  derived again. A job that panics fails without retries; the pool
  survives it.
- Callers that ask for a job already queued or running under the same
  key wait for that one and share its result; `Do` returns early when
  the caller's context ends, while the job goes on for the others.
- Job kinds:
  - `group.refresh(jid)`: runs when a group is opened, or when a
    `GroupInfo` event changes its members, if its full member list is
    missing or older than 24 h; otherwise it returns at once. **Never
    for all groups at startup.** It asks the server once
    (`GetGroupInfo`) and stores the title, the description and settings
    (`GroupMetaUpdated`) and the complete member list. A group the
    account left fails without retries. The network pool runs four
    jobs at once and starts about one a second (bursts of three).
  - `media.download(msg)`: started by the user, or automatically for
    small media if that is enabled.
  - `history.request(chat, oldest)`: on-demand history when the user
    moves up past the oldest local message. 50 messages per request,
    one request per oldest message. The request is a peer message to
    this account's phone (`BuildHistorySyncRequest`); the phone answers
    with an `ON_DEMAND` history sync, which is stored like every other
    (§3.1), without the conversation's chat state, which such an answer
    does not carry. A conversation in it without messages, or with the
    end-of-history marker, marks the chat's history complete, and no
    more requests are made for it.
  - `avatar.fetch(jid)` (planned).
- Jobs are **not persisted** in the MVP. Losing a queued job is
  harmless, because every job can be re-derived from DB state. A durable
  `jobs` table is added only if a concrete need appears (see
  [ADR-0006](../adr/0006-in-process-event-pipeline.md)).

## 4. Lifecycle

1. **Start**
   1. Resolve the XDG paths, create the private directories, tighten
      any mode other users could use and warn about it
      ([ADR-0008](../adr/0008-xdg-paths-and-file-permissions.md)), and
      take the per-profile **single-instance lock** (`flock` on
      `$XDG_DATA_HOME/tarcin/profiles/<profile>/lock`).
   2. Open the DBs and run migrations: `session.db` (whatsmeow's own
      migrations) first, then `tarcin.db`. Hand the history syncs still
      queued in `tarcin.db` back to the adapter.
   3. Start the UI from cached DB state.
   4. *Then* connect in the background. The UI is usable offline from the
      first frame (budget B1). A device that is not linked yet waits for
      the user to accept the first-run notice ([UI](ui.md) §11) before it
      connects at all.
2. **Run**
   - The connection state is shown in the status bar: online,
     connecting, offline, or "the connection is not responding" after a
     missed keepalive.
   - `StreamReplaced`, `TemporaryBan` (with its end), `ClientOutdated`
     and `LoggedOut` stop reconnecting and show an explanation in the
     warning colour. tarcin
     then never reconnects on its own; `:reconnect` tries again, except
     after a logout.
   - While offline, a netlink route socket reports changes of links,
     addresses and routes (a cable, Wi-Fi, a resume), and tarcin
     reconnects at once instead of waiting out the backoff (budget B10).
     It costs no network traffic. `:reconnect` does the same by hand, and
     restarts a connection that stopped answering.
3. **Shutdown**, on SIGINT, SIGTERM, SIGHUP or the quit key:
   1. Cancel the root context.
   2. Stop accepting UI commands.
   3. Disconnect whatsmeow so that no new events arrive.
   4. Drain the writer, with a 5 s deadline.
   5. Store queued sends as failed, run `PRAGMA optimize` and close
      `tarcin.db`, then `session.db`.
   6. Release the lock.
   7. Restore the terminal.

## 5. Goroutine inventory

| Goroutine | Count | Blocks on |
|---|---|---|
| Bubble Tea event loop | 1 | Input and messages only |
| whatsmeow internals | library-managed | The network; our handlers (bounded by the commit wait) |
| `store.Writer` | 1 | The disk |
| `ui.Bridge` | 1 | `program.Send` |
| Read queries (`tea.Cmd`) | ≤ read-pool size at a time | The disk |
| Senders | ≤ 4 | The network |
| Job workers | ≤ 4 network + 2 media | The network and the disk |

The goroutine count is exported in debug metrics. A test asserts that it
returns to the baseline after load (no leaks).

## 6. Testing the pipeline

- The `wa.Adapter` sits behind a `Backend` interface. A **fake backend**
  replays scripted or randomised event streams, used for the S5, S6, S8
  and S9 scenarios in [performance](performance.md).
- Property tests: for any shuffled and duplicated event sequence, the
  final DB state equals the state produced by the canonical ordering
  (G2).
- Crash test: kill the process during a write burst, restart it, and
  verify that every acknowledged message is present (G1) and that
  `PRAGMA integrity_check` passes.
