// Command mock-server is a synthetic, resettable orders API used to
// demonstrate FlowGuard's idempotency scenario safely and reproducibly
// (T06). It intentionally reproduces the "retry causes a double charge"
// bug by default; pass -safe to see the fixed behavior instead.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"sync"
)

type store struct {
	mu      sync.Mutex
	effects map[string]int // idempotency key -> number of order-create effects applied
	safe    bool
}

func newStore(safe bool) *store {
	return &store{effects: map[string]int{}, safe: safe}
}

func (s *store) createOrder(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.safe && key != "" && s.effects[key] > 0 {
		// Already processed this key: return the same result without a new effect.
		writeJSON(w, http.StatusOK, map[string]any{"order_id": key, "status": "ok", "replayed": true})
		return
	}

	s.effects[key]++
	writeJSON(w, http.StatusOK, map[string]any{"order_id": key, "status": "ok", "replayed": false})
}

func (s *store) businessEffectCount(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")

	s.mu.Lock()
	count := s.effects[key]
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{"business_effect_count": count})
}

func (s *store) reset(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.effects = map[string]int{}
	s.mu.Unlock()

	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address")
	safe := flag.Bool("safe", false, "enable idempotent (bug-fixed) behavior instead of the default buggy one")
	flag.Parse()

	s := newStore(*safe)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /orders", s.createOrder)
	mux.HandleFunc("GET /_test/business-effect-count", s.businessEffectCount)
	mux.HandleFunc("POST /_test/reset", s.reset)

	log.Printf("mock-server listening on %s (safe=%v)", *addr, *safe)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
