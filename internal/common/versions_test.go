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
			DefaultVersion string `json:"defaultVersion"`
			Versions       []struct {
				Version string `json:"version"`
			} `json:"versions"`
		} `json:"componentTypes"`
		DefaultVersion string `json:"defaultVersion"`
		Versions       []struct {
			Name       string            `json:"name"`
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

	// values.yaml may run a different image tag than the subchart version.
	var values struct {
		KServeResources struct {
			KServe struct {
				Version string `json:"version"`
			} `json:"kserve"`
		} `json:"kserveResources"`
	}
	readYAML(t, "../../charts/provider-kserve/values.yaml", &values)
	if v := values.KServeResources.KServe.Version; v != "" {
		chartKServe = v
	}

	defaultBundle := catalog.DefaultVersion
	if defaultBundle == "" {
		t.Fatal("defaultVersion does not name a version bundle")
	}
	predictor := ""
	bundleFound := false
	for _, b := range catalog.Versions {
		if b.Name != defaultBundle {
			continue
		}
		bundleFound = true
		predictor = b.Components["predictor"]
	}
	if !bundleFound {
		t.Fatalf("defaultVersion %q does not name any version bundle", defaultBundle)
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
		if ct.DefaultVersion == "" {
			t.Errorf("componentTypes.%s: defaultVersion is not set", name)
			continue
		}
		versionFound := false
		for _, v := range ct.Versions {
			if v.Version == ct.DefaultVersion {
				versionFound = true
			}
		}
		if !versionFound {
			t.Errorf("componentTypes.%s: defaultVersion %q does not name any version", name, ct.DefaultVersion)
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
