package common

import (
	"os"
	"testing"

	"sigs.k8s.io/yaml"
)

// TestCatalogMatchesChart fails when definition/versions.yaml advertises a
// KServe version the chart does not install.
//
// The two drifted once already: the v0.20.0 bump moved go.mod and the subcharts
// but left the catalog on 0.15.0, so the provider installed KServe 0.20 while
// telling the UI it was 0.15. Nothing caught it - `make verify` only proves the
// generated spec matches the catalog, and both were consistently wrong.
func TestCatalogMatchesChart(t *testing.T) {
	var catalog struct {
		ComponentTypes map[string]struct {
			Versions []struct {
				Version string `json:"version"`
				Default bool   `json:"default"`
			} `json:"versions"`
		} `json:"componentTypes"`
		Versions []struct {
			Name       string            `json:"name"`
			Default    bool              `json:"default"`
			Components map[string]string `json:"components"`
		} `json:"versions"`
	}
	readYAML(t, "../../definition/versions.yaml", &catalog)

	var chart struct {
		Dependencies []struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"dependencies"`
	}
	readYAML(t, "../../charts/provider-kserve/Chart.yaml", &chart)

	// The chart pins every KServe subchart to the same version; take the
	// controller subchart as the reference.
	var chartKServe string
	for _, dep := range chart.Dependencies {
		if dep.Name == "kserve-resources" {
			chartKServe = dep.Version // e.g. "v0.20.0"
		}
	}
	if chartKServe == "" {
		t.Fatal("kserve-resources dependency not found in Chart.yaml")
	}

	var defaultBundle string
	predictor := ""
	bundles := 0
	for _, b := range catalog.Versions {
		if !b.Default {
			continue
		}
		bundles++
		defaultBundle = b.Name
		predictor = b.Components["predictor"]
	}
	if bundles != 1 {
		t.Fatalf("want exactly one default version bundle, got %d", bundles)
	}

	// "v0.20.0" (chart) vs "0.20.0" (catalog predictor) vs "0.20" (bundle name).
	if want := "v" + predictor; want != chartKServe {
		t.Errorf("default bundle %q pins predictor %s, but the chart installs KServe %s\n"+
			"bump definition/versions.yaml (and test/vars.sh) alongside Chart.yaml",
			defaultBundle, predictor, chartKServe)
	}

	// The bundle name is what test/vars.sh and Instance.spec.version carry, so
	// it has to be derivable from the KServe release it claims.
	if want := "v" + defaultBundle; !isPrefixOf(want, chartKServe) {
		t.Errorf("default bundle is named %q, which does not match KServe %s", defaultBundle, chartKServe)
	}

	for name, ct := range catalog.ComponentTypes {
		defaults := 0
		for _, v := range ct.Versions {
			if v.Default {
				defaults++
			}
		}
		if defaults != 1 {
			t.Errorf("componentTypes.%s: want exactly one default version, got %d", name, defaults)
		}
	}
}

func isPrefixOf(prefix, s string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func readYAML(t *testing.T, path string, into any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, into); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}
