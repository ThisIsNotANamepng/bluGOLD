// Package api exposes a localhost HTTP API for the CLI client commands.
package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"blugold/internal/chain"
	"blugold/internal/node"
)

func New(n *node.Node) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/info", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, n.Info())
	})
	mux.HandleFunc("GET /api/balance", func(w http.ResponseWriter, r *http.Request) {
		addr := chain.Address(r.URL.Query().Get("addr"))
		if addr == "" {
			addr = n.WalletAddress()
		}
		if !addr.Valid() {
			httpError(w, http.StatusBadRequest, "invalid address")
			return
		}
		acct := n.GetAccount(addr)
		writeJSON(w, map[string]any{
			"address": addr,
			"balance": acct.Balance,
			"nonce":   acct.Nonce,
		})
	})
	mux.HandleFunc("POST /api/send", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		var req struct {
			To     string `json:"to"`
			Amount string `json:"amount"`
			Fee    string `json:"fee"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, http.StatusBadRequest, "bad json: "+err.Error())
			return
		}
		to := chain.Address(strings.TrimSpace(req.To))
		if !to.Valid() {
			httpError(w, http.StatusBadRequest, "invalid destination address")
			return
		}
		amount, err := chain.ParseAmount(req.Amount)
		if err != nil {
			httpError(w, http.StatusBadRequest, err.Error())
			return
		}
		fee := uint64(0)
		if strings.TrimSpace(req.Fee) != "" {
			fee, err = chain.ParseAmount(req.Fee)
			if err != nil {
				httpError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		txid, err := n.Send(to, amount, fee)
		if err != nil {
			httpError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]any{"txid": txid, "amount": amount, "to": to})
	})
	mux.HandleFunc("GET /api/blocks", func(w http.ResponseWriter, r *http.Request) {
		count, _ := strconv.Atoi(r.URL.Query().Get("count"))
		if count <= 0 || count > 100 {
			count = 10
		}
		writeJSON(w, n.RecentBlocks(count))
	})
	mux.HandleFunc("GET /api/peers", func(w http.ResponseWriter, r *http.Request) {
		peers := []string{}
		if n.Switch() != nil {
			for _, p := range n.Switch().Peers() {
				peers = append(peers, p.RemoteAddr()+" -> "+p.Advert)
			}
		}
		writeJSON(w, map[string]any{"peers": peers})
	})
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	writeErr := json.NewEncoder(w).Encode(map[string]string{"error": msg})
	_ = writeErr
}
