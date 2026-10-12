package main

import (
	"encoding/json"
	"log"
	"math/rand"
	"net/http"
	"os"
	"sync"
	"time"
)

type store struct {
	sync.Mutex
	refs map[string]string
}

func main() {
	s := &store{refs: make(map[string]string)}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/vouchers", s.post)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	addr := os.Getenv("ACCOUNTING_ADDR")
	if addr == "" {
		addr = ":8081"
	}
	log.Printf("mock-accounting listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func (s *store) post(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ClientRef   string `json:"client_ref"`
		AmountPaise int64  `json:"amount_paise"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.ClientRef == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "client_ref is required"})
		return
	}

	s.Lock()
	if existingRef := s.refs[in.ClientRef]; existingRef != "" {
		s.Unlock()
		log.Printf("[mock-accounting] Duplicate client_ref %s -> returning 409 with external_ref %s", in.ClientRef, existingRef)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"external_ref": existingRef})
		return
	}

	mode := r.Header.Get("X-Mock-Mode")
	if mode == "" {
		mode = os.Getenv("MOCK_MODE")
	}

	// In normal mode: 20% simulated HTTP 500, 10% simulated 5s delay followed by success
	n := rand.Intn(100)
	if mode == "deterministic-success" {
		n = 50 // guarantees immediate success
	} else if mode == "deterministic-fail" {
		n = 10 // guarantees 500
	} else if mode == "deterministic-delay" {
		n = 25 // guarantees 5s delay
	}

	if n < 20 {
		s.Unlock()
		log.Printf("[mock-accounting] Simulated 500 failure for %s", in.ClientRef)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "simulated accounting failure"})
		return
	}

	ref := "acct-" + time.Now().UTC().Format("20060102150405.000000000")
	s.refs[in.ClientRef] = ref
	s.Unlock()

	if n < 30 {
		log.Printf("[mock-accounting] Simulating 5s delay for %s", in.ClientRef)
		time.Sleep(5 * time.Second)
	}

	log.Printf("[mock-accounting] Successfully accepted %s -> %s", in.ClientRef, ref)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{"external_ref": ref})
}
