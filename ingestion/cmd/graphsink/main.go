// graph-sink : transactions.valid → Neo4j, puis relais vers transactions.graphe.
//
// Même garantie que l'ingestion : au moins une fois côté Kafka (offsets validés après l'écriture
// Neo4j ET le relais) + écriture idempotente (MERGE sur transaction_id, contrainte d'unicité).
// Le relais permet au détecteur de ne lire une opération qu'une fois qu'elle est dans le graphe :
// sa recherche de cycles voit toujours un graphe à jour.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/graph"
	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/ledger"
	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/metrics"
	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/tx"
)

const (
	group     = "graph-sink"
	batchSize = 2000 // opérations par transaction Neo4j
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, log); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("arrêt sur erreur", "err", err)
		os.Exit(1)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func run(ctx context.Context, log *slog.Logger) error {
	driver, err := neo4j.NewDriverWithContext(env("NEO4J_URI", "neo4j://localhost:17687"),
		neo4j.BasicAuth(env("NEO4J_USER", "neo4j"), env("NEO4J_PASSWORD", "motdepasse-local"), ""))
	if err != nil {
		return err
	}
	defer driver.Close(context.WithoutCancel(ctx))
	w := graph.Writer{Driver: driver}
	for wait := time.Second; ; wait = min(2*wait, 10*time.Second) {
		if err = w.EnsureSchema(ctx); err == nil {
			break
		}
		log.Warn("Neo4j injoignable au démarrage, nouvel essai", "err", err, "attente", wait)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}

	consumer, err := kgo.NewClient(
		kgo.SeedBrokers(strings.Split(env("KAFKA_BROKERS", "localhost:19092"), ",")...),
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(ledger.ValidTopic),
		kgo.DisableAutoCommit(),
		kgo.BlockRebalanceOnPoll(),
		kgo.SessionTimeout(10*time.Second),
		kgo.HeartbeatInterval(2*time.Second),
	)
	if err != nil {
		return err
	}
	defer consumer.CloseAllowingRebalance()

	producer, err := kgo.NewClient(kgo.SeedBrokers(strings.Split(env("KAFKA_BROKERS", "localhost:19092"), ",")...),
		kgo.RequiredAcks(kgo.AllISRAcks()))
	if err != nil {
		return err
	}
	defer producer.Close()

	m := metrics.New()
	mux := http.NewServeMux()
	mux.Handle("/metrics", m)
	// Chaos : panne simulée. Le graph-sink cesse de lire pendant n secondes (au plus 120), comme s'il était
	// tombé ; le grand livre, lui, continue. Réseau interne et 127.0.0.1 seulement (voir docker-compose.yml).
	var pauseJusqua atomic.Int64
	mux.HandleFunc("POST /chaos/pause", func(w http.ResponseWriter, r *http.Request) {
		// Un formulaire ou un fetch « no-cors » d'une page tierce ne peut pas poser ce Content-Type sans
		// pré-vol CORS, que ce service ne valide jamais : la pause ne peut pas être forgée depuis le navigateur.
		if strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]) != "application/json" {
			http.Error(w, "Content-Type application/json obligatoire", http.StatusUnsupportedMediaType)
			return
		}
		secondes, err := strconv.Atoi(r.URL.Query().Get("secondes"))
		if err != nil || secondes < 1 || secondes > 120 {
			http.Error(w, "secondes : entier de 1 à 120", http.StatusBadRequest)
			return
		}
		pauseJusqua.Store(time.Now().Add(time.Duration(secondes) * time.Second).UnixMilli())
		log.Warn("chaos : graph-sink en pause", "secondes", secondes)
		w.WriteHeader(http.StatusNoContent)
	})
	srv := &http.Server{Addr: env("METRICS_ADDR", ":18091"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("serveur de métriques", "addr", srv.Addr, "err", err)
		}
	}()
	defer srv.Shutdown(context.Background())

	log.Info("graph-sink démarré", "topic", ledger.ValidTopic)
	for {
		fetches := consumer.PollFetches(ctx)
		// Pause testée APRÈS la lecture : la boucle attend presque toujours dans PollFetches, et les messages
		// arrivés pendant la panne ne doivent pas passer. Ils restent en mémoire, non validés (rebalance bloqué
		// au plus 120 s : acceptable pour une panne simulée de démonstration).
		for time.Now().UnixMilli() < pauseJusqua.Load() {
			m.Set("graphsink_en_pause", 1)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(250 * time.Millisecond):
			}
		}
		m.Set("graphsink_en_pause", 0)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		fetches.EachError(func(t string, part int32, err error) {
			log.Error("lecture Kafka", "topic", t, "partition", part, "err", err)
		})
		records := fetches.Records()
		for start := 0; start < len(records); start += batchSize {
			chunk := records[start:min(start+batchSize, len(records))]
			batch := make([]tx.Transaction, 0, len(chunk))
			relais := make([]*kgo.Record, 0, len(chunk))
			for _, r := range chunk {
				var t tx.Transaction
				// Le message vient de l'outbox, déjà validé : s'il est illisible, c'est un bogue.
				// On le compte et on continue ; la réconciliation montrera l'écart.
				if err := json.Unmarshal(r.Value, &t); err != nil {
					m.Add("graphsink_illisibles_total", 1)
					log.Error("message illisible", "partition", r.Partition, "offset", r.Offset, "err", err)
					continue
				}
				batch = append(batch, t)
				relais = append(relais, &kgo.Record{Topic: graph.Topic, Key: r.Key, Value: r.Value})
			}
			ok := len(batch) == 0 || retry(ctx, log, m, "écriture Neo4j", func(c context.Context) error {
				created, err := w.Write(c, batch)
				if err == nil {
					m.Add("graphsink_ecrites_total", int64(created))
					m.Add("graphsink_doublons_absorbes_total", int64(len(batch)-created))
				}
				return err
			}) && (len(relais) == 0 || retry(ctx, log, m, "relais vers "+graph.Topic, func(c context.Context) error {
				return producer.ProduceSync(c, relais...).FirstErr()
			}))
			if !ok {
				return ctx.Err()
			}
			if err := consumer.CommitRecords(context.WithoutCancel(ctx), chunk...); err != nil {
				log.Error("validation des offsets (relus sans effet grâce à l'idempotence)", "err", err)
			}
		}
		consumer.AllowRebalance()
	}
}

// retry réessaie fn jusqu'au succès (délai de 30 s par essai) ; false seulement si le service s'arrête.
func retry(ctx context.Context, log *slog.Logger, m *metrics.Counters, quoi string, fn func(context.Context) error) bool {
	for wait := 200 * time.Millisecond; ; wait = min(2*wait, 10*time.Second) {
		attempt, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := fn(attempt)
		cancel()
		if err == nil {
			return true
		}
		m.Add("graphsink_reessais_total", 1)
		log.Warn("panne, nouvel essai", "étape", quoi, "err", err, "attente", wait)
		select {
		case <-ctx.Done():
			return false
		case <-time.After(wait):
		}
	}
}
