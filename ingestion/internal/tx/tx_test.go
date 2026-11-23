package tx

import (
	"errors"
	"testing"
	"time"
)

func TestValidateIBAN(t *testing.T) {
	ok := []string{
		"FR7630006000011234567890189", // exemple publié par la Banque de France
		"DE89370400440532013000",
		"GB82WEST12345698765432",
		MakeIBAN("FR", "30001000010000000000100"),
	}
	for _, iban := range ok {
		if err := ValidateIBAN(iban); err != nil {
			t.Errorf("%s devrait être valide : %v", iban, err)
		}
	}
	bad := map[string]string{
		"FR7630006000011234567890188": "clé fausse",
		"FR763000600001123456789018":  "trop court pour FR",
		"XX7630006000011234567890189": "pays inconnu",
		"FR76300060000112345678901 9": "espace",
		"fr7630006000011234567890189": "minuscules",
		"":                            "vide",
	}
	for iban, why := range bad {
		if ValidateIBAN(iban) == nil {
			t.Errorf("%q accepté (%s)", iban, why)
		}
	}
}

func TestParse(t *testing.T) {
	a, b := MakeIBAN("FR", "30001000010000000000100"), MakeIBAN("FR", "30001000010000000000200")
	cases := []struct {
		name, json, reason string
	}{
		{"virement valide", `{"transaction_id":"t1","booked_at":"2026-11-23T10:00:00Z","payment_type":"transfer","debtor_iban":"` + a + `","creditor_iban":"` + b + `","amount_minor":12345,"currency":"EUR"}`, ""},
		{"dépôt d'espèces", `{"transaction_id":"t2","booked_at":"2026-11-23T10:00:00Z","payment_type":"cash_deposit","creditor_iban":"` + b + `","amount_minor":900000,"currency":"EUR"}`, ""},
		{"montant négatif", `{"transaction_id":"t3","booked_at":"2026-11-23T10:00:00Z","payment_type":"transfer","debtor_iban":"` + a + `","creditor_iban":"` + b + `","amount_minor":-5,"currency":"EUR"}`, ReasonAmount},
		{"montant décimal", `{"transaction_id":"t4","booked_at":"2026-11-23T10:00:00Z","payment_type":"transfer","debtor_iban":"` + a + `","creditor_iban":"` + b + `","amount_minor":12.5,"currency":"EUR"}`, ReasonMalformed},
		{"devise inconnue", `{"transaction_id":"t5","booked_at":"2026-11-23T10:00:00Z","payment_type":"transfer","debtor_iban":"` + a + `","creditor_iban":"` + b + `","amount_minor":100,"currency":"XYZ"}`, ReasonCurrency},
		{"IBAN faux", `{"transaction_id":"t6","booked_at":"2026-11-23T10:00:00Z","payment_type":"transfer","debtor_iban":"FR7630006000011234567890188","creditor_iban":"` + b + `","amount_minor":100,"currency":"EUR"}`, ReasonIBAN},
		{"vers soi-même", `{"transaction_id":"t7","booked_at":"2026-11-23T10:00:00Z","payment_type":"transfer","debtor_iban":"` + a + `","creditor_iban":"` + a + `","amount_minor":100,"currency":"EUR"}`, ReasonSchema},
		{"dépôt avec donneur d'ordre", `{"transaction_id":"t8","booked_at":"2026-11-23T10:00:00Z","payment_type":"cash_deposit","debtor_iban":"` + a + `","creditor_iban":"` + b + `","amount_minor":100,"currency":"EUR"}`, ReasonSchema},
		{"sans identifiant", `{"booked_at":"2026-11-23T10:00:00Z","payment_type":"transfer","debtor_iban":"` + a + `","creditor_iban":"` + b + `","amount_minor":100,"currency":"EUR"}`, ReasonSchema},
		{"JSON tronqué", `{"transaction_id":"t9"`, ReasonMalformed},
		{"v2 : moyen de paiement", `{"transaction_id":"t11","booked_at":"2026-11-23T10:00:00Z","payment_type":"transfer","debtor_iban":"` + a + `","creditor_iban":"` + b + `","amount_minor":12300,"currency":"XBT","payment_format":"bitcoin"}`, ""},
		{"v2 : moyen inconnu", `{"transaction_id":"t12","booked_at":"2026-11-23T10:00:00Z","payment_type":"transfer","debtor_iban":"` + a + `","creditor_iban":"` + b + `","amount_minor":100,"currency":"EUR","payment_format":"troc"}`, ReasonSchema},
		{"données après l'objet", `{"transaction_id":"t10","booked_at":"2026-11-23T10:00:00Z","payment_type":"transfer","debtor_iban":"` + a + `","creditor_iban":"` + b + `","amount_minor":100,"currency":"EUR"}xyz`, ReasonMalformed},
	}
	for _, c := range cases {
		_, err := Parse([]byte(c.json))
		var r *Rejection
		switch {
		case c.reason == "" && err != nil:
			t.Errorf("%s : rejet inattendu %v", c.name, err)
		case c.reason != "" && (!errors.As(err, &r) || r.Reason != c.reason):
			t.Errorf("%s : attendu %s, obtenu %v", c.name, c.reason, err)
		}
	}
}

func TestParseNormalisesUTC(t *testing.T) {
	a, b := MakeIBAN("FR", "30001000010000000000100"), MakeIBAN("FR", "30001000010000000000200")
	base := `","payment_type":"transfer","debtor_iban":"` + a + `","creditor_iban":"` + b + `","amount_minor":1,"currency":"EUR"}`
	t1, _ := Parse([]byte(`{"transaction_id":"x","booked_at":"2026-11-23T10:00:00Z` + base))
	t2, _ := Parse([]byte(`{"transaction_id":"x","booked_at":"2026-11-23T11:00:00+01:00` + base))
	if t1 != t2 || t1.BookedAt.Location() != time.UTC {
		t.Fatalf("même instant, contenus différents : %v / %v", t1.BookedAt, t2.BookedAt)
	}
}

func TestAccounts(t *testing.T) {
	d, c := Transaction{PaymentType: "cash_deposit", CreditorIBAN: "B"}.Accounts()
	if d != CashAccount || c != "B" {
		t.Fatalf("dépôt : %s → %s", d, c)
	}
	d, c = Transaction{PaymentType: "cash_withdrawal", DebtorIBAN: "A"}.Accounts()
	if d != "A" || c != CashAccount {
		t.Fatalf("retrait : %s → %s", d, c)
	}
}
