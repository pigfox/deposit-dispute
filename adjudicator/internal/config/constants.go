// Package config holds every value the adjudicator reads from its environment
// and every constant it would otherwise write at a call site.
//
// NOTHING IN THE ESTATE'S DECISION PATH IS A LITERAL WHERE IT IS USED. A model
// identifier, a threshold, an endpoint or a header name written inline is a value
// nobody can grep for and nobody can change in one place. They live here.
package config

// The shape of a dispute. These MIRROR the contract's own public constants and
// are verified against it — see the chain package, which reads the registered
// adjudicators back and refuses to run when the running configuration and the
// deployed one disagree.
const (
	// SlotCount is how many adjudicators a dispute registers. Fixed at three.
	SlotCount = 3
	// ItemCount is how many deduction line items a dispute has. Fixed at five.
	ItemCount = 5
	// Quorum is how many agreeing findings freeze one line item.
	Quorum = 2
	// MaxReasonBytes bounds the reason string the chain stores verbatim.
	MaxReasonBytes = 128
)

// ExpectedChainID is Base Sepolia, and it is the only chain this program will
// talk to.
//
// DIRECT-CHAIN ONLY. The chain id is read back from the endpoint and compared
// against this value before anything else happens, so pointing the adjudicator
// at some other endpoint stops the program rather than producing verdicts that
// answer to a contract nobody deployed.
const ExpectedChainID uint64 = 84532

// Provider identifiers. A slot names one of these; anything else is refused at
// construction rather than at the first call.
const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai"
)

// Model call parameters.
const (
	// Temperature is zero and is not a setting. It is not exposed as a
	// configurable value anywhere, because a panel whose members could be run
	// warm is a panel whose agreement means less.
	Temperature = 0
	// MaxTokens bounds a reply. A decision plus a bounded reason plus a short
	// narrative fits comfortably; a model that needs more than this is not
	// answering the question it was asked.
	MaxTokens = 1024
)

// Vendor endpoints.
const (
	AnthropicBaseURL = "https://api.anthropic.com/v1/messages"
	OpenAIBaseURL    = "https://api.openai.com/v1/chat/completions"
	// AnthropicVersion is the dated API version header value the Messages API
	// requires. Pinned, like every other version in this estate.
	AnthropicVersion = "2023-06-01"
)

// HTTP wire details.
const (
	HTTPMethodPost         = "POST"
	HTTPStatusOK           = 200
	HeaderAuthorization    = "Authorization"
	HeaderContentType      = "Content-Type"
	HeaderAPIKey           = "x-api-key"
	HeaderAnthropicVersion = "anthropic-version"
	BearerPrefix           = "Bearer "
	ContentTypeJSON        = "application/json"
)

// Message roles.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Reply parsing.
const (
	// FenceDelimiter opens and closes a markdown code fence. Tolerated as
	// TRANSPORT and nothing more — see model.ParseReply.
	FenceDelimiter = "```"
	// ErrorBodyMaxLen bounds how much of a vendor error body reaches the log.
	ErrorBodyMaxLen = 512
	// TruncationSuffix marks a bounded error body.
	TruncationSuffix = "…(truncated)"
)

// The finding vocabulary a model is permitted to answer with. These are the
// ONLY two strings that parse. There is no abstain token, deliberately: the
// contract's zero value is NotEstablished and insufficient evidence collapses
// into it, so a model declining to decide is a model saying NOT_ESTABLISHED.
const (
	FindingNotEstablished = "NOT_ESTABLISHED"
	FindingEstablished    = "ESTABLISHED"
)

// Environment variable names. Every one of them; there is no other source of
// configuration and no defaults for anything that identifies a model or a
// contract.
const (
	// EnvRPCURL is the Base Sepolia endpoint, shared with `forge` rather than
	// duplicated. Two variables naming one endpoint is two things that can drift.
	EnvRPCURL         = "RPC_URL"
	EnvDisputeAddress = "DD_DISPUTE_ADDRESS"
	EnvAnthropicKey   = "ANTHROPIC_API_KEY"
	EnvOpenAIKey      = "OPENAI_API_KEY"

	// EnvSlotProviderFmt names the vendor a slot speaks to, formatted with the
	// slot index.
	EnvSlotProviderFmt = "DD_SLOT%d_PROVIDER"
	// EnvSlotModelIDFmt names the pinned model identifier for a slot. This is
	// the string whose keccak256 the contract holds, so a typo here is caught by
	// the chain check rather than by a confusing verdict.
	EnvSlotModelIDFmt = "DD_SLOT%d_MODEL_ID"
	// EnvSlotSendTemperatureFmt says whether the temperature field is sent to
	// this slot's model at all.
	//
	// WHETHER THE FIELD IS SENT IS A FACT ABOUT THE VENDOR'S API, NOT A SETTING.
	// The VALUE is always zero and cannot be changed from anywhere. But some
	// models reject the field outright rather than ignoring it, and a request
	// carrying a parameter the model refuses does not run warm — it does not run
	// at all. That is a per-model fact that must be correctable without a
	// rebuild, so it is environment rather than a table compiled in here.
	EnvSlotSendTemperatureFmt = "DD_SLOT%d_SEND_TEMPERATURE"
)

// DefaultSendTemperature is what a slot does when its environment says nothing.
// Sending the field is the common case; a model that rejects it is the exception
// and is opted out explicitly.
const DefaultSendTemperature = true

// Contract function signatures, in the form `cast call` wants them. Held here so
// a signature appears exactly once in the program.
const (
	SigAdjudicatorAt = "adjudicatorAt(uint256)(address,string)"
	SigModelIDHashAt = "modelIdHashAt(uint256)(bytes32)"
	SigLandlord      = "LANDLORD()(address)"
	SigTenant        = "TENANT()(address)"
	SigEvidenceRoot  = "evidenceRoot()(bytes32)"
	SigSubmitVerdict = "submitVerdict(uint256,uint8,bytes32,bytes32[],bytes32,bytes32,string)"
)

// Log message formats. Kept out of the call sites for the same reason as
// everything else here.
const (
	LogSpendSurfaceHeader = "spend surface — these calls are metered:"
	LogSpendSurfaceOne    = "  slot %d: model %q at %s (temperature field %s)"
	LogTemperatureSent    = "sent, value 0"
	LogTemperatureOmitted = "omitted"
	LogAskingItem         = "item %d: asking slot %d (%s)"
	LogResolvedModel      = "item %d: slot %d answered as %q (requested %q)"
	LogRefusal            = "item %d: slot %d REFUSED: %v"
	LogItemOutcome        = "item %d: %d/%d established, %d refusals"
	LogChainVerified      = "chain: all %d slots match the deployed adjudicator set"
)
