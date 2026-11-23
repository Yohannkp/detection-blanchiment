package wire

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestEncodeDecode(t *testing.T) {
	id, p, err := Decode(Encode(258, []byte(`{"a":1}`)))
	if err != nil || id != 258 || !bytes.Equal(p, []byte(`{"a":1}`)) {
		t.Fatalf("aller-retour : %d %q %v", id, p, err)
	}
	for _, bad := range [][]byte{nil, {0, 0, 0}, []byte(`{"a":1}`), {1, 0, 0, 0, 1, '{'}} {
		if _, _, err := Decode(bad); err != ErrFraming {
			t.Errorf("%v accepté", bad)
		}
	}
}

func TestRegistryKnown(t *testing.T) {
	var down atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case down.Load():
			w.WriteHeader(http.StatusInternalServerError)
		case req.URL.Path == "/schemas/ids/1/versions" || req.URL.Path == "/schemas/ids/2/versions":
			fmt.Fprint(w, `[{"subject":"transactions.raw-value","version":1}]`)
		case req.URL.Path == "/schemas/ids/3/versions":
			fmt.Fprint(w, `[{"subject":"autre-sujet","version":1}]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	r := NewRegistry(srv.URL, "transactions.raw-value")

	for id, want := range map[int]bool{1: true, 3: false, 9999: false} {
		if got, err := r.Known(id); err != nil || got != want {
			t.Errorf("id %d : %v %v, attendu %v", id, got, err, want)
		}
	}
	// Une version publiée après un identifiant inconnu est acceptée tout de suite (pas de délai de relecture).
	if ok, _ := r.Known(2); !ok {
		t.Error("nouvelle version refusée")
	}
	down.Store(true)
	if ok, _ := r.Known(1); !ok {
		t.Error("identifiant en cache refusé pendant une panne du registre")
	}
	if _, err := r.Known(4); err == nil {
		t.Error("panne du registre prise pour un identifiant inconnu : le message partirait en DLQ")
	}
}
