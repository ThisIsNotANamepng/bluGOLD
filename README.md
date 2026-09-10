# bluGOLD

A stupid cryptocurrency for the cybersecurity club of UW Eau Claire.

bluGOLD (BLG) is a real, mineable proof-of-work coin built for fun — learn
how a blockchain works by running one with your friends. It is absolutely
**not** for anything serious.

New here? Start with the [running guide](docs/GUIDE.md) — build, wallet,
mining, joining the network, and sending coins in five minutes.

## Features

- Proof-of-work mining (sha256), multi-threaded on CPU or (opt-in, see
  below) OpenCL GPU: more compute = more coins
- Auto difficulty retargeting toward a 6-minute block target
- Solo miners average exactly **10 BLG per hour** (1 BLG per block); with
  friends mining, blocks split proportionally to hashrate
- Capped supply: **20,160 BLG** total — 1 BLG per block, halving every
  10,080 blocks (~6 weeks), no premine
- Real wallets: ed25519 keypairs, `blu1...` addresses, signed transactions
- P2P networking over TCP: gossip blocks/txs, sync from seeds, heaviest-chain fork choice
- Zero dependencies by default — pure Go stdlib, easy to audit in a security
  club. GPU mining is the one opt-in exception: it needs cgo + an OpenCL SDK
  and is left out unless you build with `-tags gpu`.

## Quickstart

Requires Go 1.24+.

```sh
go build -o blugold ./cmd/blugold
```

Have a GPU and an OpenCL SDK installed? Build with `-tags gpu` instead to
enable GPU mining (see below):

```sh
go build -tags gpu -o blugold ./cmd/blugold
```

**Terminal 1 — mine some coins:**

```sh
./blugold init                      # creates ~/.blugold + wallet
./blugold mine -t 20               # node + miner, one thread per core
```

The first blocks mine instantly (difficulty starts at 1) and difficulty
climbs for about an hour until blocks take ~6 minutes. From then on a solo
miner earns a steady 10 BLG/hour.

`mine` picks CPU or GPU automatically (`-backend auto`, the default) by
timing a quick benchmark of both — pass `-backend cpu` or `-backend gpu` to
force one. Forcing `gpu` on a binary built without `-tags gpu` (or with no
OpenCL device found) logs a warning and falls back to CPU rather than
refusing to mine. See the [running guide](docs/GUIDE.md#gpu-mining) for
details.

**Terminal 2 — a friend joins (another machine or datadir):**

```sh
./blugold init --datadir ~/.blugold2
./blugold serve --datadir ~/.blugold2 --p2p :34336 --api 127.0.0.1:34335 \
    --seed <your-lan-ip>:7007
```

**Move money around:**

```sh
./blugold address                   # your address
./blugold balance                   # your balance
./blugold send <friend-addr> 5      # send 5 BLG
./blugold info                      # chain height, difficulty, hashrate
./blugold scan 10                   # last 10 blocks
```

## Run a public seed node

One always-on node in the cloud keeps the club network stitched together.
Any cheap VPS works. The seed node does **not** need to mine — it relays
blocks and transactions; mining happens on your rig.

```sh
# on the VPS (Ubuntu example)
apt install -y golang-go git
git clone <repo> blugold && cd blugold
go build -o /usr/local/bin/blugold ./cmd/blugold
blugold init

# firewall: open P2P, NEVER open the API port (it has no authentication)
ufw allow 7007/tcp
ufw default deny incoming   # 34335 stays bound to 127.0.0.1 anyway
```

If the VPS has a private interface IP (AWS/GCP/DigitalOcean do), tell it its
public address with `--adv`, then run it under systemd:

```ini
# /etc/systemd/system/blugold.service
[Unit]
Description=bluGOLD seed node
After=network-online.target

[Service]
User=blugold
ExecStart=/usr/local/bin/blugold serve --adv 203.0.113.7:7007
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

```sh
systemctl enable --now blugold
```

Share the address `203.0.113.7:7007` with friends — everyone joins with
`blugold serve --seed 203.0.113.7:7007`. Behind NAT, `curl 127.0.0.1:34335/api/peers`
on a laptop may only list the seed; that is expected. The seed relays
blocks and transactions, so you can still send coins. Nodes also gossip
reachable peer addresses, so two machines on the same LAN mesh directly.

Care on a public box: `wallet.json` is mode 0600 but lives on a machine you
don't fully control — mine on your rig, not the seed. The API
(`127.0.0.1:34335`) can spend the node wallet; never expose it.

## Data layout

Everything lives in `~/.blugold` (override with `--datadir` or `$BLUGOLD_DATA`):

| File | Purpose |
|---|---|
| `wallet.json` | ed25519 private key (mode 0600 — treat it like cash) |
| `blocks.jsonl` | append-only block storage |
| `peers.json` | known peer addresses |

## Ports

| Port | Purpose |
|---|---|
| 7007 | P2P (gossip + sync) |
| 34335 | localhost HTTP API used by the CLI |

## Status / caveats

This is a toy. Known simplifications versus a serious coin:

- No coinbase maturity (a reorg can claw back a fresh reward)
- Zero-fee transactions by default; mempool is fee-ordered with a cap
- Sybil resistance is "be nice": no peer scoring, no ban lists
- Difficulty clamps at 4× per retarget window (20 blocks), so a sudden
  burst of hashrate briefly mines blocks faster than target

See [docs/PROTOCOL.md](docs/PROTOCOL.md) for the consensus and wire specs.

## Development

```sh
go test ./...        # unit + integration tests
go test -race ./...  # with race detector
go vet ./...
```

The integration tests spin up real multi-node networks on loopback TCP.

bluGOLD is released under the MIT license. Mine responsibly.
