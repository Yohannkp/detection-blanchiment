package detect

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/tx"
)

// Etat garde dans Redis les fenêtres glissantes de la détection. Tout y est idempotent (ensembles
// indexés par transaction_id) : retraiter une opération ne change rien, et l'état survit à un
// redémarrage du détecteur (Redis en appendonly).
//
// Le temps est celui de l'ÉVÉNEMENT (booked_at). Les opérations n'arrivent pas forcément dans
// l'ordre chronologique (6 partitions, rejeu accéléré) : chaque opération réévalue TOUTES les
// fenêtres qui la contiennent, y compris celles qui se terminent après elle. Le résultat final ne
// dépend donc pas de l'ordre d'arrivée, tant qu'une opération n'a pas plus de RetardTolere de retard
// sur les plus récentes de son compte.
type Etat struct{ R *redis.Client }

const (
	// RetardTolere : au-delà, une fenêtre a pu être élaguée avant l'arrivée de l'opération en retard.
	// En direct, le retard réel se compte en millisecondes ; en rejeu accéléré, en heures d'horloge simulée.
	RetardTolere = 24 * time.Hour
	// DureeVie : une clé Redis sans écriture pendant 30 jours (horloge réelle) est oubliée.
	DureeVie = 30 * 24 * time.Hour
)

type cote struct {
	regle, compte, cle, cleHisto, contrepartie string
	seuil                                      int
}

// Fenetres applique fan-in, fan-out et espèces fractionnées à une opération.
func (e Etat) Fenetres(ctx context.Context, t tx.Transaction) ([]Alerte, error) {
	debit, credit := t.Accounts()
	T := t.BookedAt.Unix()
	W, L := int64(FenetreDegre.Seconds()), int64(RetardTolere.Seconds())
	m := membre(t.TransactionID, "", t.AmountMinor)

	var cotes []cote
	if credit != tx.CashAccount {
		cotes = append(cotes, cote{FanIn, credit, "in:" + credit, "degin:" + credit, debit, SeuilFanIn})
	}
	if debit != tx.CashAccount {
		cotes = append(cotes, cote{FanOut, debit, "out:" + debit, "degout:" + debit, credit, SeuilFanOut})
	}

	p := e.R.TxPipeline()
	type lecture struct {
		fenetre *redis.ZSliceCmd
		histo   *redis.IntCmd
	}
	lectures := make([]lecture, len(cotes))
	for i, c := range cotes {
		p.ZAdd(ctx, c.cle, redis.Z{Score: float64(T), Member: membre(t.TransactionID, c.contrepartie, t.AmountMinor)})
		p.ZRemRangeByScore(ctx, c.cle, "-inf", "("+strconv.FormatInt(T-W-L, 10))
		p.SAdd(ctx, c.cleHisto, c.contrepartie)
		// Toutes les fenêtres (fin − 96 h, fin] qui contiennent T ont leur fin dans [T, T + 96 h) :
		// on lit donc (T − 96 h, T + 96 h).
		lectures[i] = lecture{
			p.ZRangeByScoreWithScores(ctx, c.cle, &redis.ZRangeBy{Min: "(" + strconv.FormatInt(T-W, 10), Max: "(" + strconv.FormatInt(T+W, 10)}),
			p.SCard(ctx, c.cleHisto),
		}
		p.Expire(ctx, c.cle, DureeVie)
		p.Expire(ctx, c.cleHisto, DureeVie)
	}
	var depots *redis.StringSliceCmd
	cleEspeces := ""
	if t.PaymentType == "cash_deposit" && t.Currency == "EUR" {
		cleEspeces = fmt.Sprintf("especes:%s:%s", credit, t.BookedAt.UTC().Format("2006-01"))
		p.ZAdd(ctx, cleEspeces, redis.Z{Score: float64(T), Member: m})
		depots = p.ZRange(ctx, cleEspeces, 0, -1) // tout le mois civil, quel que soit l'ordre d'arrivée
		p.Expire(ctx, cleEspeces, DureeVie)
	}
	if _, err := p.Exec(ctx); err != nil {
		return nil, err
	}

	var alertes []Alerte
	for i, c := range cotes {
		fenetres, err := evaluerFenetres(lectures[i].fenetre.Val(), T, lectures[i].histo.Val(), c.seuil)
		if err != nil {
			return nil, err
		}
		for _, f := range fenetres {
			alertes = append(alertes, nouvelleAlerte(c.regle, c.compte, f.fin, FenetreDegre, t.TransactionID, f.preuves))
		}
	}
	if depots != nil {
		preuves, err := evaluerEspeces(depots.Val())
		if err != nil {
			return nil, err
		}
		if preuves != nil {
			mois := time.Date(t.BookedAt.Year(), t.BookedAt.Month(), 1, 0, 0, 0, 0, time.UTC)
			a := nouvelleAlerte(EspecesFractionnees, credit, t.BookedAt, 0, t.TransactionID, preuves)
			a.ID, a.OuverteLe = alerteID(EspecesFractionnees, credit, mois), mois
			alertes = append(alertes, a)
		}
	}
	return alertes, nil
}

// AlertesCycles transforme les cycles trouvés en alertes : (cycle, compte d'origine, période de 7 jours
// qui contient la dernière opération du cycle). Le même cycle trouvé depuis n'importe laquelle de ses
// opérations donne la même alerte.
func AlertesCycles(trouves []cycleTrouve) []Alerte {
	alertes := make([]Alerte, 0, len(trouves))
	for _, c := range trouves {
		alertes = append(alertes, nouvelleAlerte(Cycle, c.compte, c.fin, FenetreCycle, c.tx, c.chemin))
	}
	return alertes
}
