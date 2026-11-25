// chaos : service de la console Chaos de Filature. Il joue à la demande les scénarios de
// internal/scenarios (montages de blanchiment, piège à faux positif, doublons, messages corrompus) sur
// transactions.raw, et simule la panne du graph-sink. Chaque réponse dit quelles alertes le scénario doit
// faire lever (et, pour le piège, lesquelles il ne doit pas faire lever) : l'interface vérifie en direct.
//
// Démonstration locale : écoute sur le réseau Docker et 127.0.0.1 seulement. Comme l'API, il exige un
// vrai JSON et une origine autorisée (pas de requête forgée depuis une page tierce).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/scenarios"
	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/wire"
)

var catalogue = []struct {
	Nom, Titre string
}{
	{"schtroumpfage", "Schtroumpfage sous le seuil COSI"},
	{"cycle", "Cycle par des sociétés écrans"},
	{"paie", "Piège : une paie de 80 salaires"},
	{"doublons", "100 doublons réseau"},
	{"corrompus", "20 messages corrompus"},
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, log); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, http.ErrServerClosed) {
		log.Error("arrêt sur erreur", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, log *slog.Logger) error {
	origines := strings.Split(env("ORIGINES", "http://localhost:3100"), ",")
	graphSink := env("GRAPHSINK_URL", "http://localhost:18091")
	registre := wire.NewRegistry(env("SCHEMA_REGISTRY_URL", "http://localhost:18081"), "transactions.raw-value")
	producteur, err := kgo.NewClient(kgo.SeedBrokers(strings.Split(env("KAFKA_BROKERS", "localhost:19092"), ",")...),
		kgo.RequiredAcks(kgo.AllISRAcks()))
	if err != nil {
		return err
	}
	defer producteur.Close()

	// Garde commune : CORS pour l'interface, et refus des requêtes qui ne viennent pas d'elle.
	garde := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			o := r.Header.Get("Origin")
			if o != "" && !slices.Contains(origines, o) {
				http.Error(w, "origine non autorisée", http.StatusForbidden)
				return
			}
			if o != "" {
				w.Header().Set("Access-Control-Allow-Origin", o)
				w.Header().Set("Vary", "Origin")
			}
			if r.Method == http.MethodOptions {
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST")
				w.Header().Set("Access-Control-Allow-Headers", "content-type")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if r.Method == http.MethodPost && strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]) != "application/json" {
				http.Error(w, "Content-Type application/json obligatoire", http.StatusUnsupportedMediaType)
				return
			}
			h(w, r)
		}
	}
	repondre := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/scenarios", garde(func(w http.ResponseWriter, _ *http.Request) { repondre(w, 200, catalogue) }))
	mux.HandleFunc("/scenarios/{nom}", garde(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST attendu", http.StatusMethodNotAllowed)
			return
		}
		id, err := registre.Latest()
		if err != nil {
			http.Error(w, "registre de schémas injoignable", http.StatusServiceUnavailable)
			return
		}
		res, err := scenarios.Nouveau(id).Par(r.PathValue("nom"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		c, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if err := producteur.ProduceSync(c, scenarios.Records(res)...).FirstErr(); err != nil {
			http.Error(w, "Kafka injoignable : "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		log.Info("scénario joué", "nom", res.Nom, "messages", res.NbMsg)
		repondre(w, 200, res)
	}))
	mux.HandleFunc("/pannes/graphe", garde(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST attendu", http.StatusMethodNotAllowed)
			return
		}
		var corps struct{ Secondes int }
		if err := json.NewDecoder(r.Body).Decode(&corps); err != nil || corps.Secondes < 1 || corps.Secondes > 120 {
			http.Error(w, `corps attendu : {"secondes": 1 à 120}`, http.StatusBadRequest)
			return
		}
		c, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(c, http.MethodPost, fmt.Sprintf("%s/chaos/pause?secondes=%d", graphSink, corps.Secondes), strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		rep, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, "graph-sink injoignable : "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		rep.Body.Close()
		if rep.StatusCode != http.StatusNoContent {
			http.Error(w, fmt.Sprintf("le graph-sink a refusé la pause (HTTP %d)", rep.StatusCode), http.StatusBadGateway)
			return
		}
		log.Warn("panne simulée du graph-sink", "secondes", corps.Secondes)
		repondre(w, 200, map[string]any{"nom": "panne", "secondes": corps.Secondes,
			"resume": fmt.Sprintf("le graph-sink cesse de lire pendant %d s ; le grand livre continue", corps.Secondes)})
	}))

	srv := &http.Server{Addr: env("ADRESSE", ":18110"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { <-ctx.Done(); _ = srv.Shutdown(context.Background()) }()
	log.Info("service chaos démarré", "adresse", srv.Addr)
	return srv.ListenAndServe()
}
