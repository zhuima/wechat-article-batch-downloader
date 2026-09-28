package officialaccount

import (
	"encoding/json"
	"testing"

	"mp_article_batch_downloader/internal/config"
)

func TestOfficialAccountConfigExposesExplicitBridgeProbeTarget(t *testing.T) {
	t.Setenv("MP_ARCHIVE_BRIDGE_PROBE", "1")
	t.Setenv("MP_ARCHIVE_BRIDGE_PROBE_BIZ", "MzTarget")
	cfg := NewOfficialAccountConfig(&config.Config{}, false)
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var injected struct {
		Enabled bool   `json:"bridgeProbeEnabled"`
		Biz     string `json:"bridgeProbeTargetBiz"`
	}
	if err := json.Unmarshal(data, &injected); err != nil {
		t.Fatal(err)
	}
	if !injected.Enabled || injected.Biz != "MzTarget" {
		t.Fatalf("official-account injected config omitted probe gate: %+v", injected)
	}
	t.Setenv("MP_ARCHIVE_BRIDGE_PROBE", "0")
	if NewOfficialAccountConfig(&config.Config{}, false).BridgeProbeEnabled {
		t.Fatal("probe remained enabled without explicit environment switch")
	}
}
