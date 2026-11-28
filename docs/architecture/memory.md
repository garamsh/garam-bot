# Memory

Governs the source-linked conversation memory store, `internal/memory/` and `internal/memory/sqlite/`. Rationale: ADR 0002, ADR 0006, ADR 0011, ADR 0018, ADR 0040.

The store is kept as the candidate for the bot's memory (ADR 0040). No binary reaches it: the agent that wrote to it and read from it is removed, and the bot-memory interface that will decide its place is not decided yet (#1207). What follows describes the store as it stands, and what its callers would have to supply.

## Current decisions

- The store is one SQLite file, opened by `sqlite.OpenConversation` or, to refuse a file that is absent or uninitialized, `sqlite.OpenInitializedConversation`. It declares its own `Store` and sentinels. The types it takes and returns — `SourceMessage`, `Synthesis`, `ConversationHit`, and the `Embedder` and `ConversationSummarizer` its callers supply — are `internal/memory/`'s, a package that imports nothing, so a caller reaches the store without reaching an agent.
- A caller records the visible messages of a completed exchange: each original inbound message and each reply. An inbound message is written with its external ID, the scope that ID is unique within — a verified issuer and subject, or an empty issuer and the transport principal — and the poster. System instructions, tool results, hidden reasoning and credentials are outside the durable source boundary. Credential patterns are redacted before persistence; a redacted original cannot be recovered verbatim (ADR 0006).
- A full batch of four completed visible messages produces one immutable mid episode, synthesized by the caller's summarizer independently of other episodes. The same synthesis call names the entities the batch mentions. Partial batches stay durably pending (ADR 0006).
- Synthesis makes up to three calls a batch. An answer that is not usable is asked again; a model error is not. After the third unusable answer synthesis fails with that answer's error and the batch stays pending.
- Source capture commits before synthesis. Each mid episode commits its direct source interval atomically; the end of the latest committed interval is the processing checkpoint. Failed synthesis leaves pending sources for the next record call to retry, without holding a write transaction across the model call. Each episode's entity names, normalized, commit with it as its links. Each episode's summary embedding is stored with it once it commits; a failed embedding is retried the same way, and the episode is left out of similarity ranking until then (ADR 0011, ADR 0002).
- Retrieval ranks by cosine similarity between a query's embedding and each summary's, with no threshold, and by a personalized PageRank walk at damping 0.5 over the episode-entity links, seeded on the stored names the query contains whole. Source text is reached by ID through an episode's inclusive source interval. A summary is a retrieval hint, not verified evidence; across episodes, chronology and original evidence distinguish a current claim from a historical one (ADR 0018).
- The store keeps one note of at most 2,000 characters, replaced whole by its caller. It is unlinked to sources: it states what the caller keeps, not verified evidence (ADR 0011).
- One process on a host writes the file. The store takes an exclusive, non-blocking `flock` on `<resolved path>.lock` before it opens the database, resolving relative, `..` and symlinked spellings to one real path, and releases it after the database closes or when the process exits. A second opener is refused with `ErrStoreLocked`. The lock covers one host only (ADR 0002).
- A file an earlier release opened may still contain the addressed tables of the first design; a new file does not. The store refuses a file whose addressed tables hold rows, with `ErrAddressedStore`; it does not reinterpret or delete them (ADR 0002).

## Existing databases

- A source-linked database from before embeddings, before entities or before the note opens in place and gains what it lacks. The first record call embeds every stored episode, and one episode a record call is linked, so a pass adds one synthesis call at most. Stored embeddings belong to the model that wrote them: the store refuses a file whose vectors differ in length from the configured model's, and a different model of the same length is not detected, so one embedding model belongs to one file.
- Populated extractive source-linked databases are refused before modification: their one-source summaries are not reinterpreted as episodic summaries. Empty extractive schemas upgrade automatically.
- To begin episodic memory without rewriting old evidence, use a different, empty path and keep the original file. Back up with SQLite's backup facility or while no process is writing; copying only the main file during an active WAL write is not a backup.

## Open questions

- Where the store lives once the bot-memory interface is decided, and which of its contracts survive, is the owner's (#1207). Until then no binary links it, and its tests are the only thing that runs it.
- Retrieval reads every embedded episode and every entity link and ranks and walks them in process, so its cost grows linearly with stored episodes, one per four messages; no index bounds it.
- The walk seeds only on stored names the query contains exactly; a query naming an entity differently reaches nothing through it.
- Synthesis can omit facts or mishandle corrections; an episode summary is model output, not verified truth. Credential redaction recognizes common labelled and bearer forms, not every possible secret syntax.
