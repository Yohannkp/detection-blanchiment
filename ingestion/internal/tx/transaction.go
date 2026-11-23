// Package tx décrit une opération bancaire entrante et ses règles de validité.
package tx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// Compte interne de la banque qui porte les espèces : contrepartie des dépôts et retraits.
const CashAccount = "INTERNE:CAISSE"

// Transaction suit le contrat schemas/transaction.v1.json (vocabulaire ISO 20022).
type Transaction struct {
	TransactionID string    `json:"transaction_id"`
	BookedAt      time.Time `json:"booked_at"`
	PaymentType   string    `json:"payment_type"`
	DebtorIBAN    string    `json:"debtor_iban,omitempty"`
	CreditorIBAN  string    `json:"creditor_iban,omitempty"`
	AmountMinor   int64     `json:"amount_minor"`
	Currency      string    `json:"currency"`
}

// Raisons de rejet : elles partent en en-tête du message DLQ et dans les métriques.
const (
	ReasonMalformed       = "json_invalide"
	ReasonSchema          = "schema"
	ReasonIBAN            = "iban_invalide"
	ReasonCurrency        = "devise_inconnue"
	ReasonAmount          = "montant_invalide"
	ReasonIdempotencyConf = "conflit_idempotence"
)

// Rejection est une erreur de contenu : le message part en DLQ, il ne sera jamais valide en le relisant.
type Rejection struct {
	Reason string
	Detail string
}

func (r *Rejection) Error() string { return r.Reason + " : " + r.Detail }

func reject(reason, format string, a ...any) *Rejection {
	return &Rejection{Reason: reason, Detail: fmt.Sprintf(format, a...)}
}

// Codes ISO 4217 acceptés (devises présentes dans les données AMLworld + principales devises).
var currencies = map[string]bool{
	"EUR": true, "USD": true, "GBP": true, "CHF": true, "JPY": true, "CNY": true, "INR": true,
	"RUB": true, "AUD": true, "CAD": true, "MXN": true, "BRL": true, "SAR": true, "ILS": true,
	"SEK": true, "NOK": true, "DKK": true, "PLN": true,
}

// Parse décode un message JSON et le valide. Toute erreur renvoyée est une *Rejection.
func Parse(data []byte) (Transaction, error) {
	var t Transaction
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&t); err != nil {
		return t, reject(ReasonMalformed, "%v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return t, reject(ReasonMalformed, "données après l'objet JSON")
	}
	// Même instant, même représentation (« Z » ou « +00:00 ») pour l'outbox et l'aval.
	t.BookedAt = t.BookedAt.UTC()
	return t, t.Validate()
}

// Validate applique les règles que le schéma JSON ne peut pas exprimer seul.
func (t Transaction) Validate() error {
	switch {
	case t.TransactionID == "" || len(t.TransactionID) > 64:
		return reject(ReasonSchema, "transaction_id absent ou trop long")
	case t.BookedAt.IsZero():
		return reject(ReasonSchema, "booked_at absent")
	case t.AmountMinor <= 0:
		return reject(ReasonAmount, "montant %d ≤ 0", t.AmountMinor)
	case !currencies[t.Currency]:
		return reject(ReasonCurrency, "devise %q", t.Currency)
	}

	// Comptes attendus selon le type d'opération.
	var need []string
	switch t.PaymentType {
	case "transfer":
		if t.DebtorIBAN == t.CreditorIBAN {
			return reject(ReasonSchema, "virement d'un compte vers lui-même")
		}
		need = []string{t.DebtorIBAN, t.CreditorIBAN}
	case "cash_deposit":
		if t.DebtorIBAN != "" {
			return reject(ReasonSchema, "un dépôt d'espèces n'a pas de donneur d'ordre")
		}
		need = []string{t.CreditorIBAN}
	case "cash_withdrawal":
		if t.CreditorIBAN != "" {
			return reject(ReasonSchema, "un retrait d'espèces n'a pas de bénéficiaire")
		}
		need = []string{t.DebtorIBAN}
	default:
		return reject(ReasonSchema, "payment_type %q", t.PaymentType)
	}
	for _, iban := range need {
		if err := ValidateIBAN(iban); err != nil {
			return reject(ReasonIBAN, "%v", err)
		}
	}
	return nil
}

// Accounts renvoie (compte débité, compte crédité) dans le grand livre.
func (t Transaction) Accounts() (debit, credit string) {
	switch t.PaymentType {
	case "cash_deposit":
		return CashAccount, t.CreditorIBAN
	case "cash_withdrawal":
		return t.DebtorIBAN, CashAccount
	}
	return t.DebtorIBAN, t.CreditorIBAN
}
