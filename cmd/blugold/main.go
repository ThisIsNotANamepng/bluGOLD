// Command blugold is the entrypoint for the bluGOLD coin: node, miner, wallet and client.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"blugold/internal/api"
	"blugold/internal/chain"
	"blugold/internal/crypto"
	"blugold/internal/miner"
	"blugold/internal/node"
)

const (
	defaultP2PPort = 7007
	defaultAPIPort = 34335
)

func dataDir() string {
	if d := os.Getenv("BLUGOLD_DATA"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "./blugold-data"
	}
	return filepath.Join(home, ".blugold")
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "blugold: "+format+"\n", args...)
	os.Exit(1)
}

func usage() {
	fmt.Print(`bluGOLD — a stupid cryptocurrency for the UWEC cyber club

usage: blugold <command> [flags]

commands:
  init                 create a data dir + wallet
  keygen [--force]     regenerate the wallet
  address              print this wallet's address
  serve                run a node (p2p + local API), no mining
  mine [-t N] [-backend auto|cpu|gpu]   run a node and mine BLG
  send <addr> <amount> send BLG to an address (via a running node)
  balance [addr]       show balance
  info                 show chain status
  scan [N]             show the last N blocks
  version              print version

flags are per-command; run "blugold <command> -h" for help.
`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "init":
		cmdInit(os.Args[2:])
	case "keygen":
		cmdKeygen(os.Args[2:])
	case "address":
		cmdAddress(os.Args[2:])
	case "serve":
		cmdServe(os.Args[2:])
	case "mine":
		cmdMine(os.Args[2:])
	case "send":
		cmdSend(os.Args[2:])
	case "balance":
		cmdBalance(os.Args[2:])
	case "info":
		cmdInfo(os.Args[2:])
	case "scan":
		cmdScan(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("bluGOLD v0.1.0 (BLG)")
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func loadOrCreateWallet(dir string) *crypto.Wallet {
	path := crypto.WalletPath(dir)
	w, err := crypto.LoadWallet(path)
	if err == nil {
		return w
	}
	if !os.IsNotExist(err) {
		fatalf("wallet file %s is corrupt: %v (move it aside or restore it, do not regenerate: funds would be lost)", path, err)
	}
	w, err = crypto.GenerateWallet()
	if err != nil {
		fatalf("generate wallet: %v", err)
	}
	if err := crypto.SaveWallet(path, w); err != nil {
		fatalf("save wallet: %v", err)
	}
	fmt.Printf("new wallet created: %s\n", w.Address())
	return w
}

func cmdInit(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	dir := fs.String("datadir", dataDir(), "data directory")
	fs.Parse(args)
	w := loadOrCreateWallet(*dir)
	fmt.Printf("bluGOLD initialized at %s\nwallet address: %s\n", *dir, w.Address())
}

func cmdKeygen(args []string) {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	dir := fs.String("datadir", dataDir(), "data directory")
	force := fs.Bool("force", false, "overwrite existing wallet (old coins become unreachable)")
	fs.Parse(args)
	path := crypto.WalletPath(*dir)
	if _, err := crypto.LoadWallet(path); err == nil && !*force {
		fatalf("wallet already exists (use --force to replace; old coins become unreachable)")
	}
	w, err := crypto.GenerateWallet()
	if err != nil {
		fatalf("generate wallet: %v", err)
	}
	if err := crypto.SaveWallet(path, w); err != nil {
		fatalf("save wallet: %v", err)
	}
	fmt.Printf("new wallet: %s\n", w.Address())
}

func cmdAddress(args []string) {
	fs := flag.NewFlagSet("address", flag.ExitOnError)
	dir := fs.String("datadir", dataDir(), "data directory")
	fs.Parse(args)
	w, err := crypto.LoadWallet(crypto.WalletPath(*dir))
	if err != nil {
		fatalf("no wallet (run: blugold init): %v", err)
	}
	fmt.Println(w.Address())
}

type nodeOpts struct {
	dir       string
	p2pListen string
	apiListen string
	adv       string
	seeds     []string
	noSync    bool
	chatty    bool
}

func parseNodeFlags(fs *flag.FlagSet, args []string) *nodeOpts {
	dir := fs.String("datadir", dataDir(), "data directory")
	p2pListen := fs.String("p2p", fmt.Sprintf(":%d", defaultP2PPort), "p2p listen address")
	advertise := fs.String("adv", "", "advertise address host:port told to peers (set to your public IP on NAT'd/cloud hosts)")
	apiListen := fs.String("api", fmt.Sprintf("127.0.0.1:%d", defaultAPIPort), "local api listen address")
	seedList := fs.String("seed", "", "comma-separated seed peers host:port")
	noSync := fs.Bool("nosync", false, "disable p2p networking")
	chatty := fs.Bool("v", false, "verbose block logs")
	fs.Parse(args)

	var seeds []string
	for _, s := range strings.Split(*seedList, ",") {
		if s = strings.TrimSpace(s); s != "" {
			seeds = append(seeds, s)
		}
	}
	return &nodeOpts{
		dir:       *dir,
		p2pListen: *p2pListen,
		apiListen: *apiListen,
		adv:       *advertise,
		seeds:     seeds,
		noSync:    *noSync,
		chatty:    *chatty,
	}
}

func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	opts := parseNodeFlags(fs, args)
	if fs.NArg() > 0 {
		fs.Usage()
		os.Exit(2)
	}
	runNode(opts, 0, miner.BackendAuto, 0)
}

func cmdMine(args []string) {
	fs := flag.NewFlagSet("mine", flag.ExitOnError)
	threads := fs.Int("t", 1, "CPU mining threads (ignored if the GPU backend is used)")
	backendFlag := fs.String("backend", "auto", "mining backend: auto, cpu, or gpu (auto benchmarks both and picks the faster; gpu needs a build with \"-tags gpu\")")
	gpuBatch := fs.Int("gpu-batch", 0, "nonces per GPU dispatch (0 = default)")
	opts := parseNodeFlags(fs, args)
	if fs.NArg() > 0 {
		fs.Usage()
		os.Exit(2)
	}
	if *threads < 1 {
		fatalf("-t must be at least 1")
	}
	backend, err := miner.ParseBackend(*backendFlag)
	if err != nil {
		fatalf("%v", err)
	}
	runNode(opts, *threads, backend, *gpuBatch)
}

// runNode boots the node, the local API and (if threads > 0) a miner, then
// blocks until SIGINT/SIGTERM.
func runNode(opts *nodeOpts, threads int, backend miner.Backend, gpuBatch int) {
	w := loadOrCreateWallet(opts.dir)
	adv := opts.adv
	if adv == "" {
		adv = discoverAdvertise(opts.p2pListen)
	}

	n, err := node.New(node.Config{
		Params:    chain.DefaultParams(),
		DataDir:   opts.dir,
		Listen:    opts.p2pListen,
		Advertise: adv,
		Seeds:     opts.seeds,
		Wallet:    w,
		PeerSync:  !opts.noSync,
		LogBlocks: opts.chatty,
		MaxPeers:  32,
	})
	if err != nil {
		fatalf("start node: %v", err)
	}
	defer n.Stop()

	apiSrv := &http.Server{Addr: opts.apiListen, Handler: api.New(n)}
	go func() {
		if err := apiSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fatalf("api: %v", err)
		}
	}()

	mining := threads > 0
	var mCancel context.CancelFunc
	var minerBackend string
	if mining {
		m := miner.New(n, w, threads, 0, backend, gpuBatch)
		m.EnsureBackend() // benchmark/pick now so the banner below reflects reality
		minerBackend = m.BackendName()
		n.SetHashrateSource(m.Hashrate)
		var mctx context.Context
		mctx, mCancel = context.WithCancel(context.Background())
		go m.Run(mctx)
	}

	fmt.Printf(`
      ____  _              ____  ____
     | __ )| | ___   __ _ / ___|/ ___|
     |  _ \| |/ _ \ / _' | |  _ \___ \
     | |_) | | (_) | (_| | |_| |___) |
     |____/|_|\___/ \__, |\____|____/
                     |___/  BLG
`)
	fmt.Printf("address:    %s\n", w.Address())
	fmt.Printf("data dir:   %s\n", opts.dir)
	if n.Switch() != nil {
		fmt.Printf("p2p:        %s (advertised %s)\n", n.Switch().Addr(), adv)
	} else {
		fmt.Printf("p2p:        off (--nosync)\n")
	}
	fmt.Printf("api:        http://%s\n", opts.apiListen)
	if mining {
		if strings.HasPrefix(minerBackend, "gpu") {
			fmt.Printf("mining:     %s backend\n", minerBackend)
		} else {
			fmt.Printf("mining:     %s backend (%d threads)\n", minerBackend, threads)
		}
	} else {
		fmt.Printf("mining:     off (run: blugold mine)\n")
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	fmt.Println("\nshutting down...")
	if mCancel != nil {
		mCancel()
	}
	_ = apiSrv.Close()
}

func discoverAdvertise(listen string) string {
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		port = strconv.Itoa(defaultP2PPort)
	}
	ip := detectLANIP()
	if ip == "" {
		return listen
	}
	return net.JoinHostPort(ip, port)
}

func detectLANIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:53")
	if err != nil {
		return ""
	}
	defer conn.Close()
	addr := conn.LocalAddr().(*net.UDPAddr)
	return addr.IP.String()
}

func apiClient(api string) *http.Client {
	return &http.Client{Timeout: 5 * time.Second}
}

func apiGet(api, path string, out any) error {
	resp, err := apiClient(api).Get(strings.TrimRight(api, "/") + path)
	if err != nil {
		return fmt.Errorf("node unreachable at %s (is it running?): %w", api, err)
	}
	defer resp.Body.Close()
	return decode(resp, out)
}

func apiPost(api, path string, body any, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := apiClient(api).Post(strings.TrimRight(api, "/")+path, "application/json", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("node unreachable at %s (is it running?): %w", api, err)
	}
	defer resp.Body.Close()
	return decode(resp, out)
}

func decode(resp *http.Response, out any) error {
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Error != "" {
			return fmt.Errorf("%s", e.Error)
		}
		return fmt.Errorf("http %d: %s", resp.StatusCode, string(data))
	}
	return json.Unmarshal(data, out)
}

func cmdSend(args []string) {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	api := fs.String("api", fmt.Sprintf("http://127.0.0.1:%d", defaultAPIPort), "node api url")
	fee := fs.String("fee", "0", "miner fee in BLG")
	fs.Parse(args)
	if fs.NArg() != 2 {
		fatalf("usage: blugold send <address> <amount>")
	}
	to := fs.Arg(0)
	amount, err := chain.ParseAmount(fs.Arg(1))
	if err != nil {
		fatalf("%v", err)
	}
	feeAmt, err := chain.ParseAmount(*fee)
	if err != nil {
		fatalf("%v", err)
	}
	var out struct {
		TxID chain.Hash `json:"txid"`
	}
	if err := apiPost(*api, "/api/send", map[string]string{"to": to, "amount": chain.FormatAmount(amount), "fee": chain.FormatAmount(feeAmt)}, &out); err != nil {
		fatalf("%v", err)
	}
	fmt.Printf("sent %s BLG to %s\ntxid: %s\n", chain.FormatAmount(amount), to, out.TxID)
}

func cmdBalance(args []string) {
	fs := flag.NewFlagSet("balance", flag.ExitOnError)
	api := fs.String("api", fmt.Sprintf("http://127.0.0.1:%d", defaultAPIPort), "node api url")
	fs.Parse(args)
	addr := ""
	if fs.NArg() > 0 {
		addr = fs.Arg(0)
	}
	var out struct {
		Address string `json:"address"`
		Balance uint64 `json:"balance"`
		Nonce   uint64 `json:"nonce"`
	}
	q := "/api/balance"
	if addr != "" {
		q += "?addr=" + addr
	}
	if err := apiGet(*api, q, &out); err != nil {
		fatalf("%v", err)
	}
	fmt.Printf("%s  %s BLG (nonce %d)\n", out.Address, chain.FormatAmount(out.Balance), out.Nonce)
}

func cmdInfo(args []string) {
	fs := flag.NewFlagSet("info", flag.ExitOnError)
	api := fs.String("api", fmt.Sprintf("http://127.0.0.1:%d", defaultAPIPort), "node api url")
	fs.Parse(args)
	var info node.Info
	if err := apiGet(*api, "/api/info", &info); err != nil {
		fatalf("%v", err)
	}
	fmt.Printf(`bluGOLD (BLG)
height:     %d
tip:        %s
difficulty: %d
reward:     %s BLG
supply:     %s BLG
hashrate:   %s H/s
address:    %s
balance:    %s BLG
mempool:    %d txs
peers:      %d
`,
		info.Height, info.Tip.Short(), info.Difficulty, chain.FormatAmount(info.NextReward),
		chain.FormatAmount(info.Supply), formatHashrate(info.Hashrate), info.Address,
		chain.FormatAmount(info.Balance), info.Mempool, info.Peers)
}

func formatHashrate(h uint64) string {
	switch {
	case h >= 1_000_000_000:
		return fmt.Sprintf("%.2fG", float64(h)/1e9)
	case h >= 1_000_000:
		return fmt.Sprintf("%.2fM", float64(h)/1e6)
	case h >= 1_000:
		return fmt.Sprintf("%.2fk", float64(h)/1e3)
	default:
		return strconv.FormatUint(h, 10)
	}
}

func cmdScan(args []string) {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	api := fs.String("api", fmt.Sprintf("http://127.0.0.1:%d", defaultAPIPort), "node api url")
	fs.Parse(args)
	count := 10
	if fs.NArg() > 0 {
		n, err := strconv.Atoi(fs.Arg(0))
		if err != nil || n <= 0 {
			fatalf("scan count must be a positive integer")
		}
		count = n
	}
	var blocks []node.BlockSummary
	if err := apiGet(*api, fmt.Sprintf("/api/blocks?count=%d", count), &blocks); err != nil {
		fatalf("%v", err)
	}
	if len(blocks) == 0 {
		fmt.Println("no blocks yet")
		return
	}
	fmt.Printf("%-7s %-16s %-13s %-11s %-34s %s\n", "HEIGHT", "AGE", "HASH", "DIFF", "MINER", "COINBASE")
	for _, b := range blocks {
		age := time.Since(time.UnixMilli(b.Time)).Round(time.Second)
		fmt.Printf("%-7d %-16s %-13s %-11d %-34s %s BLG\n",
			b.Height, age, b.Hash.Short(), b.Difficulty, shortAddr(b.Miner), chain.FormatAmount(b.Amount))
	}
}

func shortAddr(a crypto.Address) string {
	s := string(a)
	if len(s) > 12 {
		return s[:8] + "..." + s[len(s)-4:]
	}
	return s
}
