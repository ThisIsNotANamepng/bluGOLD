# bluGOLD — running guide

A step-by-step guide for club members. Everything you need to mine, hold,
and send BLG. Takes about five minutes to get started.

You need Go 1.24+ installed (`go version` to check).

## 1. Build it

```sh
git clone <repo> && cd bluGOLD
go build -o blugold ./cmd/blugold
./blugold version
```

`blugold` is one program that does everything: runs a node, mines, holds
your wallet, and talks to the network.

## 2. Create your wallet

```sh
./blugold init
```

This creates `~/.blugold/` with a keypair and prints your address, which
looks like `blu1g3qdplhsrxgjam4iwz65f7kdxfkvsv54cb83`.

- `./blugold address` shows it again anytime.
- `wallet.json` is your money. It is saved with mode 0600, but back it up
  somewhere safe. Losing it loses your coins; there is no recovery.
- `./blugold keygen --force` makes a new wallet, but the old one's coins
  become unreachable — don't do this without backing up first.

Everything below assumes the default data dir. To keep a second node on the
same machine, add `--datadir ~/.blugold2` to *every* command and give it
different ports (shown in step 4).

## 3. Mine

```sh
./blugold mine -t 20
```

- `-t` is the number of CPU mining threads — one per core is right.
- A fresh chain mines blocks instantly, then difficulty ramps up over
  roughly an hour until a block takes ~6 minutes.
- Once difficulty settles, a **solo** miner earns 1 BLG per block ≈
  **10 BLG/hour**. If friends mine too, the network still makes a block
  every ~6 minutes and rewards split by hashrate share — your 20 cores get
  a bigger slice, but the network never pays out faster.
- Expect randomness: any given hour lands 10 ± 3 blocks; the average is 10.

Leave it running. Blocks pay straight into your wallet.

### GPU mining

`mine` takes `-backend auto|cpu|gpu` (default `auto`): auto times a quick
benchmark of both and mines with whichever is faster, so you don't have to
know in advance whether your CPU or GPU wins. Force one with `-backend cpu`
or `-backend gpu`.

GPU support uses OpenCL and isn't in a plain `go build` — it's the one
opt-in exception to bluGOLD's zero-dependency rule, since it needs cgo and
an OpenCL SDK on the machine that *builds* the binary (not just the one
that runs it). Build it in with:

```sh
go build -tags gpu -o blugold ./cmd/blugold
```

If that binary finds no OpenCL GPU at runtime, `-backend gpu` logs a
warning and falls back to CPU rather than refusing to mine; `-backend auto`
just quietly picks CPU. `-gpu-batch N` tunes how many nonces one GPU
dispatch searches (0 = a sane default) if you need to trade responsiveness
for throughput on unusual hardware.

## 4. Run another node / join the network

Anyone can join by pointing at an existing node:

```sh
./blugold serve --seed 203.0.113.7:7007
```

Replace that address with your club's seed node (see section 7). From the
same machine, run a second, non-mining node like this:

```sh
./blugold init --datadir ~/.blugold2
./blugold serve --datadir ~/.blugold2 --p2p :34336 --api 127.0.0.1:34336 \
    --seed 203.0.113.7:7007
```

Rules when co-locating nodes: each needs its own `--datadir`, its own
`--p2p` port, and its own `--api` port. The p2p port is the coin's network;
the api port is local-only (CLI talks to it) — never share, never expose.

## 5. Check your stuff

In a second terminal while your node runs:

```sh
./blugold balance          # your balance (confirmed)
./blugold balance blu1...  # someone else's balance
./blugold info             # height, difficulty, hashrate, peers, supply
./blugold scan 10          # the last 10 blocks (explorer view)
curl -s 127.0.0.1:34335/api/peers   # who you're connected to
```

`info` fields worth knowing: `height` is how many blocks exist; `hashrate`
is your miner's speed; `difficulty` is what the network demands per block;
`reward` is the current payout.

## 6. Send coins

```sh
./blugold send blu1g3qdplhsrxgjam4iwz65f7kdxfkvsv54cb83 5
```

- Amounts are in BLG with up to 8 decimals: `0.5`, `2.25`, `100` all work.
- Fees are optional: `--fee 0.01` tips the miner. Zero-fee is the norm here.
- The tx shows up in the mempool instantly, then gets **mined into a block
  within ~6 minutes** — this is normal. Check the recipient's balance after
  the next block or two.
- If the node isn't running you'll get `node unreachable` — start it first.

## 7. Run the public seed node (cloud)

One always-on VPS keeps the network stitched together. It does not need to
mine. Full details live in the README; the short version:

```sh
# open the P2P port, never the API port
ufw allow 7007/tcp

# if the VPS's interface IP is private (AWS/GCP/DigitalOcean),
# tell it its public address explicitly:
blugold serve --adv 203.0.113.7:7007
```

Run it under systemd (`Restart=always`) so it survives reboots — the README
has a ready-made unit file. Share `203.0.113.7:7007` with the club; every
member uses it as their `--seed`. Nodes tell each other about peers they
learn, so the network meshes even if the seed goes down later.

## 8. Troubleshooting

| Symptom | Meaning / fix |
|---|---|
| `node unreachable at ...` | The `serve`/`mine` daemon isn't running, or `--api` points at the wrong port. |
| `bind: address already in use` | Another node (or your own p2p listener) has that port. Pick different `--p2p`/`--api` ports. |
| `wallet file corrupt` | The node refuses to start rather than silently making you a new address. Restore or move the file aside. |
| `tx rejected: insufficient funds` | Your wallet hasn't mined enough yet. Check `balance`. |
| `tx rejected: bad nonce` | You sent twice very fast; wait for the first tx to be mined. |
| Balance unchanged after a send | It's waiting for a block (~6 min). `scan` shows recent blocks. |
| Height differs between friends | Normal during sync; it catches up within seconds of connecting. |
| `peers: 0` against a node you know is up | Version mismatch — every node must run the same protocol version. Rebuild and restart all of them. |
| `/api/peers` only lists the seed | Normal behind NAT. Laptops cannot dial each other; the seed relays txs and blocks. You still need someone mining, then ~6 min for a block. If a send never shows up even after a block, the seed is on an old build that dropped extra friends who advertised the same LAN IP — rebuild and restart the VPS. |
| Peers connected, but heights and tips never converge | Everyone is not on the same build. Nodes running the old height-based sync (protocol 1) mine parallel chains forever; upgrade every node. On the upgraded build the lighter chain is downloaded and abandoned, so its miner's balance drops to 0 — those coins were only ever real on the losing chain. |
| Difficulty shot up, blocks are slow | Someone joined with a big rig. It settles at the next retarget. |

## 9. Cheat sheet

```sh
./blugold init                    # one-time wallet setup
./blugold mine -t 20              # run a full miner
./blugold serve --seed IP:7007   # join the network
./blugold address                 # show your address
./blugold balance                 # show your balance
./blugold send <addr> <amount>    # send BLG
./blugold info                    # network status
./blugold scan 10                 # recent blocks
./blugold version                 # version
```

Happy mining. Nothing here is financial advice; nothing here is real money.
