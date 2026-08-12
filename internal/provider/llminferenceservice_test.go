package provider

import (
	"testing"

	"k8s.io/utils/ptr"

	"github.com/openeverest/provider-kserve/definition/components"
	"github.com/openeverest/provider-kserve/internal/common"
)

func loraCatalogEnv(t *testing.T) {
	t.Helper()
	t.Setenv("LORA_ADAPTER_CATALOG", `[{"label":"SQL","name":"sql-lora","uri":"hf://org/sql"},{"label":"Code","name":"code-style","uri":"s3://bucket/code"}]`)
	common.ResetLoRACatalogForTest()
}

func TestBuildModelLoRA(t *testing.T) {
	t.Run("nil when unset", func(t *testing.T) {
		got, err := buildModelLoRA(components.VllmCustomSpec{})
		if err != nil {
			t.Fatal(err)
		}
		if got != nil {
			t.Fatalf("buildModelLoRA() = %#v, want nil", got)
		}
	})

	t.Run("maps adapters and tuning", func(t *testing.T) {
		got, err := buildModelLoRA(components.VllmCustomSpec{
			LoRA: &components.LoRASpec{
				MaxRank:        ptr.To(int32(32)),
				MaxAdapters:    ptr.To(int32(4)),
				MaxCpuAdapters: ptr.To(int32(8)),
				Adapters: []components.LoRAAdapterSpec{
					{Name: "ft-v1", URI: "hf://org/adapter-v1"},
					{Name: "ft-v2", URI: "s3://bucket/adapters/v2"},
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if got == nil {
			t.Fatal("expected non-nil LoRA spec")
		}
		if len(got.Adapters) != 2 {
			t.Fatalf("adapters = %d, want 2", len(got.Adapters))
		}
		if got.MaxRank == nil || *got.MaxRank != 32 {
			t.Fatalf("maxRank = %v, want 32", got.MaxRank)
		}
		if *got.Adapters[0].Name != "ft-v1" || got.Adapters[0].URI.String() != "hf://org/adapter-v1" {
			t.Fatalf("adapter[0] = %+v", got.Adapters[0])
		}
	})

	t.Run("maps catalog slots when deployment enabled", func(t *testing.T) {
		loraCatalogEnv(t)
		got, err := buildModelLoRA(components.VllmCustomSpec{
			LoraDeployment: components.LoraDeploymentEnabled,
			LoraSlot1:      "sql-lora",
			LoraSlot2:      "code-style",
		})
		if err != nil {
			t.Fatal(err)
		}
		if got == nil || len(got.Adapters) != 2 {
			t.Fatalf("adapters = %d, want 2", len(got.Adapters))
		}
		if *got.Adapters[0].Name != "sql-lora" || got.Adapters[0].URI.String() != "hf://org/sql" {
			t.Fatalf("adapter[0] = %+v", got.Adapters[0])
		}
		if *got.Adapters[1].Name != "code-style" || got.Adapters[1].URI.String() != "s3://bucket/code" {
			t.Fatalf("adapter[1] = %+v", got.Adapters[1])
		}
	})
}

func TestValidateLoRAParams(t *testing.T) {
	base := components.VllmCustomSpec{
		ModelURI:  "hf://org/base",
		ModelName: "base-model",
	}

	t.Run("duplicate adapter names", func(t *testing.T) {
		err := validateLoRAParams("inst", components.VllmCustomSpec{
			ModelURI: base.ModelURI,
			LoRA: &components.LoRASpec{
				Adapters: []components.LoRAAdapterSpec{
					{Name: "dup", URI: "hf://org/a"},
					{Name: "dup", URI: "hf://org/b"},
				},
			},
		})
		if err == nil {
			t.Fatal("expected error for duplicate adapter name")
		}
	})

	t.Run("adapter name matches base model", func(t *testing.T) {
		err := validateLoRAParams("inst", components.VllmCustomSpec{
			ModelURI:  base.ModelURI,
			ModelName: "base-model",
			LoRA: &components.LoRASpec{
				Adapters: []components.LoRAAdapterSpec{
					{Name: "base-model", URI: "hf://org/a"},
				},
			},
		})
		if err == nil {
			t.Fatal("expected error when adapter name equals base model name")
		}
	})

	t.Run("hf adapter with storage initializer disabled", func(t *testing.T) {
		err := validateLoRAParams("inst", components.VllmCustomSpec{
			ModelURI:                  base.ModelURI,
			DisableStorageInitializer: ptr.To(true),
			LoRA: &components.LoRASpec{
				Adapters: []components.LoRAAdapterSpec{
					{Name: "ft", URI: "hf://org/a"},
				},
			},
		})
		if err == nil {
			t.Fatal("expected error when hf adapter needs storage initializer")
		}
	})

	t.Run("valid", func(t *testing.T) {
		if err := validateLoRAParams("inst", components.VllmCustomSpec{
			ModelURI: base.ModelURI,
			LoRA: &components.LoRASpec{
				Adapters: []components.LoRAAdapterSpec{
					{Name: "ft-v1", URI: "hf://org/a"},
				},
			},
		}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("enabled deployment requires slot", func(t *testing.T) {
		loraCatalogEnv(t)
		err := validateLoRAParams("inst", components.VllmCustomSpec{
			ModelURI:       base.ModelURI,
			LoraDeployment: components.LoraDeploymentEnabled,
		})
		if err == nil {
			t.Fatal("expected error when enabled with no slots")
		}
	})

	t.Run("unknown catalog adapter in slot", func(t *testing.T) {
		loraCatalogEnv(t)
		err := validateLoRAParams("inst", components.VllmCustomSpec{
			ModelURI:       base.ModelURI,
			LoraDeployment: components.LoraDeploymentEnabled,
			LoraSlot1:      "missing",
		})
		if err == nil {
			t.Fatal("expected error for unknown catalog adapter")
		}
	})

	t.Run("duplicate slots", func(t *testing.T) {
		loraCatalogEnv(t)
		err := validateLoRAParams("inst", components.VllmCustomSpec{
			ModelURI:       base.ModelURI,
			LoraDeployment: components.LoraDeploymentEnabled,
			LoraSlot1:      "sql-lora",
			LoraSlot2:      "sql-lora",
		})
		if err == nil {
			t.Fatal("expected error for duplicate slot selection")
		}
	})

	t.Run("valid catalog slots", func(t *testing.T) {
		loraCatalogEnv(t)
		if err := validateLoRAParams("inst", components.VllmCustomSpec{
			ModelURI:       base.ModelURI,
			LoraDeployment: components.LoraDeploymentEnabled,
			LoraSlot1:      "sql-lora",
		}); err != nil {
			t.Fatal(err)
		}
	})
}
