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
tip hash. Blocks whose parent is unknown are buffered as orphans; on reorg,
transactions from the abandoned branch return to the mempool if still valid.

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
message from an unhandshaked peer drops the connection. Peers advertising our
own address are dropped (self-connection guard).

| Type | Payload | Direction |
|---|---|---|
| `version` | `{protocol, listen_addr, height}` | both, first |
| `peers` | `{addrs: [host:port]}` | both, periodically |
| `getblocks` | `{from, count}` (max 500) | requester |
| `blocks` | `{blocks: [Block]}` | responder |
| `newtx` | `{tx}` | gossip |
| `newblock` | `{block}` | gossip |

Flow:

- On handshake (and every 10s), a node behind a peer sends `getblocks` from
  its tip height + 1; the peer answers with blocks from its **active chain**
- `newblock`/`newtx` are flooded to all peers except the sender; duplicate
  suppression by hash
- Block/tx payloads are the same JSON encodings used for on-disk storage
- Protocol version is 1; mismatched versions are disconnected

## Local HTTP API

The node binds `127.0.0.1:34335` for its CLI client:

| Endpoint | Method | Purpose |
|---|---|---|
| `/api/info` | GET | height, difficulty, supply, balance, peers, hashrate |
| `/api/balance?addr=` | GET | balance + nonce (default: node wallet) |
| `/api/send` | POST | `{to, amount, fee}` — sign + broadcast |
| `/api/blocks?count=` | GET | recent block summaries |
| `/api/peers` | GET | connected peers |
