// Package node implements the bluGOLD full node: block tree, fork choice,
// mempool, transaction flow, and peer sync.
package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"sort"
	"sync"
	"time"

	"blugold/internal/chain"
	"blugold/internal/crypto"
	"blugold/internal/p2p"
	"blugold/internal/state"
	"blugold/internal/store"
	"blugold/internal/wire"
)

var ErrOrphan = errors.New("parent block unknown")

const (
	checkpointInterval = 100
	maxOrphans         = 100
	maxMempool         = 10000
	seenResetAt        = 50000
	syncBatch          = 500
)

type blockEntry struct {
	block *chain.Block
	hash  chain.Hash
	work  *big.Int
}

type Config struct {
	Params    chain.Params
	DataDir   string
	Listen    string
	Advertise string
	Seeds     []string
	Wallet    *crypto.Wallet
	PeerSync  bool
	LogBlocks bool
	MaxPeers  int
}

type Node struct {
	cfg    Config
	params chain.Params
	store  *store.Store
	sw     *p2p.Switch
	wallet *crypto.Wallet

	mu           sync.Mutex
	blocks       map[chain.Hash]*blockEntry
	tip          *blockEntry
	state        *state.State
	checkpoints  map[chain.Hash]*state.State
	rejected     map[chain.Hash]bool
	orphans      map[chain.Hash][]*chain.Block
	chainHeights map[uint64]chain.Hash

	mempoolTx    map[chain.Hash]*chain.Tx
	mempoolOrder []chain.Hash
	pending      *state.State
	pendingDirty bool

	seenTx    map[chain.Hash]struct{}
	seenBlock map[chain.Hash]struct{}

	tipSubs  map[chan *chain.Block]struct{}
	booting  bool
	hashrate func() uint64
	stopped  bool
}

func New(cfg Config) (*Node, error) {
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	n := &Node{
		cfg:          cfg,
		params:       cfg.Params,
		store:        st,
		wallet:       cfg.Wallet,
		blocks:       make(map[chain.Hash]*blockEntry),
		checkpoints:  make(map[chain.Hash]*state.State),
		rejected:     make(map[chain.Hash]bool),
		orphans:      make(map[chain.Hash][]*chain.Block),
		chainHeights: make(map[uint64]chain.Hash),
		mempoolTx:    make(map[chain.Hash]*chain.Tx),
		seenTx:       make(map[chain.Hash]struct{}),
		seenBlock:    make(map[chain.Hash]struct{}),
		tipSubs:      make(map[chan *chain.Block]struct{}),
		state:        state.New(),
	}

	n.booting = true
	blocks, err := st.LoadBlocks()
	if err != nil {
		return nil, fmt.Errorf("load blocks: %w", err)
	}
	if len(blocks) == 0 {
		genesis := chain.Genesis(n.params)
		if err := n.insertBlockLocked(genesis, false, false, ""); err != nil {
			return nil, fmt.Errorf("insert genesis: %w", err)
		}
		if err := st.AppendBlock(genesis); err != nil {
			return nil, err
		}
	} else {
		for _, b := range blocks {
			if err := n.insertBlockLocked(b, true, false, ""); err != nil {
				return nil, fmt.Errorf("load block %d: %w", b.Height, err)
			}
		}
	}
	n.booting = false
	n.mu.Lock()
	n.selectTipLocked()
	n.mu.Unlock()

	if cfg.PeerSync {
		seeds := append([]string{}, cfg.Seeds...)
		if saved, err := st.LoadPeers(); err == nil {
			seeds = append(seeds, saved...)
		}
		n.sw = p2p.New(cfg.Listen, cfg.Advertise, seeds)
		if cfg.MaxPeers > 0 {
			n.sw.MaxPeers = cfg.MaxPeers
		}
		n.sw.GetHeight = n.TipHeight
		n.sw.OnConnect = n.onPeerConnect
		n.sw.OnMessage = n.onMessage
		n.sw.SetPeerPersister(func(addrs []string) {
			_ = st.SavePeers(addrs)
		})
		if err := n.sw.Start(); err != nil {
			return nil, fmt.Errorf("p2p start: %w", err)
		}
		go n.syncLoop()
	}
	return n, nil
}

func (n *Node) Stop() {
	n.mu.Lock()
	if n.stopped {
		n.mu.Unlock()
		return
	}
	n.stopped = true
	n.mu.Unlock()
	if n.sw != nil {
		n.sw.Stop()
	}
	_ = n.store.Close()
}

func (n *Node) Switch() *p2p.Switch { return n.sw }

func (n *Node) Params() chain.Params { return n.params }

func (n *Node) Wallet() *crypto.Wallet { return n.wallet }

func (n *Node) SetHashrateSource(f func() uint64) { n.hashrate = f }

// WalletAddress returns the node wallet's address, or "" if no wallet is loaded.
func (n *Node) WalletAddress() crypto.Address {
	if n.wallet == nil {
		return ""
	}
	return n.wallet.Address()
}

func (n *Node) TipHeight() uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.tip == nil {
		return 0
	}
	return n.tip.block.Height
}

func (n *Node) TipHash() chain.Hash {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.tip == nil {
		return chain.Hash{}
	}
	return n.tip.hash
}

func (n *Node) nowMs() int64 { return time.Now().UnixMilli() }

func (n *Node) genesisHash() chain.Hash { return chain.Genesis(n.params).Hash() }

func (n *Node) anchorLocked(parent *blockEntry) *chain.Block {
	if n.params.BlocksPerRetarget == 0 || (parent.block.Height+1)%n.params.BlocksPerRetarget != 0 {
		return nil
	}
	cur := parent
	for i := uint64(0); i < n.params.BlocksPerRetarget-1 && cur != nil; i++ {
		cur = n.blocks[cur.block.PrevHash]
	}
	if cur == nil {
		return nil
	}
	return cur.block
}

func (n *Node) insertBlockLocked(b *chain.Block, fromDisk, announce bool, exceptPeer string) error {
	h := b.Hash()
	if _, ok := n.blocks[h]; ok {
		return nil
	}
	if n.rejected[h] {
		return errors.New("block previously rejected")
	}
	if b.Height == 0 {
		if h != n.genesisHash() {
			return errors.New("unknown genesis block (params mismatch?)")
		}
		n.blocks[h] = &blockEntry{block: b, hash: h, work: big.NewInt(0)}
		n.chainHeights[0] = h
		return nil
	}

	parent := n.blocks[b.PrevHash]
	if parent == nil {
		n.addOrphanLocked(b)
		return ErrOrphan
	}

	now := n.nowMs()
	if err := b.ValidateBasic(n.params, parent.block, now); err != nil {
		return fmt.Errorf("invalid block: %w", err)
	}
	if want := chain.NextDifficulty(n.params, parent.block, n.anchorLocked(parent)); b.Difficulty != want {
		return fmt.Errorf("difficulty %d, want %d", b.Difficulty, want)
	}
	if err := b.ValidatePoW(n.params); err != nil {
		return fmt.Errorf("invalid pow: %w", err)
	}

	n.blocks[h] = &blockEntry{
		block: b,
		hash:  h,
		work:  new(big.Int).Add(parent.work, new(big.Int).SetUint64(b.Difficulty)),
	}
	if !fromDisk {
		if err := n.store.AppendBlock(b); err != nil {
			log.Printf("store append: %v", err)
		}
	}
	n.markBlockSeen(h)
	n.promoteOrphansLocked(h)

	if announce && !n.booting && n.sw != nil {
		if env, err := wire.NewEnvelope(wire.MsgNewBlock, wire.NewBlockMsg{Block: b}); err == nil {
			n.sw.Broadcast(env, exceptPeer)
		}
	}
	if !n.booting {
		n.selectTipLocked()
	}
	return nil
}

func (n *Node) addOrphanLocked(b *chain.Block) {
	total := 0
	for _, l := range n.orphans {
		total += len(l)
	}
	if total >= maxOrphans {
		return
	}
	n.orphans[b.PrevHash] = append(n.orphans[b.PrevHash], b)
}

func (n *Node) promoteOrphansLocked(parent chain.Hash) {
	list, ok := n.orphans[parent]
	if !ok {
		return
	}
	delete(n.orphans, parent)
	for _, b := range list {
		if err := n.insertBlockLocked(b, false, true, ""); err != nil && !errors.Is(err, ErrOrphan) {
			log.Printf("orphan promotion: %v", err)
		}
	}
}

func (n *Node) selectTipLocked() {
	var best *blockEntry
	for _, e := range n.blocks {
		if n.rejected[e.hash] {
			continue
		}
		if best == nil || e.work.Cmp(best.work) > 0 ||
			(e.work.Cmp(best.work) == 0 && e.hash.String() < best.hash.String()) {
			best = e
		}
	}
	if best == nil || n.tip == best {
		return
	}
	if err := n.switchChainLocked(best); err != nil {
		log.Printf("chain switch to %s rejected: %v", best.hash.Short(), err)
	}
}

func (n *Node) switchChainLocked(newTip *blockEntry) error {
	if n.tip == nil {
		newPath := n.pathToGenesisLocked(newTip)
		reverse(newPath)
		fresh := state.New()
		if err := n.replayLocked(fresh, newPath); err != nil {
			return err
		}
		n.commitChainLocked(fresh, newPath, nil)
		return nil
	}

	if newTip.block.PrevHash == n.tip.hash {
		fresh := n.state.Clone()
		if err := fresh.ApplyBlock(newTip.block, n.params); err != nil {
			n.rejectSubtreeLocked(newTip)
			return fmt.Errorf("block state invalid: %w", err)
		}
		if newTip.block.Height%checkpointInterval == 0 {
			n.checkpoints[newTip.hash] = fresh.Clone()
		}
		n.chainHeights[newTip.block.Height] = newTip.hash
		n.state = fresh
		n.tip = newTip
		n.pendingDirty = true
		n.dropConfirmedLocked(newTip)
		n.publishLocked(newTip.block)
		return nil
	}

	newPath := n.pathToGenesisLocked(newTip)
	reverse(newPath)
	oldPath := n.pathToGenesisLocked(n.tip)
	oldSet := make(map[chain.Hash]struct{}, len(oldPath))
	for _, e := range oldPath {
		oldSet[e.hash] = struct{}{}
	}
	var fork *blockEntry
	for _, e := range newPath {
		if _, ok := oldSet[e.hash]; ok {
			fork = e
			break
		}
	}
	if fork == nil {
		return errors.New("no common ancestor")
	}

	fresh := n.baseStateLocked(newPath)
	startHeight := uint64(0)
	for _, e := range newPath {
		if n.checkpoints[e.hash] != nil {
			startHeight = e.block.Height + 1
		}
	}
	tail := tailAfter(newPath, startHeight)
	if err := n.replayLocked(fresh, tail); err != nil {
		return err
	}

	var rolledBack []*chain.Tx
	for _, e := range oldPath {
		if e.block.Height > fork.block.Height {
			rolledBack = append(rolledBack, e.block.Txs...)
		}
	}
	n.commitChainLocked(fresh, newPath, rolledBack)
	n.publishLocked(newTip.block)
	return nil
}

func (n *Node) replayLocked(fresh *state.State, path []*blockEntry) error {
	for _, e := range path {
		if e.block.Height == 0 {
			continue
		}
		if err := fresh.ApplyBlock(e.block, n.params); err != nil {
			n.rejectSubtreeLocked(e)
			return fmt.Errorf("replay %s: %w", e.hash.Short(), err)
		}
		if e.block.Height%checkpointInterval == 0 {
			n.checkpoints[e.hash] = fresh.Clone()
		}
	}
	return nil
}

func (n *Node) baseStateLocked(newPath []*blockEntry) *state.State {
	var best *blockEntry
	for _, e := range newPath {
		if n.checkpoints[e.hash] != nil {
			best = e
		}
	}
	if best == nil {
		return state.New()
	}
	return n.checkpoints[best.hash].Clone()
}

// commitChainLocked installs the new active chain (ascending, genesis-first).
func (n *Node) commitChainLocked(fresh *state.State, newPath []*blockEntry, rolledBack []*chain.Tx) {
	newTip := newPath[len(newPath)-1]
	pathSet := make(map[chain.Hash]struct{}, len(newPath))
	n.chainHeights = make(map[uint64]chain.Hash, len(newPath))
	for _, e := range newPath {
		pathSet[e.hash] = struct{}{}
		n.chainHeights[e.block.Height] = e.hash
	}
	for h := range n.checkpoints {
		if _, ok := pathSet[h]; !ok {
			delete(n.checkpoints, h)
		}
	}
	n.state = fresh
	n.tip = newTip
	n.pendingDirty = true
	n.rebuildMempoolLocked(rolledBack)
}

func (n *Node) dropConfirmedLocked(newTip *blockEntry) {
	dropped := false
	for _, t := range newTip.block.Txs {
		h := t.Hash()
		if _, ok := n.mempoolTx[h]; ok {
			delete(n.mempoolTx, h)
			dropped = true
		}
	}
	if dropped || len(n.mempoolTx) > 0 {
		n.revalidateMempoolLocked()
	} else {
		n.pendingDirty = true
	}
}

// revalidateMempoolLocked re-applies every pending tx to the current state in
// order, dropping any that no longer validate (e.g. a competing spend of the
// same nonce was just confirmed). Without this, stale txs would be baked into
// freshly mined candidates and every new block would fail state validation.
func (n *Node) revalidateMempoolLocked() {
	work := n.state.Clone()
	keepTx := make(map[chain.Hash]*chain.Tx, len(n.mempoolTx))
	var keepOrder []chain.Hash
	for _, h := range n.mempoolOrder {
		t := n.mempoolTx[h]
		if t == nil {
			continue
		}
		if err := work.ApplyTx(t); err != nil {
			continue
		}
		keepTx[h] = t
		keepOrder = append(keepOrder, h)
	}
	n.mempoolTx = keepTx
	n.mempoolOrder = keepOrder
	n.pending = work
	n.pendingDirty = false
}

func (n *Node) rebuildMempoolLocked(rolledBack []*chain.Tx) {
	cands := make(map[chain.Hash]*chain.Tx, len(n.mempoolTx)+len(rolledBack))
	var order []chain.Hash
	add := func(t *chain.Tx) {
		h := t.Hash()
		if _, ok := cands[h]; !ok && !t.IsCoinbase() {
			cands[h] = t
			order = append(order, h)
		}
	}
	for _, h := range n.mempoolOrder {
		if t, ok := n.mempoolTx[h]; ok {
			add(t)
		}
	}
	for _, t := range rolledBack {
		add(t)
	}

	confirmSet := make(map[chain.Hash]struct{})
	for _, e := range n.pathToGenesisLocked(n.tip) {
		for _, t := range e.block.Txs {
			confirmSet[t.Hash()] = struct{}{}
		}
	}

	sort.SliceStable(order, func(i, j int) bool {
		return cands[order[i]].Time < cands[order[j]].Time
	})

	work := n.state.Clone()
	newTx := make(map[chain.Hash]*chain.Tx)
	var newOrder []chain.Hash
	for _, h := range order {
		t := cands[h]
		if _, confirmed := confirmSet[h]; confirmed {
			continue
		}
		if err := t.Verify(); err != nil {
			continue
		}
		if err := work.ApplyTx(t); err != nil {
			continue
		}
		newTx[h] = t
		newOrder = append(newOrder, h)
	}
	n.mempoolTx = newTx
	n.mempoolOrder = newOrder
	n.pending = work
	n.pendingDirty = false
}

func (n *Node) rejectSubtreeLocked(e *blockEntry) {
	var reject func(*blockEntry)
	reject = func(x *blockEntry) {
		n.rejected[x.hash] = true
		delete(n.blocks, x.hash)
		for _, child := range n.childrenLocked(x.hash) {
			reject(child)
		}
	}
	reject(e)
}

func (n *Node) childrenLocked(parent chain.Hash) []*blockEntry {
	var out []*blockEntry
	for _, e := range n.blocks {
		if e.block.PrevHash == parent {
			out = append(out, e)
		}
	}
	return out
}

func (n *Node) pathToGenesisLocked(e *blockEntry) []*blockEntry {
	var path []*blockEntry
	cur := e
	for cur != nil {
		path = append(path, cur)
		cur = n.blocks[cur.block.PrevHash]
	}
	return path
}

func reverse(path []*blockEntry) {
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
}

func tailAfter(path []*blockEntry, fromHeight uint64) []*blockEntry {
	var out []*blockEntry
	for _, e := range path {
		if e.block.Height >= fromHeight {
			out = append(out, e)
		}
	}
	return out
}

func (n *Node) pendingStateLocked() *state.State {
	if !n.pendingDirty && n.pending != nil {
		return n.pending
	}
	work := n.state.Clone()
	for _, h := range n.mempoolOrder {
		if t := n.mempoolTx[h]; t != nil {
			if err := work.ApplyTx(t); err == nil {
				continue
			}
		}
	}
	n.pending = work
	n.pendingDirty = false
	return work
}

func (n *Node) markSeenTx(h chain.Hash) {
	n.seenTx[h] = struct{}{}
	if len(n.seenTx) > seenResetAt {
		n.seenTx = make(map[chain.Hash]struct{})
	}
}

func (n *Node) markBlockSeen(h chain.Hash) {
	n.seenBlock[h] = struct{}{}
	if len(n.seenBlock) > seenResetAt {
		n.seenBlock = make(map[chain.Hash]struct{})
	}
}

func (n *Node) AddTx(t *chain.Tx, exceptPeer string) error {
	h := t.Hash()
	n.mu.Lock()
	if _, ok := n.seenTx[h]; ok {
		n.mu.Unlock()
		return nil
	}
	ps := n.pendingStateLocked()
	if err := ps.ValidateTx(t); err != nil {
		n.mu.Unlock()
		return fmt.Errorf("tx rejected: %w", err)
	}
	if err := ps.ApplyTx(t); err != nil {
		n.mu.Unlock()
		return fmt.Errorf("tx rejected: %w", err)
	}
	if len(n.mempoolTx) >= maxMempool {
		n.mu.Unlock()
		return errors.New("mempool full")
	}
	n.markSeenTx(h)
	n.mempoolTx[h] = t
	n.mempoolOrder = append(n.mempoolOrder, h)
	n.pending = ps
	n.pendingDirty = false
	n.mu.Unlock()

	if n.sw != nil {
		if env, err := wire.NewEnvelope(wire.MsgNewTx, wire.NewTxMsg{Tx: t}); err == nil {
			n.sw.Broadcast(env, exceptPeer)
		}
	}
	return nil
}

func (n *Node) Send(to chain.Address, amount, fee uint64) (chain.Hash, error) {
	if n.wallet == nil {
		return chain.Hash{}, errors.New("no wallet")
	}
	if !to.Valid() {
		return chain.Hash{}, errors.New("invalid destination address")
	}
	n.mu.Lock()
	from := n.wallet.Address()
	nonce := n.pendingStateLocked().Account(from).Nonce
	tx := &chain.Tx{
		From:   from,
		To:     to,
		Amount: amount,
		Fee:    fee,
		Nonce:  nonce,
		Time:   n.nowMs(),
		PubKey: n.wallet.PubKey(),
	}
	tx.Sig = n.wallet.Sign(tx.SigningBytes())
	n.mu.Unlock()

	if err := n.AddTx(tx, ""); err != nil {
		return chain.Hash{}, err
	}
	return tx.Hash(), nil
}

func (n *Node) BuildCandidate(miner chain.Address) *chain.Block {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.tip == nil {
		return nil
	}
	now := n.nowMs()
	if now <= n.tip.block.Time {
		now = n.tip.block.Time + 1
	}
	all := make([]*chain.Tx, 0, len(n.mempoolOrder))
	for _, h := range n.mempoolOrder {
		if t, ok := n.mempoolTx[h]; ok {
			all = append(all, t)
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Fee != all[j].Fee {
			return all[i].Fee > all[j].Fee
		}
		return all[i].Time < all[j].Time
	})
	work := n.state.Clone()
	txs := make([]*chain.Tx, 0, len(all))
	for _, t := range all {
		if err := work.ApplyTx(t); err != nil {
			continue
		}
		txs = append(txs, t)
	}
	if len(txs) > n.params.MaxTxsPerBlock-1 {
		txs = txs[:n.params.MaxTxsPerBlock-1]
	}
	var feeSum uint64
	for _, t := range txs {
		feeSum += t.Fee
	}
	coinbase := &chain.Tx{
		From:   crypto.CoinbaseAddress,
		To:     miner,
		Amount: n.params.RewardAt(n.tip.block.Height+1) + feeSum,
		Nonce:  n.tip.block.Height + 1,
		Time:   now,
	}
	blockTxs := append([]*chain.Tx{coinbase}, txs...)
	return &chain.Block{
		Height:     n.tip.block.Height + 1,
		PrevHash:   n.tip.hash,
		Time:       now,
		Difficulty: chain.NextDifficulty(n.params, n.tip.block, n.anchorLocked(n.tip)),
		Miner:      miner,
		MerkleRoot: chain.MerkleRoot(blockTxs),
		Txs:        blockTxs,
	}
}

func (n *Node) SubmitSolution(b *chain.Block) error {
	n.mu.Lock()
	err := n.insertBlockLocked(b, false, true, "")
	n.mu.Unlock()
	if err != nil {
		return err
	}
	if n.cfg.LogBlocks {
		log.Printf("mined block %d %s", b.Height, b.Hash().Short())
	}
	return nil
}

func (n *Node) GetAccount(addr chain.Address) state.Account {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.state.Account(addr)
}

func (n *Node) MempoolSize() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.mempoolTx)
}

type Info struct {
	Name       string         `json:"name"`
	Ticker     string         `json:"ticker"`
	Height     uint64         `json:"height"`
	Tip        chain.Hash     `json:"tip"`
	Difficulty uint64         `json:"difficulty"`
	NextReward uint64         `json:"next_reward"`
	Supply     uint64         `json:"supply"`
	Address    crypto.Address `json:"address"`
	Balance    uint64         `json:"balance"`
	Mempool    int            `json:"mempool"`
	Peers      int            `json:"peers"`
	Hashrate   uint64         `json:"hashrate"`
	TargetTime int64          `json:"target_block_ms"`
}

func (n *Node) Info() Info {
	n.mu.Lock()
	height := uint64(0)
	diff := uint64(0)
	var tip chain.Hash
	if n.tip != nil {
		height = n.tip.block.Height
		diff = n.tip.block.Difficulty
		tip = n.tip.hash
	}
	var supply uint64
	for _, acct := range n.state.Accounts() {
		supply += acct.Balance
	}
	balance := n.state.Account(n.WalletAddress()).Balance
	mempool := len(n.mempoolTx)
	n.mu.Unlock()

	peers := 0
	if n.sw != nil {
		peers = n.sw.PeerCount()
	}
	var hashrate uint64
	if n.hashrate != nil {
		hashrate = n.hashrate()
	}
	return Info{
		Name:       "bluGOLD",
		Ticker:     "BLG",
		Height:     height,
		Tip:        tip,
		Difficulty: diff,
		NextReward: n.params.RewardAt(height + 1),
		Supply:     supply,
		Address:    n.WalletAddress(),
		Balance:    balance,
		Mempool:    mempool,
		Peers:      peers,
		Hashrate:   hashrate,
		TargetTime: n.params.TargetBlockTime,
	}
}

type BlockSummary struct {
	Height     uint64         `json:"height"`
	Hash       chain.Hash     `json:"hash"`
	Time       int64          `json:"time"`
	Difficulty uint64         `json:"difficulty"`
	Miner      crypto.Address `json:"miner"`
	TxCount    int            `json:"tx_count"`
	Amount     uint64         `json:"coinbase_amount"`
}

func (n *Node) RecentBlocks(count int) []BlockSummary {
	n.mu.Lock()
	defer n.mu.Unlock()
	if count <= 0 {
		count = 10
	}
	if n.tip == nil {
		return nil
	}
	out := make([]BlockSummary, 0, count)
	h := n.tip.block.Height
	for i := uint64(0); i < uint64(count) && h >= i; i++ {
		hash, ok := n.chainHeights[h-i]
		if !ok {
			break
		}
		e := n.blocks[hash]
		if e == nil {
			break
		}
		amount := uint64(0)
		if len(e.block.Txs) > 0 {
			amount = e.block.Txs[0].Amount
		}
		out = append(out, BlockSummary{
			Height:     e.block.Height,
			Hash:       e.hash,
			Time:       e.block.Time,
			Difficulty: e.block.Difficulty,
			Miner:      e.block.Miner,
			TxCount:    len(e.block.Txs),
			Amount:     amount,
		})
	}
	return out
}

func (n *Node) SubscribeTips() (*chain.Block, <-chan *chain.Block, func()) {
	ch := make(chan *chain.Block, 4)
	n.mu.Lock()
	snap := n.tip
	n.tipSubs[ch] = struct{}{}
	n.mu.Unlock()
	var snapBlock *chain.Block
	if snap != nil {
		b := *snap.block
		snapBlock = &b
	}
	cancel := func() {
		n.mu.Lock()
		delete(n.tipSubs, ch)
		n.mu.Unlock()
	}
	return snapBlock, ch, cancel
}

func (n *Node) publishLocked(tip *chain.Block) {
	if n.booting {
		return
	}
	for ch := range n.tipSubs {
		select {
		case ch <- tip:
		default:
		}
	}
}

func (n *Node) onPeerConnect(p *p2p.Peer) {
	tipHeight := n.TipHeight()
	if p.Height > tipHeight {
		env, _ := wire.NewEnvelope(wire.MsgGetBlocks, wire.GetBlocksMsg{From: tipHeight + 1, Count: syncBatch})
		_ = p.Send(env)
	}
}

func (n *Node) onMessage(p *p2p.Peer, env *wire.Envelope) {
	switch env.Type {
	case wire.MsgGetBlocks:
		n.onGetBlocks(p, env)
	case wire.MsgBlocks:
		n.onBlocks(p, env)
	case wire.MsgNewBlock:
		n.onNewBlock(p, env)
	case wire.MsgNewTx:
		n.onNewTx(p, env)
	}
}

func (n *Node) onGetBlocks(p *p2p.Peer, env *wire.Envelope) {
	var req wire.GetBlocksMsg
	if json.Unmarshal(env.Payload, &req) != nil || req.Count <= 0 {
		return
	}
	if req.Count > syncBatch {
		req.Count = syncBatch
	}
	n.mu.Lock()
	var out []*chain.Block
	for h := req.From; h < req.From+uint64(req.Count); h++ {
		hash, ok := n.chainHeights[h]
		if !ok {
			break
		}
		if e := n.blocks[hash]; e != nil {
			out = append(out, e.block)
		}
	}
	n.mu.Unlock()
	if len(out) == 0 {
		return
	}
	if msg, err := wire.NewEnvelope(wire.MsgBlocks, wire.BlocksMsg{Blocks: out}); err == nil {
		_ = p.Send(msg)
	}
}

func (n *Node) onBlocks(p *p2p.Peer, env *wire.Envelope) {
	var msg wire.BlocksMsg
	if json.Unmarshal(env.Payload, &msg) != nil {
		return
	}
	before := n.TipHeight()
	for _, b := range msg.Blocks {
		n.acceptRemoteBlock(b, p)
	}
	if len(msg.Blocks) == 0 {
		return
	}
	if after := n.TipHeight(); after > before && after < p.Height {
		if gm, err := wire.NewEnvelope(wire.MsgGetBlocks, wire.GetBlocksMsg{From: after + 1, Count: syncBatch}); err == nil {
			_ = p.Send(gm)
		}
	}
}

func (n *Node) onNewBlock(p *p2p.Peer, env *wire.Envelope) {
	var msg wire.NewBlockMsg
	if json.Unmarshal(env.Payload, &msg) != nil || msg.Block == nil {
		return
	}
	n.acceptRemoteBlock(msg.Block, p)
}

func (n *Node) acceptRemoteBlock(b *chain.Block, p *p2p.Peer) {
	n.mu.Lock()
	h := b.Hash()
	if _, ok := n.seenBlock[h]; ok {
		n.mu.Unlock()
		return
	}
	n.markBlockSeen(h)
	err := n.insertBlockLocked(b, false, true, p.Dial)
	orph := errors.Is(err, ErrOrphan)
	tipHeight := uint64(0)
	if n.tip != nil {
		tipHeight = n.tip.block.Height
	}
	n.mu.Unlock()

	if n.cfg.LogBlocks && err == nil {
		log.Printf("accepted block %d %s from %s", b.Height, b.Hash().Short(), p.RemoteAddr())
	}
	if orph {
		if msg, err := wire.NewEnvelope(wire.MsgGetBlocks, wire.GetBlocksMsg{From: tipHeight + 1, Count: syncBatch}); err == nil {
			_ = p.Send(msg)
		}
	} else if err != nil {
		log.Printf("rejected block %d from %s: %v", b.Height, p.RemoteAddr(), err)
	}
}

func (n *Node) onNewTx(p *p2p.Peer, env *wire.Envelope) {
	var msg wire.NewTxMsg
	if json.Unmarshal(env.Payload, &msg) != nil || msg.Tx == nil {
		return
	}
	if err := n.AddTx(msg.Tx, p.Dial); err != nil {
		log.Printf("tx rejected: %v", err)
	}
}

func (n *Node) syncLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		tipHeight := n.TipHeight()
		if n.sw == nil {
			return
		}
		peerHeight, peer := n.sw.BestPeerHeight()
		if peer != nil && peerHeight > tipHeight {
			msg, _ := wire.NewEnvelope(wire.MsgGetBlocks, wire.GetBlocksMsg{From: tipHeight + 1, Count: syncBatch})
			_ = peer.Send(msg)
		}
	}
}
