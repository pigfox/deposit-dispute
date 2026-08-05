package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Sentinel errors. Callers match these with errors.Is.
var (
	// ErrMissing means a required environment variable was absent or blank.
	ErrMissing = errors.New("config: required environment variable is not set")
	// ErrNoAPIKey means a slot's vendor has no key in the environment.
	ErrNoAPIKey = errors.New("config: no API key for provider")
	// ErrBadBool means a boolean environment variable was not parseable.
	ErrBadBool = errors.New("config: value is not a boolean")
	// ErrBadAddress means the dispute address is not a 20-byte hex string.
	ErrBadAddress = errors.New("config: not a 0x-prefixed 20-byte address")
	// ErrSingleVendor means every slot named the same vendor.
	//
	// THIS CHECK LIVES HERE AND NOWHERE ELSE, because this is the only layer that
	// can see it. The contract stores model identifier hashes and has no idea
	// which company serves them, and the panel sees clients rather than provider
	// names. Three distinct models from one vendor share a tokenizer, a safety
	// layer, a serving stack and an outage — a correlation a 2-of-3 threshold
	// silently assumes away.
	ErrSingleVendor = errors.New("config: every slot speaks to one vendor")
)

// Getenv is the environment seam. A function rather than a direct os.Getenv call
// so a test supplies an environment without mutating the process's own.
type Getenv func(key string) string

// Slot is one adjudicator's configuration.
type Slot struct {
	// Provider is the vendor this slot speaks to.
	Provider string
	// ModelID is the pinned model identifier. Its keccak256 is what the deployed
	// contract holds for this slot, and the chain package checks that.
	ModelID string
	// APIKey authenticates to Provider. Never logged, never printed, never
	// included in an error.
	APIKey string
	// SendsTemperature says whether the temperature field is put on the wire.
	// The value, when sent, is always zero.
	SendsTemperature bool
}

// Config is everything the adjudicator needs to run.
type Config struct {
	// RPCURL is the Base Sepolia endpoint. Never logged: an endpoint often
	// carries a project key in its path.
	RPCURL string
	// DisputeAddress is the DepositDispute this run adjudicates.
	DisputeAddress string
	// Slots are the three adjudicators, positionally matching the contract's.
	Slots [SlotCount]Slot
}

// Load reads the whole configuration from the environment and refuses anything
// it cannot fully justify.
//
// EVERY CHECK HERE IS A REFUSAL, NOT A DEFAULT. A missing model identifier is not
// filled in from a table, because the identifier is what the contract pinned and
// guessing it would produce a run that looks correct and answers for a model
// nobody registered. The one thing that does default is whether the temperature
// field is sent, which is a property of a vendor's API rather than of this
// dispute.
func Load(getenv Getenv) (Config, error) {
	var cfg Config

	rpc, err := required(getenv, EnvRPCURL)
	if err != nil {
		return Config{}, err
	}
	cfg.RPCURL = rpc

	addr, err := required(getenv, EnvDisputeAddress)
	if err != nil {
		return Config{}, err
	}
	if !isAddress(addr) {
		return Config{}, fmt.Errorf("%w: %s=%q", ErrBadAddress, EnvDisputeAddress, addr)
	}
	cfg.DisputeAddress = addr

	for i := 0; i < SlotCount; i++ {
		slot, err := loadSlot(getenv, i)
		if err != nil {
			return Config{}, err
		}
		cfg.Slots[i] = slot
	}

	if err := checkDistinct(cfg.Slots); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// loadSlot reads one adjudicator's configuration.
func loadSlot(getenv Getenv, index int) (Slot, error) {
	provider, err := required(getenv, fmt.Sprintf(EnvSlotProviderFmt, index))
	if err != nil {
		return Slot{}, err
	}

	modelID, err := required(getenv, fmt.Sprintf(EnvSlotModelIDFmt, index))
	if err != nil {
		return Slot{}, err
	}

	// AN UNKNOWN PROVIDER IS NOT REJECTED HERE. The list of vendors this program
	// can actually speak to belongs to the package that speaks to them, and
	// model.New is where that switch lives. Duplicating it here would be a second
	// copy of the same fact and would make model.New's own refusal unreachable.
	// What this layer knows is which environment variable holds which key, so an
	// unknown provider simply has no key to look up and model.New refuses it by
	// name.
	apiKey := ""
	if keyVar, known := apiKeyVar(provider); known {
		apiKey = strings.TrimSpace(getenv(keyVar))
		if apiKey == "" {
			// The variable is NAMED and the value is not, which is the whole rule
			// for talking about credentials in this program.
			return Slot{}, fmt.Errorf("%w: slot %d needs %s", ErrNoAPIKey, index, keyVar)
		}
	}

	sendTemp, err := optionalBool(getenv, fmt.Sprintf(EnvSlotSendTemperatureFmt, index), DefaultSendTemperature)
	if err != nil {
		return Slot{}, err
	}

	return Slot{Provider: provider, ModelID: modelID, APIKey: apiKey, SendsTemperature: sendTemp}, nil
}

// apiKeyVar maps a provider to the environment variable holding its key.
func apiKeyVar(provider string) (string, bool) {
	switch provider {
	case ProviderAnthropic:
		return EnvAnthropicKey, true
	case ProviderOpenAI:
		return EnvOpenAIKey, true
	default:
		return "", false
	}
}

// checkDistinct enforces the one thing about the panel only this layer can see.
//
// DISTINCT MODELS ARE NOT CHECKED HERE, deliberately. panel.New checks that, and
// the contract checks it again on the hashes at construction. A third copy in
// this package would be the same rule in three places, which is how the three
// stop agreeing.
func checkDistinct(slots [SlotCount]Slot) error {
	first := slots[0].Provider
	for i := 1; i < SlotCount; i++ {
		if slots[i].Provider != first {
			return nil
		}
	}
	return fmt.Errorf("%w: all %d slots name %q", ErrSingleVendor, SlotCount, first)
}

// required reads a variable that has no default.
func required(getenv Getenv, key string) (string, error) {
	v := strings.TrimSpace(getenv(key))
	if v == "" {
		return "", fmt.Errorf("%w: %s", ErrMissing, key)
	}
	return v, nil
}

// optionalBool reads a boolean variable, falling back when it is absent.
func optionalBool(getenv Getenv, key string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%w: %s=%q", ErrBadBool, key, raw)
	}
	return v, nil
}

// addressHexLen is the length of a 0x-prefixed 20-byte address.
const addressHexLen = 42

// isAddress reports whether s is a 0x-prefixed 20-byte hex string.
//
// CASE IS NOT CHECKED HERE, deliberately. EIP-55 casing is enforced by the
// pipeline's address-checksum gate over every address literal this repository
// tracks; an address arriving from the environment at run time is checked for
// SHAPE here and for existence by the chain read that follows, which is a
// stronger answer than re-deriving a checksum in a second place.
func isAddress(s string) bool {
	if len(s) != addressHexLen || !strings.HasPrefix(s, "0x") {
		return false
	}
	for _, c := range s[2:] {
		isDigit := c >= '0' && c <= '9'
		isLower := c >= 'a' && c <= 'f'
		isUpper := c >= 'A' && c <= 'F'
		if !isDigit && !isLower && !isUpper {
			return false
		}
	}
	return true
}
