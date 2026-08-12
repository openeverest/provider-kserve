package common

import (
	"testing"
)

func TestLookupLoRAAdapter(t *testing.T) {
	t.Setenv(loraAdapterCatalogEnvVar, `[{"label":"A","name":"a","uri":"hf://org/a"}]`)
	ResetLoRACatalogForTest()

	entry, ok := LookupLoRAAdapter("a")
	if !ok || entry.URI != "hf://org/a" {
		t.Fatalf("LookupLoRAAdapter() = %+v, %v", entry, ok)
	}
	if _, ok := LookupLoRAAdapter("missing"); ok {
		t.Fatal("expected missing adapter to be absent")
	}
}
