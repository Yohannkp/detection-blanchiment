// Package detect applique les règles de détection du blanchiment à chaque opération écrite dans
// le graphe. Réglages calibrés sur les jours 1 à 5 du rejeu AMLworld (docs/decisions/003-detection.md).
package detect

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Règles (identifiants stables : ils sont stockés dans Postgres).
const (
	FanIn               = "fan_in"
	FanOut              = "fan_out"
	Cycle               = "cycle"
	EspecesFractionnees = "especes_fractionnees"
)

// Réglages.
const (
	// Fan-in / fan-out : au moins N contreparties distinctes en 96 h, sur un compte qui en a eu au plus
	// 20 parmi les opérations vues jusqu'ici. Sans ce plafond, les comptes « hubs » légitimes (payeurs de
	// salaires, commerces) produisent des dizaines de milliers d'alertes (balayage de calibrage hors ligne,
	// jours 1-5 : F1 fan-out 0,018 sans plafond, 0,144 avec).
	FenetreDegre   = 96 * time.Hour
	SeuilFanIn     = 5
	SeuilFanOut    = 6
	PlafondHistori = 20

	// Cycles : l'argent revient à l'émetteur en 2 à 6 opérations sur 7 jours, sans passer par un compte
	// de plus de 200 liens. Au-delà de 6, le nombre de chemins explose (constaté : Neo4j saturé).
	FenetreCycle    = 7 * 24 * time.Hour
	SautsMaxCycle   = 5 // chemin de retour ; + l'opération qui ferme le cycle = 6
	PlafondHubCycle = 200

	// Espèces fractionnées : au moins 3 dépôts d'espèces dans le mois civil, pour un cumul juste sous
	// le seuil de communication systématique à Tracfin (COSI : plus de 10 000 € cumulés sur un mois,
	// art. L.561-15-1 du CMF). Au-delà, la COSI part d'office : ce n'est plus du contournement.
	DepotsMinEspeces = 3
	CumulMinEspeces  = 800_000   // 8 000,00 € en centimes
	CumulMaxEspeces  = 1_000_000 // 10 000,00 €
)

// Alerte est une règle déclenchée sur un compte, pour une PÉRIODE fixe (96 h pour fan-in/fan-out,
// 7 jours pour les cycles, le mois civil pour les espèces). L'identifiant ne dépend que de (règle,
// compte, période) : quel que soit l'ordre d'arrivée des opérations, un réseau donne les mêmes alertes.
// Un réseau à cheval sur deux périodes en donne deux : c'est le prix de cette indépendance.
type Alerte struct {
	ID          string
	Regle       string
	Entite      string
	OuverteLe   time.Time // début de la période
	Quand       time.Time // temps de l'événement auquel la règle est vérifiée (preuves datées)
	Declencheur string    // première opération traitée qui a fait franchir le seuil (indicatif)
	Preuves     []string
}

func alerteID(regle, entite string, debut time.Time) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d", regle, entite, debut.Unix())))
	return regle + "-" + hex.EncodeToString(h[:8])
}

// nouvelleAlerte range l'alerte dans la période qui contient quand (periode = 0 : à fixer par l'appelant).
func nouvelleAlerte(regle, entite string, quand time.Time, periode time.Duration, declencheur string, preuves []string) Alerte {
	debut := quand.UTC()
	if periode > 0 {
		debut = time.Unix(quand.Unix()/int64(periode.Seconds())*int64(periode.Seconds()), 0).UTC()
	}
	return Alerte{ID: alerteID(regle, entite, debut), Regle: regle, Entite: entite, OuverteLe: debut,
		Quand: quand.UTC(), Declencheur: declencheur, Preuves: preuves}
}

// membre code une opération dans une fenêtre : « contrepartie|montant|transaction_id ».
// L'identifiant vient en dernier et n'est pas découpé : il peut contenir « | » sans rien casser.
func membre(txID, contrepartie string, montant int64) string {
	return fmt.Sprintf("%s|%d|%s", contrepartie, montant, txID)
}

type element struct {
	tx, contrepartie string
	montant          int64
}

func lire(m string) (element, error) {
	p := strings.SplitN(m, "|", 3)
	if len(p) != 3 {
		return element{}, fmt.Errorf("membre de fenêtre illisible %q", m)
	}
	montant, err := strconv.ParseInt(p[1], 10, 64)
	if err != nil {
		return element{}, fmt.Errorf("montant illisible dans %q", m)
	}
	return element{p[2], p[0], montant}, nil
}

type fenetre struct {
	fin     time.Time
	preuves []string
}

// evaluerFenetres : fan-in ou fan-out. points = opérations du compte dans (T − 96 h, T + 96 h).
// On examine chaque fenêtre (fin − 96 h, fin] qui contient T, avec fin = l'horodatage d'une opération
// de [T, T + 96 h) : le nombre de contreparties distinctes n'augmente qu'à ces instants. Les fenêtres
// qui atteignent le seuil sont regroupées par période ; preuves = toutes leurs opérations, et fin = la
// première fin qui atteint le seuil dans la période. Rien si le compte a dépassé le plafond d'historique.
func evaluerFenetres(points []redis.Z, T, historique int64, seuil int) ([]fenetre, error) {
	if historique > PlafondHistori {
		return nil, nil
	}
	W := int64(FenetreDegre.Seconds())
	els := make([]element, len(points))
	for i, z := range points {
		m, ok := z.Member.(string)
		if !ok {
			return nil, fmt.Errorf("membre de fenêtre de type %T", z.Member)
		}
		e, err := lire(m)
		if err != nil {
			return nil, err
		}
		els[i] = e
	}
	parPeriode := map[int64]*fenetre{}
	var ordre []int64
	for i, z := range points {
		fin := int64(z.Score)
		if fin < T || fin >= T+W || (i > 0 && int64(points[i-1].Score) == fin) {
			continue // fin hors des fenêtres qui contiennent T, ou déjà examinée
		}
		distinctes := map[string]bool{}
		var dans []string
		for j, y := range points {
			if s := int64(y.Score); s > fin-W && s <= fin {
				distinctes[els[j].contrepartie] = true
				dans = append(dans, els[j].tx)
			}
		}
		if len(distinctes) < seuil {
			continue
		}
		periode := fin / W
		f, ok := parPeriode[periode]
		if !ok {
			f = &fenetre{fin: time.Unix(fin, 0).UTC()}
			parPeriode[periode] = f
			ordre = append(ordre, periode)
		}
		f.preuves = append(f.preuves, dans...)
	}
	out := make([]fenetre, 0, len(ordre))
	for _, k := range ordre {
		f := parPeriode[k]
		f.preuves = sansDoublon(f.preuves)
		out = append(out, *f)
	}
	return out, nil
}

func sansDoublon(ids []string) []string {
	vus := map[string]bool{}
	out := ids[:0]
	for _, id := range ids {
		if !vus[id] {
			vus[id] = true
			out = append(out, id)
		}
	}
	return out
}

// evaluerEspeces : dépôts d'espèces du mois civil.
func evaluerEspeces(depots []string) ([]string, error) {
	var cumul int64
	preuves := make([]string, 0, len(depots))
	for _, m := range depots {
		e, err := lire(m)
		if err != nil {
			return nil, err
		}
		cumul += e.montant
		preuves = append(preuves, e.tx)
	}
	if len(depots) < DepotsMinEspeces || cumul < CumulMinEspeces || cumul > CumulMaxEspeces {
		return nil, nil
	}
	return preuves, nil
}
