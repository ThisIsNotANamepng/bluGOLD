# bluGOLD protocol

bluGOLD is a small proof-of-work cryptocurrency. This document specifies its
consensus rules and peer-to-peer wire protocol. All integers are little-endian
in block/tx encodings and big-endian in frame headers. All timestamps are unix
milliseconds.

## Units

1 BLG = 10^8 bluglets. Amounts are `uint64` bluglets.

## Addresses

```
addr = "blu1" || base32_lower_nopad( sha256(ed25519_pubkey)[0:20] ) || checksum
checksum = hex( sha256("blu1" || body) )[0:4]
```

Length is fixed at 40 characters. The empty address is reserved for coinbase
inputs.

## Transactions

| Field | Type |
|---|---|
| From | address (empty = coinbase) |
| To | address |
| Amount | uint64 bluglets |
| Fee | uint64 bluglets |
| Nonce | uint64 (per-sender counter, 0 for first tx) |
| Time | int64 unix ms |
| PubKey | 32-byte ed25519 public key (regular txs only) |
| Sig | 64-byte ed25519 signature (regular txs only) |

Signing: `sig = ed25519_sign(privkey, signing_bytes)` where `signing_bytes`
is the deterministic binary encoding of every field except `Sig`.

Encoding: fields in table order, strings/byte-slices length-prefixed with
`uint32`, integers little-endian. TxID = `sha256(encoding including Sig)`.

Validation:

- Coinbase: no pubkey/sig/fee, valid non-empty `To`
- Regular: valid addresses, pubkey derives `From`, signature verifies
- Nonce must equal the sender's current nonce; balance must cover `Amount+Fee`
- Coinbase in a block must pay exactly `reward(height) + sum(fees)`; it is
  applied **after** the other txs, so a miner cannot spend its own
  same-block reward

## Blocks

| Field | Type |
|---|---|
| Height | uint64 |
| PrevHash | 32 bytes |
| Time | int64 unix ms |
| Difficulty | uint64 |
| Miner | address (coinbase recipient) |
| ExtraNonce | uint64 (mining scratch space) |
| MerkleRoot | 32 bytes |
| Nonce | uint64 |
| Txs | [Tx] (first tx must be the coinbase) |

BlockHash = `sha256(header encoding)` (header = the fields above, no txs).
Merkle root: pairwise sha256 over tx hashes, last node duplicated on odd
levels; empty = 32 zero bytes.

## Proof of work

`target = (2^256 - 1) / difficulty`. A block is valid iff
`big_endian_int(BlockHash) < target`. Difficulty 1 accepts any hash.

## Difficulty retarget

Every `BlocksPerRetarget` (20) blocks, for the block at height
`H` where `H % 20 == 0`:

```
expected = 20 * 6min
actual   = Time(H-1) - Time(H-21)
ratio    = clamp(expected / actual, 1/4, 4)   # actual <= 0 counts as 4
difficulty(H) = max(1, difficulty(H-1) * ratio)
```

All other blocks inherit the parent difficulty. A block must carry exactly
the difficulty so computed — miners cannot pick their own.

## Reward schedule

```
reward(height) = 1 BLG >> (height / 10080)
```

Reward halves every 10,080 blocks (~6 weeks at target). Total supply is
capped at 20,159.99879040 BLG (~20,160). Genesis (height 0) carries no
coinbase. A solo miner earns every block, so a solo network produces
exactly 10 BLG/hour once difficulty converges; with several miners, blocks
split by hashrate share.

## Chain selection

The active chain is the one with the greatest **cumulative difficulty** (sum
of block difficulties from genesis). Ties break toward the lexically smaller
tip hash. Blocks whose parent is unknown are buffered as orphans and trigger a
`getblocks` for the missing ancestry; on reorg, transactions from the abandoned
branch return to the mempool if still valid.

Fork choice can only pick a winner among branches a node actually holds, so a
node must be able to download a branch that starts *below* its own tip — see
block locators below.

State replay uses cached account-state snapshots (checkpoints) every 100
blocks to avoid full replays on deep reorgs.

## Consensus parameters

| Param | Value |
|---|---|
| Target block time | 360,000 ms |
| Blocks per retarget | 20 |
| Reward halving interval | 10,080 blocks |
| Initial reward | 1 BLG |
| Max txs per block | 1000 |
| Max future timestamp | +300,000 ms |
| Genesis time | 1727049600000 (2024-09-23 UTC) |
| Genesis difficulty | 1 |

Nodes with different parameters produce a different genesis hash and thus
cannot accidentally merge.

## Block validation order

1. Structural: height = parent+1, PrevHash matches, Time in
   `(parent.Time, now + 300s]`, first tx coinbase, merkle root matches,
   miner address valid
2. Difficulty matches retarget rule; PoW target met
3. State: every tx validates against the sequentially-updated account state;
   coinbase amount exactly `reward + fees`

## Wire protocol

TCP with length-prefixed JSON frames:

```
[4-byte big-endian length][JSON payload]     max 8 MiB
payload = {"type": <msg>, "payload": {...}}
```

On connect, both sides immediately send `version`; the first non-version
message from an unhandshaked peer drops the connection. Each node picks a
random `nonce` at startup; a peer that echoes our nonce is us (self-dial)
and is dropped. Advertised listen addresses are *not* an identity —
behind NAT many nodes claim the same `192.168.1.x:7007`, and the seed
must keep all of them so it can relay.

| Type | Payload | Direction |
|---|---|---|
| `version` | `{protocol, listen_addr, height, nonce?}` (`nonce` optional, omitted = 0) | both, first |
| `peers` | `{addrs: [host:port]}` | both, periodically |
| `getblocks` | `{locator, count}` (count max 500) | requester |
| `blocks` | `{blocks: [Block]}` | responder |
| `newtx` | `{tx}` | gossip |
| `newblock` | `{block}` | gossip |
| `getmempool` | `{}` | requester, once on connect |
| `mempool` | `{txs: [Tx]}` | responder |

### Block locators

`locator` is a list of block hashes from the requester's best branch, newest
first: its tip, then ancestors at increasing intervals (dense for the last ten
blocks, then doubling gaps), always ending with genesis. A ~25-entry locator
covers a chain of any realistic length.

The responder scans the locator in order and answers with up to `count` blocks
of its **active chain** starting just after the first entry it also holds on
that chain — genesis if it recognises nothing else. A response is also cut off
after 5000 transactions so it cannot exceed the frame limit; the requester
simply asks again from where the short batch ended. That block is the deepest
common ancestor the two nodes can agree on cheaply, so the answer always
connects to something the requester already has.

Requesting a height range instead cannot work across a fork: the answer is a
run of blocks whose parents the requester has never seen, and it can only
orphan them forever.

Flow:

- On handshake, and every 10s, a node sends `getblocks` to each peer with a
  locator for its own tip. It asks **unconditionally** — an announced height
  says nothing about cumulative work, and two chains of equal height can be
  entirely different. A peer with nothing to add answers with no blocks.
- On receiving `blocks`, a node inserts them and, if any were new, immediately
  requests the next batch with a locator anchored at the **last block of the
  batch**. Anchoring at its own tip would loop: the tip does not move until the
  branch being downloaded outweighs the current one.
- A `newblock` whose parent is unknown is buffered as an orphan and answered
  with a `getblocks` locator, which is how two long-diverged chains discover
  each other's history.
- `newblock`/`newtx` are flooded to all peers except the sender; duplicate
  suppression is by block hash (tree membership) and tx hash. Blocks received
  in a `blocks` response are *not* re-flooded — they were requested, and every
  peer polls for itself.
- Block/tx payloads are the same JSON encodings used for on-disk storage
- Protocol version is 2; mismatched versions are disconnected. Version 1 used
  `getblocks {from, count}` and could not sync across a fork.

### Mempool sync

Flood gossip (`newtx`) only reaches peers that were already connected at
broadcast time, so a node that connects (or reconnects) after a tx was
gossiped would otherwise not see it until the next block confirms it.

- On handshake, right after the initial `getblocks`, a node sends
  `getmempool` (empty payload) to the new peer.
- The peer answers `mempool` with its pending transactions, oldest-first,
  capped at 5000 txs so the reply cannot approach the 8 MiB frame limit. A
  mempool larger than that is not fully synced by this exchange; it still
  reaches new peers eventually via `newtx` gossip and confirmed blocks.
- Received txs are fed through the normal tx-acceptance path: dedup by hash,
  validate against pending state, add to the mempool, and re-broadcast if
  new. There is no separate mempool wire format.

## Local HTTP API

The node binds `127.0.0.1:34335` for its CLI client:

| Endpoint | Method | Purpose |
|---|---|---|
| `/api/info` | GET | height, difficulty, supply, balance, peers, hashrate |
| `/api/balance?addr=` | GET | balance + nonce (default: node wallet) |
| `/api/send` | POST | `{to, amount, fee}` — sign + broadcast |
| `/api/blocks?count=` | GET | recent block summaries |
| `/api/peers` | GET | connected peers |
