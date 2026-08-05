package config_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/pigfox/deposit-dispute/adjudicator/internal/config"
)

// env builds a Getenv over a map, so no test touches the process environment.
func env(m map[string]string) config.Getenv {
	return func(k string) string { return m[k] }
}

// validEnv is a configuration that loads. Tests copy it and break one thing, so
// each assertion is about exactly the difference it introduced.
func validEnv() map[string]string {
	m := map[string]string{
		config.EnvRPCURL:         "https://example.invalid/rpc",
		config.EnvDisputeAddress: "0xDD00000000000000000000000000000000000DD0",
		config.EnvAnthropicKey:   "anthropic-key",
		config.EnvOpenAIKey:      "openai-key",
	}
	providers := []string{config.ProviderAnthropic, config.ProviderOpenAI, config.ProviderAnthropic}
	models := []string{"model-alpha", "model-beta", "model-gamma"}
	for i := 0; i < config.SlotCount; i++ {
		m[fmt.Sprintf(config.EnvSlotProviderFmt, i)] = providers[i]
		m[fmt.Sprintf(config.EnvSlotModelIDFmt, i)] = models[i]
	}
	return m
}

func TestLoadReadsEveryValueFromTheEnvironment(t *testing.T) {
	cfg, err := config.Load(env(validEnv()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.RPCURL != "https://example.invalid/rpc" {
		t.Errorf("RPCURL = %q", cfg.RPCURL)
	}
	if cfg.DisputeAddress != "0xDD00000000000000000000000000000000000DD0" {
		t.Errorf("DisputeAddress = %q", cfg.DisputeAddress)
	}

	wantProviders := []string{config.ProviderAnthropic, config.ProviderOpenAI, config.ProviderAnthropic}
	wantModels := []string{"model-alpha", "model-beta", "model-gamma"}
	wantKeys := []string{"anthropic-key", "openai-key", "anthropic-key"}
	for i, slot := range cfg.Slots {
		if slot.Provider != wantProviders[i] {
			t.Errorf("slot %d provider = %q, want %q", i, slot.Provider, wantProviders[i])
		}
		if slot.ModelID != wantModels[i] {
			t.Errorf("slot %d model = %q, want %q", i, slot.ModelID, wantModels[i])
		}
		if slot.APIKey != wantKeys[i] {
			t.Errorf("slot %d key came from the wrong variable", i)
		}
		if !slot.SendsTemperature {
			t.Errorf("slot %d should default to sending the temperature field", i)
		}
	}
}

// TestTheDefaultConfigurationSpansMoreThanOneVendor is the point of the
// two-vendor rule, asserted on the shipped example rather than only on the
// rejection path.
func TestTheDefaultConfigurationSpansMoreThanOneVendor(t *testing.T) {
	cfg, err := config.Load(env(validEnv()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	seen := map[string]bool{}
	for _, slot := range cfg.Slots {
		seen[slot.Provider] = true
	}
	if len(seen) < 2 {
		t.Fatalf("all slots speak to one vendor: %v", seen)
	}
}

func TestLoadRejects(t *testing.T) {
	cases := []struct {
		name   string
		break_ func(m map[string]string)
		want   error
	}{
		{"missing rpc url", func(m map[string]string) { delete(m, config.EnvRPCURL) }, config.ErrMissing},
		{"blank rpc url", func(m map[string]string) { m[config.EnvRPCURL] = "   " }, config.ErrMissing},
		{"missing address", func(m map[string]string) { delete(m, config.EnvDisputeAddress) }, config.ErrMissing},
		{"address too short", func(m map[string]string) { m[config.EnvDisputeAddress] = "0xdeadbeef" }, config.ErrBadAddress},
		{"address without prefix", func(m map[string]string) {
			m[config.EnvDisputeAddress] = strings.Repeat("a", 42)
		}, config.ErrBadAddress},
		{"address with a non-hex byte", func(m map[string]string) {
			m[config.EnvDisputeAddress] = "0x" + strings.Repeat("z", 40)
		}, config.ErrBadAddress},
		{"missing provider", func(m map[string]string) {
			delete(m, fmt.Sprintf(config.EnvSlotProviderFmt, 1))
		}, config.ErrMissing},
		{"missing model id", func(m map[string]string) {
			delete(m, fmt.Sprintf(config.EnvSlotModelIDFmt, 0))
		}, config.ErrMissing},
		{"missing api key", func(m map[string]string) { delete(m, config.EnvOpenAIKey) }, config.ErrNoAPIKey},
		{"unparseable temperature flag", func(m map[string]string) {
			m[fmt.Sprintf(config.EnvSlotSendTemperatureFmt, 0)] = "sometimes"
		}, config.ErrBadBool},
		{"every slot on one vendor", func(m map[string]string) {
			m[fmt.Sprintf(config.EnvSlotProviderFmt, 1)] = config.ProviderAnthropic
		}, config.ErrSingleVendor},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := validEnv()
			tc.break_(m)
			_, err := config.Load(env(m))
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestNoErrorEverCarriesAKey is the credential rule, asserted rather than
// assumed: a variable is NAMED and a value never is.
func TestNoErrorEverCarriesAKey(t *testing.T) {
	m := validEnv()
	const secret = "sk-this-must-never-appear"
	m[config.EnvAnthropicKey] = secret
	delete(m, config.EnvOpenAIKey)

	_, err := config.Load(env(m))
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("the error leaked a key: %v", err)
	}
	if !strings.Contains(err.Error(), config.EnvOpenAIKey) {
		t.Fatalf("the error should name the missing variable: %v", err)
	}
}

// TestAnUnknownProviderIsCarriedThroughForModelNewToRefuse pins where that rule
// lives. The list of vendors this program can speak to belongs to the package
// that speaks to them, so config carries the name verbatim and finds no key for
// it; model.New is what refuses it, by name.
func TestAnUnknownProviderIsCarriedThroughForModelNewToRefuse(t *testing.T) {
	m := validEnv()
	m[fmt.Sprintf(config.EnvSlotProviderFmt, 2)] = "a-vendor-we-cannot-speak-to"

	cfg, err := config.Load(env(m))
	if err != nil {
		t.Fatalf("config should carry the provider through, not judge it: %v", err)
	}
	if cfg.Slots[2].Provider != "a-vendor-we-cannot-speak-to" {
		t.Errorf("provider = %q", cfg.Slots[2].Provider)
	}
	if cfg.Slots[2].APIKey != "" {
		t.Error("an unknown provider has no key variable, so its key must be empty")
	}
}

func TestTheTemperatureFieldCanBeTurnedOffPerSlot(t *testing.T) {
	m := validEnv()
	m[fmt.Sprintf(config.EnvSlotSendTemperatureFmt, 1)] = "false"

	cfg, err := config.Load(env(m))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Slots[1].SendsTemperature {
		t.Error("slot 1 should not send the temperature field")
	}
	if !cfg.Slots[0].SendsTemperature || !cfg.Slots[2].SendsTemperature {
		t.Error("turning it off for one slot must not affect the others")
	}
}

// TestTheTemperatureValueIsZeroAndUnreachable pins the thing that is NOT
// configurable. Whether the field is sent is environment; what it says is not.
func TestTheTemperatureValueIsZeroAndUnreachable(t *testing.T) {
	if config.Temperature != 0 {
		t.Fatalf("config.Temperature = %d, want 0", config.Temperature)
	}
}

// TestTheExpectedChainIsBaseSepolia pins DIRECT-CHAIN.
func TestTheExpectedChainIsBaseSepolia(t *testing.T) {
	if config.ExpectedChainID != 84532 {
		t.Fatalf("ExpectedChainID = %d, want 84532", config.ExpectedChainID)
	}
}

// TestTheShapeConstantsMatchTheContract keeps the Go side and the Solidity side
// talking about the same dispute.
func TestTheShapeConstantsMatchTheContract(t *testing.T) {
	if config.SlotCount != 3 {
		t.Errorf("SlotCount = %d, want 3", config.SlotCount)
	}
	if config.ItemCount != 5 {
		t.Errorf("ItemCount = %d, want 5", config.ItemCount)
	}
	if config.Quorum != 2 {
		t.Errorf("Quorum = %d, want 2", config.Quorum)
	}
	if config.MaxReasonBytes != 128 {
		t.Errorf("MaxReasonBytes = %d, want 128", config.MaxReasonBytes)
	}
}
