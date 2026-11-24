// detecteur : transactions.graphe → règles de détection → alertes (Postgres + outbox → alerts.aml).
//
// Lit chaque opération APRÈS son écriture dans Neo4j (relais du graph-sink). Les opérations peuvent
// arriver hors de l'ordre chronologique (6 partitions) : fenêtres et cycles sont évalués de façon
// indépendante de l'ordre d'arrivée (voir internal/detect). Au moins une fois côté Kafka ; état Redis,
// alertes et preuves idempotents : relire un lot après une panne ne crée aucun doublon.
//
// Toute erreur arrête le lot et le réessaie : une erreur définitive bloque donc le détecteur (visible
// dans detecteur_reessais_total et les journaux) plutôt que de laisser passer des opérations sans
// les examiner. Choix assumé pour un contrôle de conformité.
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

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/detect"
	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/graph"
	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/metrics"
	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/tx"
)

const (
	group     = "detecteur"
	batchSize = 500
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
	pool, err := pgxpool.New(ctx, env("DATABASE_URL", "postgres://detection:detection@localhost:15433/grand_livre"))
	if err != nil {
		return err
	}
	defer pool.Close()
	opts, err := redis.ParseURL(env("REDIS_URL", "redis://localhost:16379/0"))
	if err != nil {
		return err
	}
	rdb := redis.NewClient(opts)
	defer rdb.Close()
	driver, err := neo4j.NewDriverWithContext(env("NEO4J_URI", "neo4j://localhost:17687"),
		neo4j.BasicAuth(env("NEO4J_USER", "neo4j"), env("NEO4J_PASSWORD", "motdepasse-local"), ""))
	if err != nil {
		return err
	}
	defer driver.Close(context.WithoutCancel(ctx))

	consumer, err := kgo.NewClient(
		kgo.SeedBrokers(strings.Split(env("KAFKA_BROKERS", "localhost:19092"), ",")...),
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(graph.Topic),
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
	srv := &http.Server{Addr: env("METRICS_ADDR", ":18092"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("serveur de métriques", "addr", srv.Addr, "err", err)
		}
	}()
	defer srv.Shutdown(context.Background())

	etat, cycles, store := detect.Etat{R: rdb}, detect.Cycles{Driver: driver}, detect.Store{Pool: pool}
	log.Info("détecteur démarré", "topic", graph.Topic)
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
			lot := make([]tx.Transaction, 0, len(chunk))
			for _, r := range chunk {
				var t tx.Transaction
				if err := json.Unmarshal(r.Value, &t); err != nil {
					m.Add("detecteur_illisibles_total", 1)
					log.Error("message illisible", "partition", r.Partition, "offset", r.Offset, "err", err)
					continue
				}
				lot = append(lot, t)
			}
			// Le lot entier est retraité en cas de panne : chaque étape est idempotente.
			if !retry(ctx, log, m, func(c context.Context) error { return traiter(c, etat, cycles, store, m, lot) }) {
				return ctx.Err()
			}
			if err := consumer.CommitRecords(context.WithoutCancel(ctx), chunk...); err != nil {
				log.Error("validation des offsets (relus sans effet grâce à l'idempotence)", "err", err)
			}
		}
		consumer.AllowRebalance()
	}
}

func traiter(ctx context.Context, etat detect.Etat, cycles detect.Cycles, store detect.Store, m *metrics.Counters, lot []tx.Transaction) error {
	var alertes []detect.Alerte
	ids := make([]string, len(lot))
	for i, t := range lot {
		a, err := etat.Fenetres(ctx, t)
		if err != nil {
			return err
		}
		alertes = append(alertes, a...)
		ids[i] = t.TransactionID
	}
	trouves, err := cycles.Chercher(ctx, ids)
	if err != nil {
		return err
	}
	alertes = append(alertes, detect.AlertesCycles(trouves)...)
	nouvelles, err := store.Enregistrer(ctx, alertes)
	if err != nil {
		return err
	}
	m.Add("detecteur_operations_total", int64(len(lot)))
	m.Add("detecteur_declenchements_total", int64(len(alertes)))
	m.Add("detecteur_alertes_nouvelles_total", int64(nouvelles))
	return nil
}

// retry réessaie fn jusqu'au succès (délai de 60 s par essai) ; false seulement si le service s'arrête.
func retry(ctx context.Context, log *slog.Logger, m *metrics.Counters, fn func(context.Context) error) bool {
	for wait := 200 * time.Millisecond; ; wait = min(2*wait, 10*time.Second) {
		attempt, cancel := context.WithTimeout(ctx, 60*time.Second)
		err := fn(attempt)
		cancel()
		if err == nil {
			return true
		}
		m.Add("detecteur_reessais_total", 1)
		log.Warn("panne, nouvel essai du lot", "err", err, "attente", wait)
		select {
		case <-ctx.Done():
			return false
		case <-time.After(wait):
		}
	}
}
