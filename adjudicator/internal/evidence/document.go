package evidence

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/pigfox/deposit-dispute/adjudicator/internal/config"
)

// Sentinel errors for the published document.
var (
	// ErrDecodeDocument means the bundle was not valid JSON.
	ErrDecodeDocument = errors.New("evidence: decoding the bundle")
	// ErrReadDocument means the bundle could not be read.
	ErrReadDocument = errors.New("evidence: reading the bundle")
	// ErrItemCount means the bundle did not carry exactly config.ItemCount items.
	ErrItemCount = errors.New("evidence: the bundle must carry exactly the scheduled items")
	// ErrEmptyField means a required field of an item was blank.
	ErrEmptyField = errors.New("evidence: item field is empty")
)

// DocumentItem is one line item's published evidence.
type DocumentItem struct {
	// Description is the deduction being claimed, as the adjudicators see it.
	Description string `json:"description"`
	// AmountWei is what the landlord claims, shown for context only. No model is
	// ever asked for an amount and no reply can carry one.
	AmountWei string `json:"amountWei"`
	// Evidence is the material filed for this deduction.
	Evidence string `json:"evidence"`
}

// Document is the evidence bundle the landlord publishes and commits to.
//
// IT IS A DOCUMENT, NOT A FIXTURE. The merkle root over its items is what a
// filed claim commits to, and what a third party re-running the adjudication
// reproduces. Changing it after a claim is filed does not re-point the
// commitment; it makes the published bundle stop being the one the chain
// answers to.
type Document struct {
	// Dispute is the address this bundle was published for. Recorded so a bundle
	// cannot be quietly re-used against a different dispute.
	Dispute string `json:"dispute"`
	// Items are the five deductions, in schedule order.
	Items []DocumentItem `json:"items"`
}

// Load reads and validates a published bundle, returning it together with the
// per-item hashes it commits to.
//
// THE HASHES COME BACK WITH THE DOCUMENT because validating twice is how the two
// answers start disagreeing. Once Validate has passed, the document has exactly
// config.ItemCount items and hashing them cannot fail — so a caller that had to
// ask for the hashes separately would be writing an error branch nothing could
// ever take.
func Load(r io.Reader) (Document, Bundle, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return Document{}, Bundle{}, fmt.Errorf("%w: %w", ErrReadDocument, err)
	}
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Document{}, Bundle{}, fmt.Errorf("%w: %w", ErrDecodeDocument, err)
	}
	if err := doc.Validate(); err != nil {
		return Document{}, Bundle{}, err
	}

	var b Bundle
	for i := range b {
		b[i] = doc.itemHashAt(i)
	}
	return doc, b, nil
}

// Validate refuses a bundle the adjudicator could not honestly work from.
func (d Document) Validate() error {
	if len(d.Items) != config.ItemCount {
		return fmt.Errorf("%w: %d, want %d", ErrItemCount, len(d.Items), config.ItemCount)
	}
	for i, item := range d.Items {
		if strings.TrimSpace(item.Description) == "" {
			return fmt.Errorf("%w: item %d has no description", ErrEmptyField, i)
		}
		if strings.TrimSpace(item.AmountWei) == "" {
			return fmt.Errorf("%w: item %d has no amount", ErrEmptyField, i)
		}
		if strings.TrimSpace(item.Evidence) == "" {
			// An item with no evidence is not an item the landlord can establish,
			// and filing one would put a model in the position of deciding on
			// nothing. It is refused here rather than answered NOT_ESTABLISHED by
			// a model that was asked a question with no content.
			return fmt.Errorf("%w: item %d has no evidence", ErrEmptyField, i)
		}
	}
	return nil
}

// ItemHash is the per-item evidence hash committed at leaf `index`.
//
// keccak256 OVER THE EVIDENCE TEXT, and nothing else. Not the description and not
// the amount: those are fixed in the contract's constructor and are already
// immutable, whereas the evidence is the thing being committed to. Keeping the
// hash over exactly the evidence means a third party can reproduce it from the
// published bundle without reconstructing a serialization format.
func (d Document) ItemHash(index int) (Hash, error) {
	if index < 0 || index >= len(d.Items) {
		return Hash{}, fmt.Errorf("%w: %d", ErrIndexOutOfRange, index)
	}
	return d.itemHashAt(index), nil
}

// itemHashAt is ItemHash without the bounds check, for callers that have already
// validated the document and are walking its fixed length.
func (d Document) itemHashAt(index int) Hash {
	return Keccak([]byte(d.Items[index].Evidence))
}

// Bundle is the five per-item hashes, in schedule order.
//
// Validate runs first and is the only thing that can fail here: once it passes,
// the document has exactly config.ItemCount items and every index below is in
// range.
func (d Document) Bundle() (Bundle, error) {
	var b Bundle
	if err := d.Validate(); err != nil {
		return b, err
	}
	for i := range b {
		b[i] = d.itemHashAt(i)
	}
	return b, nil
}
