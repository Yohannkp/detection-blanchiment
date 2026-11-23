// graph-sink : transactions.valid → Neo4j.
//
// Même garantie que l'ingestion : au moins une fois côté Kafka (offsets validés après l'écriture
// Neo4j) + écriture idempotente (MERGE sur transaction_id, contrainte d'unicité).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
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

	m := metrics.New()
	mux := http.NewServeMux()
	mux.Handle("/metrics", m)
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
			}
			if !writeWithRetry(ctx, log, w, m, batch) {
				return ctx.Err()
			}
			if err := consumer.CommitRecords(context.WithoutCancel(ctx), chunk...); err != nil {
				log.Error("validation des offsets (relus sans effet grâce à l'idempotence)", "err", err)
			}
		}
		consumer.AllowRebalance()
	}
}

// writeWithRetry réessaie le lot jusqu'au succès ; false seulement si le service s'arrête.
func writeWithRetry(ctx context.Context, log *slog.Logger, w graph.Writer, m *metrics.Counters, batch []tx.Transaction) bool {
	if len(batch) == 0 {
		return true
	}
	for wait := 200 * time.Millisecond; ; wait = min(2*wait, 10*time.Second) {
		attempt, cancel := context.WithTimeout(ctx, 30*time.Second)
		created, err := w.Write(attempt, batch)
		cancel()
		if err == nil {
			m.Add("graphsink_ecrites_total", int64(created))
			m.Add("graphsink_doublons_absorbes_total", int64(len(batch)-created))
			return true
		}
		m.Add("graphsink_reessais_total", 1)
		log.Warn("panne Neo4j, nouvel essai", "taille", len(batch), "err", err, "attente", wait)
		select {
		case <-ctx.Done():
			return false
		case <-time.After(wait):
		}
	}
}
