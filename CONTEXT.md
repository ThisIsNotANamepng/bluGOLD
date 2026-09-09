# CONTEXT.md

Notes for future agents and contributors working on **bluGOLD** — a toy but
technically-real proof-of-work cryptocurrency for the UWEC cybersecurity club.
Read this before changing consensus or networking code.

## What this is

- Go 1.24 module `blugold`, **zero external dependencies** (pure stdlib).
  Keep it that way unless there is a strong reason.
- One binary (`cmd/blugold`) that does everything: node, miner, wallet, CLI client.
- Not for real value. Simplicity and readability are explicitly preferred over
  robustness features a serious coin would need.

## Package layout & dependency direction

```
crypto  →  chain  →  state  →  wire  →  p2p  →  node  →  miner / api  →  cmd/blugold
                                              ↘ store ↗
```

| Package | Owns |
|---|---|
| `internal/crypto` | ed25519 wallets, `blu1...` addresses (base32 + 4-hex checksum), sign/verify |
| `internal/chain` | consensus primitives: Hash, Params, Tx, Block, merkle, PoW, difficulty, amount formatting |
| `internal/state` | account balances/nonces, tx validation, block application |
| `internal/wire` | p2p message types + length-prefixed JSON framing |
| `internal/p2p` | TCP switch, version handshake, peer management, gossip |
| `internal/store` | `blocks.jsonl` append-only storage, `peers.json`, wallet path |
| `internal/node` | block tree, fork choice, mempool, sync, tx flow, tip events |
| `internal/miner` | threaded PoW mining loop against a node |
| `internal/api` | localhost HTTP API for the CLI (send/balance/info/blocks/peers) |
| `internal/itest` | multi-node integration tests over real loopback TCP |

Do not introduce cycles: `node` must not be imported by `p2p`/`store`/`chain`;
`miner`/`api` talk to `node` only through its exported methods.

## Core design decisions (and why)

1. **Account model, not UTXO.** Balances + per-sender nonce. Simpler to
   implement, reason about, and query. Nonce rule: incoming tx must have
   `nonce == account.Nonce`, which increments after each sent tx (first tx = 0).
2. **ed25519, not secp256k1.** In the stdlib, zero deps. PubKey is embedded in
   txs and must derive `From` — the address binds the key.
3. **All timestamps are unix MILLISECONDS.** Block `Time`, `Params.GenesisTime`,
   `TargetBlockTime`, retarget math, CLI display. Seconds granularity made
   test mining impossible (many blocks per second violate `Time > parent.Time`).
4. **Continuous difficulty, not leading-zero-bits.** `target = (2^256-1)/difficulty`
   with difficulty as a plain uint64 — fine-grained retargeting without big jumps.
5. **Retarget rule** (every 20 blocks): `d = d * expected/actual`, clamped to
   [1/4, 4], floor 1. Fast blocks → harder. The direction was once inverted in
   code; the unit test `TestNextDifficultyClamp` pins the correct semantics.
6. **Coinbase is applied LAST** in `state.ApplyBlock`, after regular txs, so a
   miner cannot spend its own same-block reward. Coinbase must pay exactly
   `reward(height) + sum(fees)` — inflation check.
7. **Heaviest-chain fork choice** by cumulative difficulty; ties break toward
   the lexically smaller tip hash. Ties are real (equal-difficulty forks), so
   tests must never assume "same-length fork loses".
8. **Deferred state validation for forks.** Insert only checks structure + PoW
   + difficulty. State validity is proven when a branch *wins* (replay from the
   last checkpoint). A failing replay deletes the subtree and records the hash
   in `rejected` (prevents gossip loops).
9. **Checkpoints every 100 blocks**, keyed by block *hash* (not height — height
   keys go stale across reorgs). Pruned to the active chain on every switch so
   reorg replay never starts from genesis.
10. **Different `Params` → different genesis hash → incompatible networks.**
    This is a feature (accidental isolation), used heavily by tests.
11. **JSON wire protocol** (length-prefixed frames) rather than binary/gob —
    debuggable with netcat, which suits a security club.
12. **No fees required.** Zero-fee txs are valid; mempool sorts candidates by
    fee then time, capped at 10k txs.
13. **Economics tuned for a club, not a nation-state.** 1 BLG per block,
    6-minute target → a solo miner averages exactly 10 BLG/hour once
    difficulty converges. Halving every 10,080 blocks (~6 weeks), total
    supply 20,159.99879040 BLG (~20,160), fully mined in ~3.1 years. Block
    rate is fixed by retargeting regardless of hashrate — "more compute =
    more coins" means a larger *share*, not a faster schedule.
14. **Retarget clamp is a feature and a hazard.** ±4× per 20-block window
    keeps retargets responsive, but a sudden hashrate burst mines blocks
    faster than target until the window completes (documented in README).

## Invariants — do not change without a hard fork

- Genesis constants (`Params.DefaultParams`), tx/block binary encodings
  (`codec.go`), the address format, the signing bytes layout, and the
  difficulty/retarget/reward formulas. Any change forks the network.
- `chain.TestParams()` sets `BlocksPerRetarget=1000` so difficulty stays 1 in
  tests (no retarget within test heights). Integration tests rely on this for
  speed. Live retarget math is covered by unit tests only.
- `n.nowMs()` time source; candidate blocks must have `Time > tip.Time` —
  `BuildCandidate` bumps `now = tip.Time + 1` because at difficulty 1 blocks
  complete in <1ms.

## Node internals (internal/node)

- Single `sync.Mutex` (`n.mu`) guards the whole block tree, state, mempool.
  Public methods lock; internal `*Locked` helpers assume the lock is held.
- Boot sequence: open store → load `blocks.jsonl` (or write genesis if empty)
  → insert all (fromDisk=true, no announcements) → `selectTipLocked` once.
  The `booting` flag suppresses gossip/events during load.
- `pendingState` = confirmed state + all mempool txs applied, cached with a
  dirty flag. **It must be invalidated on every tip change** (`pendingDirty = true`),
  not only when txs are dropped — a stale cache once caused "insufficient
  funds" errors on a funded wallet (see Gotchas).
- Reorg flow (`switchChainLocked`): fast path when the new block extends the
  tip (clone state, apply, commit); slow path when not — replay from the
  deepest checkpoint on the new path, collect txs from abandoned blocks, and
  `rebuildMempoolLocked` re-validates old-mempool + rolled-back txs in time order.
- **Mempool must be revalidated on every tip change**, not just when txs are
  dropped: `dropConfirmedLocked` calls `revalidateMempoolLocked` (sequential
  re-application to the new state), and `BuildCandidate` additionally applies
  filters txs in fee order before including them. Without both, a stale
  mempool tx (e.g. same sender/nonce confirmed in a competing block) would be
  baked into every fresh candidate and the miner would stall forever on
  rejected blocks (`TestStaleMempoolTx` pins this).
- **Sync is locator-based, never height-based.** `getblocks` carries a block
  locator (tip, then ancestors with doubling gaps, genesis last) and the peer
  answers from the deepest entry it shares on its *active* chain. Asking for
  "blocks above my tip height" only works when your chain is a prefix of the
  peer's; across a fork every answer is unconnectable orphans (see Gotchas).
  Three rules keep it converging: ask every peer on connect and every 10s
  regardless of announced height (height is not work); continue a multi-batch
  download from the **last block of the previous batch**, not from the tip
  (the tip does not move until the new branch outweighs the old one); and
  answer an orphan `newblock` with a locator request, which is what makes two
  long-diverged mining nodes discover each other's history.
- Block dedup is the `blocks` tree plus the `rejected` set — there is no
  "seen blocks" map. One existed and was set *before* insertion, so a block
  dropped because the orphan buffer was full could never be reconsidered.
- Announcements: `insertBlockLocked` broadcasts accepted blocks (except to the
  sender) *inside* the lock; `p2p.Switch.Broadcast` spawns a goroutine per
  peer so a slow peer cannot stall the node. Tip-change events are published
  non-blocking (channel per subscriber, buffered 4). Blocks arriving in a
  `blocks` sync response are not re-announced: they were requested, and peers
  poll for themselves.
- Dedup: `seenTx` map, reset wholesale past 50k entries.
- Orphans: buffered keyed by PrevHash, cap 100, promoted recursively on parent arrival.
- **Mempool sync on connect.** `onPeerConnect` sends `getmempool` right after
  the initial `getblocks`. The peer replies `mempool` with its pending txs
  (capped at `mempoolReplyBudget`, same reasoning as `syncTxBudget`), fed back
  through `AddTx` on receipt. Without this, flood gossip alone means a node
  that connects after a tx was broadcast never sees it until the next block —
  no test caught this because every itest network was fully connected before
  any tx was sent.
- `n.hashrate` (set by `SetHashrateSource`, read by `Info()`) is guarded by
  `n.mu` like every other field — it is set from `main()` shortly after
  `node.New` returns, concurrently with the API server goroutine already
  calling `Info()`. A plain field would race the moment a request lands
  before `SetHashrateSource` runs (see Gotchas).

## P2P details (internal/p2p)

- Frame = 4-byte big-endian length + JSON `{"type","payload"}`, max 8 MiB.
- Handshake: both sides send `version` immediately; the first message from an
  unhandshaked peer must be `version`; mismatched `Protocol` or self-claimed
  address drops the conn.
- **Self-connection guard must compare both** the configured advertise address
  AND the actually bound listener address. With `--p2p :0` (tests) the
  configured advertise is `:0` and matches every local node.
- If the user did not set an explicit advertise address, `Start()` replaces
  `Advertise` with the bound `ln.Addr().String()`.
- Dial loop: immediate first pass, then every 3s tops up peers (max 32).
  Learned peers are persisted through `SetPeerPersister` → `store.SavePeers`.
- **Peers gossip**: `peersLoop` broadcasts known addrs (cap 64) every 60s;
  inbound `peers` messages are intercepted in the read loop (never forwarded
  to node handlers) and merged into the known set. Without this, star
  topologies through one seed never mesh.
- A peer that fails `register` (duplicate advertised address, duplicate dial
  key, or shutdown) is **dropped**, not left connected.
- `Peer.Height` is written at handshake by the read goroutine; read it only
  through `Peer.snapshot()` (mutex-guarded) — a bare field read was a data
  race. It is a stale, work-blind diagnostic: `BestPeerHeight` exists for
  dashboards, and sync must not gate on it (that was the fork bug).
- `MaxPeers <= 0` must never reach the switch: `full()` would be instantly
  true and both accept and dial would do nothing. `node.New` only overrides
  the default (32) with positive values.
- Seeds are filtered for self-addresses (listen/advertise/bound) at
  construction; a self-seed otherwise causes a dial/drop churn loop.

## Miner details (internal/miner)

- Shared `atomic.Uint64` nonce across CPU workers; worker id in `ExtraNonce`
  guarantees unique headers. Per-instance hashrate counter (1s window),
  backend-agnostic (`m.total`/`m.rate` are fed by both the CPU and GPU paths).
- Loop: `SubscribeTips()` → `BuildCandidate` → search (CPU workers race, or
  one GPU dispatch loop) → submit or bail on tip change. Node tips channel
  drives restarts.
- At difficulty 1 mining is a firehose (microseconds/block). Integration tests
  throttle with `minPause` (e.g. 150ms). A real network ramps difficulty via
  retargeting within a minute.
- **GPU backend (`-tags gpu`)**: `gpu_opencl.go` is bluGOLD's one intentional
  exception to "zero dependencies, pure stdlib" (core design decision #1) —
  it needs cgo and an OpenCL SDK on the *build* machine, so it's excluded
  from the default build entirely (`gpu_stub.go` stands in with `!gpu`) and
  only compiled in when a contributor opts in. Runtime behavior degrades
  gracefully with no GPU present: forced `-backend gpu` logs a warning and
  falls back to CPU; `-backend auto` benchmarks both (`resolveBackend` in
  `miner.go`) and just picks CPU.
- **Midstate optimization**: `Block.HeaderBytes()`'s only variable-length
  field is the miner address, which is always exactly 40 bytes for a valid
  (non-coinbase) address — so a candidate's header is always the same total
  length, and every field except `Nonce` (the trailing 8 bytes) is identical
  across every nonce trial in one mining round. `sha256mid.go` exploits this:
  it runs SHA-256 compression once over the header's complete 64-byte blocks
  (the "midstate"), and the GPU kernel resumes from that state, computing
  only the final partial block per trial instead of re-hashing the whole
  header. `buildMidstate` checks this shape defensively (word-aligned
  remainder, room for padding) and reports `ok=false` if a future codec
  change ever breaks the assumption, so GPU mining fails safe onto the CPU
  path instead of mining against the wrong bytes.
  `TestMidstateMatchesHash` pins the two paths' outputs as identical.
- The OpenCL kernel (in `gpu_opencl.go`) mirrors `sha256mid.go`'s Go
  implementation function-for-function; keep them in sync if either changes.
  One cgo gotcha worth remembering: `clSetKernelArg` needs a pointer to each
  `cl_mem` handle, but `&someStructField` where the field's Go type is
  itself a pointer (like `C.cl_mem`) panics at runtime ("Go pointer to
  unpinned Go pointer") — copy the handle into a local `uintptr` first and
  take *that* address instead (see `memArg` in `gpu_opencl.go`).

## CLI (cmd/blugold)

- Subcommand dispatch via `flag.NewFlagSet` per subcommand. Data dir:
  `--datadir` flag, else `$BLUGOLD_DATA`, else `~/.blugold`.
- **`serve`** runs the node only (p2p + local API, no mining); **`mine`**
  runs node + miner (`-t` threads). Both share the same flag set via
  `parseNodeFlags`/`runNode` — keep them in sync when adding flags.
- Client commands (`send`/`balance`/`info`/`scan`) are thin HTTP clients of the
  node's localhost API (default `127.0.0.1:34335`); they never touch chain code.
- `serve`/`mine` accept `--adv host:port` to override the advertised address —
  required on NAT'd/cloud hosts where the interface IP is private. Without
  it, a LAN IP is auto-detected (UDP-dial trick).
- They auto-create the wallet (but fatal on a *corrupt* wallet file
  rather than silently regenerating — that would lose the address/funds),
  print a banner, and handle SIGINT/SIGTERM.
- Default ports: P2P 7007, API 34335.

## Testing conventions

- `go test ./...`, and `go test -race ./...` before merging anything touching
  node/p2p/miner (they are heavily concurrent; races were caught here).
- Unit tests per package; `internal/itest` boots real multi-node networks over
  loopback TCP (convergence, tx propagation, late-joiner catch-up).
- Determinism gotcha: block hashes are random per run, so equal-work tie-break
  outcomes vary between runs. Never assert a specific tie outcome.
- Funding test wallets: prefer mining coinbases (difficulty 1 = instant) over
  injecting state directly; injected funds do not exist on any chain and will
  be rolled away by reorgs (this produced a subtle test bug once).

## Gotchas that previously bit us

1. Retarget formula inverted (fast blocks lowered difficulty) — fixed; pinned
   by `TestNextDifficultyClamp`.
2. `pendingState` cache not invalidated on tip change → stale zero balances.
3. Self-connection guard compared only the configured advertise string and
   broke with ephemeral `:0` ports.
4. Merkle tree: odd levels duplicate the last node; 3-tx root must equal the
   4-tx root with the last tx repeated (`TestMerkleRoot`).
5. Equal-work forks legitimately win via the hash tie-break — several tests
   were rewritten after asserting otherwise.
6. Genesis must be written to `blocks.jsonl` on first boot and validated by
   hash on later boots (params-mismatch detection).
7. A mempool tx that turns stale after a tip change was once baked into
   every candidate, causing endless rejected blocks (miner stall) — fixed
   by `revalidateMempoolLocked` + `BuildCandidate` apply-filter.
8. `MaxPeers=0` silently disabled all accept/dial (full() always true).
9. Advertise-address auto-detection is wrong on NAT'd VPSes — use `--adv`.
10. Test funding via state injection doesn't survive reorg replay; with
    small rewards, a fork chain may not be able to fund a rolled-back tx —
    size test amounts against the *fork* chain's coinbase income.
11. Height-based `getblocks` never converged diverged chains. Nodes asked for
    blocks above their own tip height, so a peer on a different chain answered
    with blocks whose parents were unknown; they piled into the orphan buffer
    (and were marked "seen", so they were never retried) and the two chains
    coexisted forever despite live connections. Fixed by block locators —
    `TestDivergedChainsConverge` and friends in `internal/itest` pin it.
    Fork choice is only as good as the branches a node manages to download.
12. `n.hashrate` was a bare `func() uint64` field written by `SetHashrateSource`
    from `main()` and read by `Info()` from the API's HTTP handler goroutine,
    with no lock — a real data race, just one the existing tests never
    triggered because nothing called `Info()` concurrently with node startup.
    Fixed by moving both the write and the read under `n.mu`
    (`TestLateJoinerSeesMempool` and the rest of the suite pass under
    `-race`, but this specific race needed a targeted fix, not a test, since
    reproducing a startup-ordering race reliably in a test is its own can of
    worms).

## Known simplifications (documented, accepted)

- No coinbase maturity (a reorg can claw back a fresh reward).
- No peer scoring, no ban lists, no message rate limiting.
- Single writer mutex; state replay from checkpoints is O(fork depth).
- `seenTx` resets wholesale rather than using an LRU.
- Every peer is polled with a locator every 10s rather than tracking each
  peer's announced tip; responses are empty once converged.
- The node trusts its own clock for timestamp validation (no median-time-past).

## Possible next steps (if anyone asks)

- `blugold seed` bootstrap node / DNS seeds for easier friend onboarding.
- Coinbase maturity (e.g. 10 blocks) if reward clawbacks start to annoy people.
- Fee market minimums if the mempool ever fills for real.
- A tiny terminal dashboard (hashrate, peers, latest blocks) — the API already
  exposes everything needed.
