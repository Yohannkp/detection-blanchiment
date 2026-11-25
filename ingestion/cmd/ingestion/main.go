// Service d'ingestion : transactions.raw → grand livre Postgres (+ outbox) ou transactions.dlq,
// puis relais de l'outbox vers transactions.valid.
//
// Garantie : au moins une fois côté Kafka (offset validé APRÈS le commit SQL) + écriture
// idempotente côté Postgres (clé primaire transaction_id) ⇒ chaque opération compte une seule fois.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/ingest"
	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/ledger"
	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/metrics"
	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/tx"
	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/wire"
)

const (
	rawTopic = "transactions.raw"
	dlqTopic = "transactions.dlq"
	group    = "ingestion"
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
	pool, err := pgxpool.New(ctx, env("DATABASE_URL", "postgres://ingestion:ingestion@localhost:15433/grand_livre"))
	if err != nil {
		return err
	}
	defer pool.Close()

	brokers := strings.Split(env("KAFKA_BROKERS", "localhost:19092"), ",")
	consumer, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(rawTopic),
		kgo.DisableAutoCommit(),    // on valide nous-mêmes, après le commit SQL
		kgo.BlockRebalanceOnPoll(), // pas de rééquilibrage entre le traitement et la validation
		// Après un arrêt brutal, le groupe attend l'expiration de la session de l'ancien membre
		// avant de redonner ses partitions : 10 s au lieu de 45 s par défaut (mesuré : ~45 s de reprise).
		kgo.SessionTimeout(10*time.Second),
		kgo.HeartbeatInterval(2*time.Second),
	)
	if err != nil {
		return err
	}
	// Close seul bloquerait : BlockRebalanceOnPoll exige d'autoriser le rééquilibrage pour quitter le groupe.
	defer consumer.CloseAllowingRebalance()

	producer, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.RequiredAcks(kgo.AllISRAcks()))
	if err != nil {
		return err
	}
	defer producer.Close()

	m := metrics.New()
	store := ledger.Store{Pool: pool}
	p := ingest.Processor{
		Store:   store,
		Schemas: wire.NewRegistry(env("SCHEMA_REGISTRY_URL", "http://localhost:18081"), rawTopic+"-value"),
		DLQ:     dlq{producer},
		Metrics: m,
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", m)
	srv := &http.Server{Addr: env("METRICS_ADDR", ":18090"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("serveur de métriques", "addr", srv.Addr, "err", err)
		}
	}()
	defer srv.Shutdown(context.Background())

	go relay(ctx, log, store, producer, m)

	log.Info("ingestion démarrée", "topic", rawTopic)
	for {
		fetches := consumer.PollFetches(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		fetches.EachError(func(t string, part int32, err error) {
			log.Error("lecture Kafka", "topic", t, "partition", part, "err", err)
		})
		// Lots de 500 messages : une transaction SQL (un COMMIT) par lot au lieu d'une par message.
		records := fetches.Records()
		for debut := 0; debut < len(records) && ctx.Err() == nil; debut += 500 {
			lot := records[debut:min(debut+500, len(records))]
			msgs := make([]ingest.Message, len(lot))
			for i, r := range lot {
				msgs[i] = ingest.Message{Value: r.Value, Partition: r.Partition, Offset: r.Offset}
			}
			// Une panne bloque le lot (jamais d'offset sauté) jusqu'au retour du service ; le lot entier est
			// réessayé, sans effet grâce à l'idempotence.
			ok := false
			for wait := 200 * time.Millisecond; ; wait = min(2*wait, 10*time.Second) {
				// Délai par essai : sans lui, une base injoignable bloque l'appel sans bruit (constaté).
				attempt, cancel := context.WithTimeout(ctx, 30*time.Second)
				err := p.ProcessBatch(attempt, msgs)
				cancel()
				if ok = err == nil; ok {
					break
				}
				m.Add("ingestion_reessais_total", 1)
				log.Warn("panne, nouvel essai du lot", "taille", len(lot), "err", err, "attente", wait)
				select {
				case <-ctx.Done():
				case <-time.After(wait):
				}
				if ctx.Err() != nil {
					break
				}
			}
			if !ok {
				break // arrêt pendant une panne : rien de commité, rien à valider
			}
			// Contexte détaché : on valide ce qui est commité en base même si l'arrêt arrive à cet instant.
			if err := consumer.CommitRecords(context.WithoutCancel(ctx), lot...); err != nil {
				log.Error("validation des offsets (les messages seront relus, sans effet grâce à l'idempotence)", "err", err)
			}
			if ctx.Err() != nil {
				break
			}
		}
		consumer.AllowRebalance()
	}
}

// dlq publie le message d'origine, intact, avec la raison et sa position en en-têtes.
type dlq struct{ c *kgo.Client }

func (d dlq) Send(ctx context.Context, m ingest.Message, r *tx.Rejection) error {
	rec := &kgo.Record{Topic: dlqTopic, Value: m.Value, Headers: []kgo.RecordHeader{
		{Key: "dlq.reason", Value: []byte(r.Reason)},
		{Key: "dlq.detail", Value: []byte(r.Detail)},
		{Key: "dlq.source", Value: []byte(rawTopic + "/" + strconv.Itoa(int(m.Partition)) + "/" + strconv.FormatInt(m.Offset, 10))},
	}}
	return d.c.ProduceSync(ctx, rec).FirstErr()
}

// relay publie l'outbox vers transactions.valid toutes les 200 ms (par lots de 500).
func relay(ctx context.Context, log *slog.Logger, store ledger.Store, producer *kgo.Client, m *metrics.Counters) {
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		for {
			attempt, cancel := context.WithTimeout(ctx, 10*time.Second)
			n, err := store.PublishOutbox(attempt, 500, func(msgs []ledger.OutboxMessage) error {
				recs := make([]*kgo.Record, len(msgs))
				for i, o := range msgs {
					recs[i] = &kgo.Record{Topic: o.Topic, Key: []byte(o.Key), Value: o.Payload}
				}
				return producer.ProduceSync(attempt, recs...).FirstErr()
			})
			cancel()
			if err != nil {
				log.Warn("relais de l'outbox", "err", err)
				break
			}
			m.Add("outbox_publies_total", int64(n))
			if n < 500 {
				break
			}
		}
	}
}
