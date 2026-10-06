package localmodels

import (
	"net/http"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// PoolModel is one model of omlx's engine pool, named by its on-disk id (the
// model directory's name, never an alias).
type PoolModel struct {
	ID         string
	Loaded     bool
	Loading    bool
	Pinned     bool    // omlx never evicts a pinned model
	LastAccess float64 // omlx evicts the smallest first; 0 when never used
	Size       int64   // resident estimate in bytes; 0 when unknown
}

// Pool is one reading of omlx's engine pool. Ceiling is 0 when omlx's memory
// guard is off. SizesKnown is false when the reading came from the fallback
// (/health and the list), which names what is loaded and nothing else.
type Pool struct {
	Models     []PoolModel
	Ceiling    int64
	InUse      int64
	SizesKnown bool
}

// LoadedIDs lists the models that occupy the pool: loaded, or mid-load.
func (p Pool) LoadedIDs() []string {
	var ids []string
	for _, m := range p.Models {
		if m.Loaded || m.Loading {
			ids = append(ids, m.ID)
		}
	}
	return ids
}

// Find returns the pool model a provider-side name denotes, by the same
// lenient match the inventory uses for omlx artifacts.
func (p Pool) Find(name string) (PoolModel, bool) {
	for _, m := range p.Models {
		if NameMatches(m.ID, name) {
			return m, true
		}
	}
	return PoolModel{}, false
}

// OmlxPool reads omlx's pool. /v1/models/status is asked first: it names
// models by on-disk id, counts hidden ones, sees a model mid-load, and carries
// the sizes the eviction plan needs (#213). It is behind omlx's management
// auth, so the registry's key is sent when there is one. When status does not
// answer — a keyed server whose key the registry does not name, a server still
// initialising, an omlx without the endpoint — omlxLoaded's /health and list
// reading supplies the loaded ids alone, with its errors unchanged: a pool
// that will not say what is loaded is an error, never a guess.
func OmlxPool(cfg *config.Config, client *http.Client) (Pool, error) {
	origin, _ := FamilyOrigin(cfg, "omlx")
	key := FamilyAPIKey(cfg, "omlx")
	if p, ok := omlxStatusPool(client, origin, key); ok {
		return p, nil
	}
	ids, err := omlxLoaded(client, origin, key)
	if err != nil {
		return Pool{}, err
	}
	p := Pool{}
	for _, id := range ids {
		p.Models = append(p.Models, PoolModel{ID: id, Loaded: true})
	}
	return p, nil
}

// omlxStatusPool is the status half of OmlxPool; ok is false when status gave
// no model listing (a refusal answers with a JSON object too, without one).
func omlxStatusPool(client *http.Client, origin, key string) (Pool, bool) {
	var status struct {
		Ceiling int64 `json:"final_ceiling"`
		InUse   int64 `json:"current_model_memory"`
		Models  *[]struct {
			ID         string  `json:"id"`
			Loaded     bool    `json:"loaded"`
			Loading    bool    `json:"is_loading"`
			Pinned     bool    `json:"pinned"`
			LastAccess float64 `json:"last_access"`
			Resident   int64   `json:"resident_estimated_size"`
			Estimated  int64   `json:"estimated_size"`
		} `json:"models"`
	}
	if _, err := getJSON(client, origin+"/v1/models/status", key, &status); err != nil || status.Models == nil {
		return Pool{}, false
	}
	p := Pool{Ceiling: status.Ceiling, InUse: status.InUse, SizesKnown: true}
	for _, m := range *status.Models {
		if m.ID == "" {
			continue
		}
		size := m.Resident
		if size == 0 {
			size = m.Estimated
		}
		p.Models = append(p.Models, PoolModel{ID: m.ID, Loaded: m.Loaded, Loading: m.Loading, Pinned: m.Pinned, LastAccess: m.LastAccess, Size: size})
	}
	return p, true
}
