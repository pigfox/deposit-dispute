package evidence_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/pigfox/deposit-dispute/adjudicator/internal/config"
	"github.com/pigfox/deposit-dispute/adjudicator/internal/evidence"
)

// failingReader stands in for a file that cannot be read.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("disk went away") }

const goodBundle = `{
  "dispute": "0xDD00000000000000000000000000000000000DD0",
  "items": [
    {"description": "carpet", "amountWei": "100000000000000000", "evidence": "photographs, check-in report"},
    {"description": "wall",   "amountWei": "200000000000000000", "evidence": "photographs of fixings"},
    {"description": "window", "amountWei": "300000000000000000", "evidence": "glazier invoice"},
    {"description": "door",   "amountWei": "400000000000000000", "evidence": "locksmith invoice"},
    {"description": "clean",  "amountWei": "900000000000000000", "evidence": "cleaning invoice"}
  ]
}`

func TestLoadReadsAPublishedBundle(t *testing.T) {
	doc, _, err := evidence.Load(strings.NewReader(goodBundle))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(doc.Items) != config.ItemCount {
		t.Fatalf("got %d items, want %d", len(doc.Items), config.ItemCount)
	}
	if doc.Items[0].Description != "carpet" {
		t.Errorf("item 0 description = %q", doc.Items[0].Description)
	}
	if doc.Dispute == "" {
		t.Error("the bundle should record which dispute it was published for")
	}
}

func TestLoadRejects(t *testing.T) {
	cases := []struct {
		name string
		body string
		want error
	}{
		{"not json", "this is not json", evidence.ErrDecodeDocument},
		{"too few items", `{"items":[{"description":"a","amountWei":"1","evidence":"e"}]}`, evidence.ErrItemCount},
		{"no items at all", `{"items":[]}`, evidence.ErrItemCount},
		{
			"an item with no description",
			strings.Replace(goodBundle, `"description": "carpet"`, `"description": "  "`, 1),
			evidence.ErrEmptyField,
		},
		{
			"an item with no amount",
			strings.Replace(goodBundle, `"amountWei": "200000000000000000"`, `"amountWei": ""`, 1),
			evidence.ErrEmptyField,
		},
		{
			"an item with no evidence",
			strings.Replace(goodBundle, `"evidence": "glazier invoice"`, `"evidence": ""`, 1),
			evidence.ErrEmptyField,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := evidence.Load(strings.NewReader(tc.body)); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestLoadReportsAnUnreadableBundle(t *testing.T) {
	if _, _, err := evidence.Load(failingReader{}); !errors.Is(err, evidence.ErrReadDocument) {
		t.Fatalf("err = %v, want ErrReadDocument", err)
	}
}

func TestTooManyItemsIsRefusedLikeTooFew(t *testing.T) {
	sixth := `{"description": "extra", "amountWei": "1", "evidence": "e"}`
	body := strings.Replace(goodBundle, `"evidence": "cleaning invoice"}`, `"evidence": "cleaning invoice"},`+sixth, 1)

	if _, _, err := evidence.Load(strings.NewReader(body)); !errors.Is(err, evidence.ErrItemCount) {
		t.Fatalf("err = %v, want ErrItemCount", err)
	}
}

func TestItemHashCoversTheEvidenceAndNothingElse(t *testing.T) {
	doc, _, err := evidence.Load(strings.NewReader(goodBundle))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	got, err := doc.ItemHash(2)
	if err != nil {
		t.Fatalf("ItemHash: %v", err)
	}
	want := evidence.Keccak([]byte(doc.Items[2].Evidence))
	if got != want {
		t.Fatal("ItemHash is not keccak256 over exactly the evidence text")
	}

	// Changing the description must NOT move the hash: the description is fixed
	// in the contract's constructor and is already immutable.
	doc.Items[2].Description = "a different name for the same thing"
	again, err := doc.ItemHash(2)
	if err != nil {
		t.Fatalf("ItemHash: %v", err)
	}
	if again != got {
		t.Fatal("the description must not be inside the committed hash")
	}
}

func TestItemHashRejectsAnIndexOffTheSchedule(t *testing.T) {
	doc, _, err := evidence.Load(strings.NewReader(goodBundle))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, index := range []int{-1, config.ItemCount} {
		if _, err := doc.ItemHash(index); !errors.Is(err, evidence.ErrIndexOutOfRange) {
			t.Errorf("ItemHash(%d) err = %v, want ErrIndexOutOfRange", index, err)
		}
	}
}

func TestBundleIsTheFiveItemHashesInScheduleOrder(t *testing.T) {
	doc, _, err := evidence.Load(strings.NewReader(goodBundle))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	bundle, err := doc.Bundle()
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	for i := range bundle {
		want := evidence.Keccak([]byte(doc.Items[i].Evidence))
		if bundle[i] != want {
			t.Errorf("bundle[%d] is not item %d's evidence hash", i, i)
		}
	}

	// Every leaf must differ, or two items would share a commitment.
	seen := map[evidence.Hash]bool{}
	for _, h := range bundle {
		if seen[h] {
			t.Fatal("two items produced the same evidence hash")
		}
		seen[h] = true
	}
}

func TestBundleRefusesAnInvalidDocument(t *testing.T) {
	doc := evidence.Document{Items: []evidence.DocumentItem{{Description: "a", AmountWei: "1", Evidence: "e"}}}
	if _, err := doc.Bundle(); !errors.Is(err, evidence.ErrItemCount) {
		t.Fatalf("err = %v, want ErrItemCount", err)
	}
}
