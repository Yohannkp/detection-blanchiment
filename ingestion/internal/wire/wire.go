// Package wire gère le format de message du Schema Registry (octet magique 0 + identifiant
// de schéma sur 4 octets big-endian + charge JSON) et la liste des schémas acceptés.
package wire

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Encode préfixe la charge avec l'identifiant de schéma.
func Encode(schemaID int, payload []byte) []byte {
	out := make([]byte, 5, 5+len(payload))
	binary.BigEndian.PutUint32(out[1:5], uint32(schemaID))
	return append(out, payload...)
}

// ErrFraming : le message ne suit pas le format du registre.
var ErrFraming = errors.New("message sans en-tête de schéma (octet magique 0 + identifiant)")

// Decode sépare l'identifiant de schéma de la charge.
func Decode(msg []byte) (schemaID int, payload []byte, err error) {
	if len(msg) < 5 || msg[0] != 0 {
		return 0, nil, ErrFraming
	}
	return int(binary.BigEndian.Uint32(msg[1:5])), msg[5:], nil
}

// Registry dit si un identifiant de schéma appartient au sujet. Les identifiants acceptés sont
// gardés en cache ; un identifiant inconnu est demandé au registre à chaque fois, pour qu'une
// version publiée à l'instant soit acceptée sans redémarrer le service.
type Registry struct {
	URL, Subject string
	Client       *http.Client

	mu  sync.Mutex
	ids map[int]bool
}

func NewRegistry(url, subject string) *Registry {
	return &Registry{URL: url, Subject: subject, Client: &http.Client{Timeout: 5 * time.Second}, ids: map[int]bool{}}
}

// Known renvoie false seulement si le registre répond que l'identifiant n'existe pas (404) ou
// qu'il appartient à un autre sujet. Toute autre erreur est une panne, renvoyée telle quelle.
// shortcut: un flot de messages à identifiant inconnu fait un appel HTTP chacun ; ajouter un cache
// négatif court si la DLQ reçoit un jour de gros volumes.
func (r *Registry) Known(id int) (bool, error) {
	r.mu.Lock()
	known := r.ids[id]
	r.mu.Unlock()
	if known {
		return true, nil
	}
	var refs []struct {
		Subject string `json:"subject"`
	}
	err := r.get(fmt.Sprintf("/schemas/ids/%d/versions", id), &refs)
	var st statusError
	if errors.As(err, &st) && st.code == http.StatusNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, ref := range refs {
		if ref.Subject == r.Subject {
			r.mu.Lock()
			r.ids[id] = true
			r.mu.Unlock()
			return true, nil
		}
	}
	return false, nil
}

// Latest renvoie l'identifiant de la dernière version (utilisé par les producteurs).
func (r *Registry) Latest() (int, error) {
	var s struct {
		ID int `json:"id"`
	}
	err := r.get("/subjects/"+r.Subject+"/versions/latest", &s)
	return s.ID, err
}

func (r *Registry) get(path string, v any) error {
	resp, err := r.Client.Get(r.URL + path)
	if err != nil {
		return fmt.Errorf("registre de schémas : %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return statusError{path, resp.StatusCode}
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

type statusError struct {
	path string
	code int
}

func (e statusError) Error() string {
	return fmt.Sprintf("registre de schémas : %s → HTTP %d", e.path, e.code)
}
