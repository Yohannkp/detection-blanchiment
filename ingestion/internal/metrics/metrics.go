// Package metrics expose des compteurs au format texte Prometheus, sans dépendance.
// shortcut: compteurs seulement (pas d'histogrammes) ; passer à prometheus/client_golang
// quand le projet observabilité branchera Prometheus.
package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"sync"
)

type Counters struct {
	mu sync.Mutex
	v  map[string]int64
}

func New() *Counters { return &Counters{v: map[string]int64{}} }

// Add incrémente la série name{labels}, par ex. Add(`ingestion_dlq_total{reason="iban_invalide"}`, 1).
func (c *Counters) Add(series string, n int64) {
	c.mu.Lock()
	c.v[series] += n
	c.mu.Unlock()
}

// Set fixe une jauge (valeur instantanée, pas un compteur).
func (c *Counters) Set(series string, v int64) {
	c.mu.Lock()
	c.v[series] = v
	c.mu.Unlock()
}

func (c *Counters) Get(series string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.v[series]
}

func (c *Counters) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	c.mu.Lock()
	keys := make([]string, 0, len(c.v))
	for k := range c.v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	for _, k := range keys {
		fmt.Fprintf(w, "%s %d\n", k, c.v[k])
	}
	c.mu.Unlock()
}
