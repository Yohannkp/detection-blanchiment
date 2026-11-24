package detect

import (
	"slices"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// points fabrique le contenu d'une fenêtre Redis : (horodatage en heures depuis t0, contrepartie).
func points(t *testing.T, ops ...[2]any) []redis.Z {
	t.Helper()
	out := make([]redis.Z, len(ops))
	for i, o := range ops {
		h, c := o[0].(int), o[1].(string)
		out[i] = redis.Z{Score: float64(t0.Add(time.Duration(h) * time.Hour).Unix()), Member: membre("tx"+string(rune('a'+i)), c, 100)}
	}
	return out
}

func heure(h int) int64 { return t0.Add(time.Duration(h) * time.Hour).Unix() }

func TestEvaluerFenetres(t *testing.T) {
	cas := []struct {
		nom        string
		points     []redis.Z
		T          int64
		historique int64
		preuves    int // 0 = pas d'alerte
	}{
		{"5 émetteurs distincts en 5 h", points(t, [2]any{0, "A"}, [2]any{1, "B"}, [2]any{2, "C"}, [2]any{3, "D"}, [2]any{4, "E"}), heure(4), 5, 5},
		{"4 émetteurs seulement", points(t, [2]any{0, "A"}, [2]any{1, "B"}, [2]any{2, "C"}, [2]any{3, "D"}), heure(3), 4, 0},
		{"5 opérations, 2 émetteurs", points(t, [2]any{0, "A"}, [2]any{1, "A"}, [2]any{2, "B"}, [2]any{3, "B"}, [2]any{4, "A"}), heure(4), 2, 0},
		{"hub au-delà du plafond", points(t, [2]any{0, "A"}, [2]any{1, "B"}, [2]any{2, "C"}, [2]any{3, "D"}, [2]any{4, "E"}), heure(4), PlafondHistori + 1, 0},
		// Opération en retard (T = 0 h) qui complète une fenêtre se terminant APRÈS elle : détectée.
		{"opération arrivée en retard", points(t, [2]any{0, "A"}, [2]any{1, "B"}, [2]any{2, "C"}, [2]any{3, "D"}, [2]any{4, "E"}), heure(0), 5, 5},
		// Émetteurs étalés sur 120 h : jamais 5 dans une fenêtre de 96 h.
		{"émetteurs hors fenêtre", points(t, [2]any{0, "A"}, [2]any{30, "B"}, [2]any{60, "C"}, [2]any{90, "D"}, [2]any{120, "E"}), heure(120), 5, 0},
	}
	for _, c := range cas {
		f, err := evaluerFenetres(c.points, c.T, c.historique, SeuilFanIn)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, x := range f {
			n += len(x.preuves)
		}
		if n != c.preuves {
			t.Errorf("%s : %d preuve(s), attendu %d", c.nom, n, c.preuves)
		}
	}
}

func TestMembreAvecSeparateurDansLIdentifiant(t *testing.T) {
	e, err := lire(membre("id|avec|barres", "FR76X", 4200))
	if err != nil || e.tx != "id|avec|barres" || e.contrepartie != "FR76X" || e.montant != 4200 {
		t.Fatalf("%+v %v", e, err)
	}
	if _, err := lire("illisible"); err == nil {
		t.Error("membre illisible accepté")
	}
}

func TestEvaluerEspeces(t *testing.T) {
	depots := func(montants ...int64) []string {
		out := make([]string, len(montants))
		for i, m := range montants {
			out[i] = membre("d"+string(rune('a'+i)), "", m)
		}
		return out
	}
	cas := []struct {
		nom    string
		depots []string
		alerte bool
	}{
		{"4 × 2 400 € = 9 600 €", depots(240_000, 240_000, 240_000, 240_000), true},
		{"3 dépôts pile à 8 000 €", depots(300_000, 300_000, 200_000), true},
		{"pile à 10 000 € : encore sous le seuil COSI", depots(400_000, 300_000, 300_000), true},
		{"10 000,01 € : la COSI part d'office", depots(400_000, 300_000, 300_001), false},
		{"2 dépôts seulement", depots(450_000, 450_000), false},
		{"cumul trop faible", depots(100_000, 100_000, 100_000), false},
	}
	for _, c := range cas {
		preuves, err := evaluerEspeces(c.depots)
		if err != nil {
			t.Fatal(err)
		}
		if (preuves != nil) != c.alerte {
			t.Errorf("%s : alerte %v, attendu %v", c.nom, preuves != nil, c.alerte)
		}
	}
}

func TestAlerteParPeriode(t *testing.T) {
	d := time.Date(2022, 9, 1, 10, 0, 0, 0, time.UTC)
	a := nouvelleAlerte(FanIn, "X", d, FenetreDegre, "t1", nil)
	b := nouvelleAlerte(FanIn, "X", d.Add(time.Hour), FenetreDegre, "t2", nil) // même période, autre déclencheur
	autres := []string{
		nouvelleAlerte(FanOut, "X", d, FenetreDegre, "t1", nil).ID,
		nouvelleAlerte(FanIn, "Y", d, FenetreDegre, "t1", nil).ID,
		nouvelleAlerte(FanIn, "X", d.Add(FenetreDegre), FenetreDegre, "t1", nil).ID, // période suivante
	}
	if a.ID != b.ID || slices.Contains(autres, a.ID) || !a.OuverteLe.After(d.Add(-FenetreDegre)) || a.OuverteLe.After(d) {
		t.Fatalf("identifiants : %s %s %v, début %v", a.ID, b.ID, autres, a.OuverteLe)
	}
}
