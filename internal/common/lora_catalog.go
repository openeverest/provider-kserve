package common

import (
	"encoding/json"
	"os"
	"sync"
)

const loraAdapterCatalogEnvVar = "LORA_ADAPTER_CATALOG"

// LoRACatalogEntry is one operator-defined LoRA adapter in the chart catalog.
type LoRACatalogEntry struct {
	Label string `json:"label"`
	Name  string `json:"name"`
	URI   string `json:"uri"`
}

var (
	loraCatalogOnce   sync.Once
	loraCatalogByName map[string]LoRACatalogEntry
)

func loadLoRACatalog() {
	loraCatalogByName = make(map[string]LoRACatalogEntry)
	raw := os.Getenv(loraAdapterCatalogEnvVar)
	if raw == "" {
		return
	}
	var entries []LoRACatalogEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return
	}
	for _, e := range entries {
		if e.Name == "" || e.URI == "" {
			continue
		}
		loraCatalogByName[e.Name] = e
	}
}

// LookupLoRAAdapter resolves a catalog adapter name to its entry.
func LookupLoRAAdapter(name string) (LoRACatalogEntry, bool) {
	loraCatalogOnce.Do(loadLoRACatalog)
	entry, ok := loraCatalogByName[name]
	return entry, ok
}

// ResetLoRACatalogForTest clears the cached catalog so tests can repoint the env var.
func ResetLoRACatalogForTest() {
	loraCatalogOnce = sync.Once{}
	loraCatalogByName = nil
}
