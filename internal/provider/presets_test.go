package provider

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller/validationtest"

	"github.com/openeverest/provider-kserve/internal/common"
)

const chartValuesPath = "../../charts/provider-kserve/values.yaml"

// Every ClusterInstancePreset the chart installs by default must create a
// valid Instance.
func TestChartPresetsAreValidInstances(t *testing.T) {
	data, err := os.ReadFile(chartValuesPath)
	require.NoError(t, err)
	var values struct {
		Provider struct {
			Name      string `json:"name"`
			ShortName string `json:"shortName"`
		} `json:"provider"`
		Presets []struct {
			Name    string          `json:"name"`
			Enabled bool            `json:"enabled"`
			Spec    json.RawMessage `json:"spec"`
		} `json:"presets"`
	}
	require.NoError(t, yaml.Unmarshal(data, &values))
	require.Equal(t, common.ProviderName, values.Provider.Name)
	require.NotEmpty(t, values.Provider.ShortName)
	require.NotEmpty(t, values.Presets)

	v := validationtest.New(t, New(), providerSpecPath)
	for _, p := range values.Presets {
		if !p.Enabled {
			continue
		}
		name := values.Provider.ShortName + "-" + p.Name
		t.Run(name, func(t *testing.T) {
			var spec corev1alpha1.ClusterInstancePresetSpec
			// Strict, like the API server: a field the Instance does not have
			// would be pruned from the preset without notice.
			require.NoError(t, yaml.UnmarshalStrict(p.Spec, &spec))
			assert.Empty(t, spec.ProviderRef.Name, "the chart sets providerRef")
			spec.ProviderRef.Name = values.Provider.Name

			in := &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "from-" + name, Namespace: "default"},
				Spec:       spec.InstanceSpec,
			}
			res := v.Run(in, nil)
			assert.Empty(t, res.Errors)
		})
	}
}
