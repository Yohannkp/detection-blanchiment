package tx

import (
	"fmt"
	"math/big"
	"strings"
)

// Longueur officielle de l'IBAN par pays (registre SWIFT) : espace SEPA + quelques pays
// présents dans les données. Un pays absent de la table est refusé.
var ibanLength = map[string]int{
	"AD": 24, "AT": 20, "BE": 16, "BG": 22, "CH": 21, "CY": 28, "CZ": 24, "DE": 22, "DK": 18,
	"EE": 20, "ES": 24, "FI": 18, "FR": 27, "GB": 22, "GI": 23, "GR": 27, "HR": 21, "HU": 28,
	"IE": 22, "IS": 26, "IT": 27, "LI": 21, "LT": 20, "LU": 20, "LV": 21, "MC": 27, "MT": 31,
	"NL": 18, "NO": 15, "PL": 28, "PT": 25, "RO": 24, "SE": 24, "SI": 19, "SK": 24, "SM": 27,
	"VA": 22, "AE": 23, "SA": 24, "TR": 26, "IL": 23, "BR": 29,
}

// ValidateIBAN vérifie le format, la longueur du pays et la clé de contrôle mod-97 (ISO 13616).
func ValidateIBAN(iban string) error {
	if len(iban) < 4 {
		return fmt.Errorf("IBAN trop court")
	}
	want, ok := ibanLength[iban[:2]]
	if !ok {
		return fmt.Errorf("pays IBAN inconnu %q", iban[:2])
	}
	if len(iban) != want {
		return fmt.Errorf("IBAN %s : %d caractères au lieu de %d", iban[:2], len(iban), want)
	}
	for i, c := range iban {
		digit := c >= '0' && c <= '9'
		upper := c >= 'A' && c <= 'Z'
		if (i < 2 && !upper) || (i >= 2 && i < 4 && !digit) || (!digit && !upper) {
			return fmt.Errorf("IBAN : caractère invalide en position %d", i+1)
		}
	}
	if mod97(iban[4:]+iban[:2]+iban[2:4]) != 1 {
		return fmt.Errorf("IBAN : clé de contrôle invalide")
	}
	return nil
}

// mod97 convertit les lettres en nombres (A=10 … Z=35) puis calcule le reste modulo 97.
func mod97(s string) int64 {
	var b strings.Builder
	for _, c := range s {
		if c >= 'A' && c <= 'Z' {
			fmt.Fprintf(&b, "%d", c-'A'+10)
		} else {
			b.WriteRune(c)
		}
	}
	n, _ := new(big.Int).SetString(b.String(), 10)
	return new(big.Int).Mod(n, big.NewInt(97)).Int64()
}

// MakeIBAN fabrique un IBAN valide (clé calculée) à partir d'un pays et d'un BBAN.
// Sert au simulateur et aux tests : les comptes affichés sont fictifs mais bien formés.
func MakeIBAN(country, bban string) string {
	check := 98 - mod97(bban+country+"00")
	return fmt.Sprintf("%s%02d%s", country, check, bban)
}
