package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"blugold/internal/chain"
	"blugold/internal/crypto"
	"blugold/internal/miner"
	"blugold/internal/node"
)

func testNode(t *testing.T, mine bool) (*node.Node, *crypto.Wallet, context.CancelFunc) {
	t.Helper()
	w, _ := crypto.GenerateWallet()
	n, err := node.New(node.Config{Params: chain.TestParams(), DataDir: t.TempDir(), Wallet: w})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(n.Stop)
	cancel := func() {}
	if mine {
		m := miner.New(n, w, 1, 50*time.Millisecond, miner.BackendCPU, 0)
		ctx, c := context.WithCancel(context.Background())
		cancel = c
		go m.Run(ctx)
	}
	return n, w, cancel
}

func httpGet(t *testing.T, url string) string {
	t.Helper()
	r, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	data, _ := io.ReadAll(r.Body)
	return string(data)
}

func httpPost(t *testing.T, url, body string) string {
	t.Helper()
	r, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	data, _ := io.ReadAll(r.Body)
	return string(data)
}

func TestInfoEndpoint(t *testing.T) {
	n, _, _ := testNode(t, false)
	srv := httptest.NewServer(New(n))
	defer srv.Close()

	resp := httpGet(t, srv.URL+"/api/info")
	if !strings.Contains(resp, `"height": 0`) {
		t.Fatalf("unexpected info: %s", resp)
	}
}

func TestSendEndpoint(t *testing.T) {
	n, w, cancel := testNode(t, true)
	w2, _ := crypto.GenerateWallet()

	srv := httptest.NewServer(New(n))
	defer srv.Close()

	waitForBalance(t, n, w.Address(), 3*chain.TestParams().RewardAt(1))
	cancel()

	resp := httpPost(t, srv.URL+"/api/send", fmt.Sprintf(`{"to": %q, "amount": "1.5"}`, w2.Address()))
	if !strings.Contains(resp, "txid") {
		t.Fatalf("send failed: %s", resp)
	}
	if n.MempoolSize() != 1 {
		t.Fatalf("mempool = %d", n.MempoolSize())
	}

	balResp := httpGet(t, srv.URL+"/api/balance?addr="+string(w2.Address()))
	if strings.Contains(balResp, "error") {
		t.Fatalf("balance request failed: %s", balResp)
	}
}

func waitForBalance(t *testing.T, n *node.Node, addr crypto.Address, want uint64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if n.GetAccount(addr).Balance >= want {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("balance never reached %d", want)
}

func TestSendBadAmount(t *testing.T) {
	n, _, _ := testNode(t, false)
	w2, _ := crypto.GenerateWallet()
	srv := httptest.NewServer(New(n))
	defer srv.Close()

	resp := httpPost(t, srv.URL+"/api/send", fmt.Sprintf(`{"to": %q, "amount": "notanumber"}`, w2.Address()))
	if !strings.Contains(resp, "error") {
		t.Fatalf("expected error, got: %s", resp)
	}
	resp = httpPost(t, srv.URL+"/api/send", `{"to": "garbage", "amount": "1"}`)
	if !strings.Contains(resp, "error") {
		t.Fatalf("expected address error, got: %s", resp)
	}
}

func TestBlocksEndpoint(t *testing.T) {
	n, _, _ := testNode(t, false)
	srv := httptest.NewServer(New(n))
	defer srv.Close()
	resp := httpGet(t, srv.URL+"/api/blocks?count=5")
	if !strings.Contains(resp, `"height": 0`) {
		t.Fatalf("blocks response: %s", resp)
	}
}

func TestLeaderboardEndpoint(t *testing.T) {
	n, w1, _ := testNode(t, false)
	w2, _ := crypto.GenerateWallet()
	for i := 0; i < 2; i++ {
		b := n.BuildCandidate(w1.Address())
		if b == nil {
			t.Fatal("missing candidate for first miner")
		}
		if err := n.SubmitSolution(b); err != nil {
			t.Fatal(err)
		}
	}
	b := n.BuildCandidate(w2.Address())
	if b == nil {
		t.Fatal("missing candidate for second miner")
	}
	if err := n.SubmitSolution(b); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(New(n))
	defer srv.Close()
	resp := httpGet(t, srv.URL+"/api/leaderboard?limit=2")
	first := strings.Index(resp, string(w1.Address()))
	second := strings.Index(resp, string(w2.Address()))
	if first < 0 || second < 0 || first > second {
		t.Fatalf("expected richest wallet to rank first: %s", resp)
	}
	if !strings.Contains(resp, `"balance": 200000000`) {
		t.Fatalf("expected wallet balances: %s", resp)
	}

	resp = httpGet(t, srv.URL+"/api/leaderboard?limit=2&mode=miners")
	if !strings.Contains(resp, `"blocks": 2`) || !strings.Contains(resp, `"earned": 200000000`) {
		t.Fatalf("expected miner totals: %s", resp)
	}
}
