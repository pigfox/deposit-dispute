package app

import (
	"strings"
	"testing"
)

// TestJSONLineEncodesASubmission covers the ordinary path: a submission is one
// machine-readable line, and the reason survives whatever whitespace the model
// put in it.
func TestJSONLineEncodesASubmission(t *testing.T) {
	got := jsonLine(Submission{
		Slot: 2,
		Item: 4,
		Args: []string{"send", "0xabc", `the reason has spaces, and "quotes"`},
	})

	for _, want := range []string{`"slot":2`, `"item":4`, `\"quotes\"`, "the reason has spaces"} {
		if !strings.Contains(got, want) {
			t.Errorf("encoded line is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\n") {
		t.Error("a submission must be one line")
	}
}

// TestJSONLineReportsAnEncodingFailure keeps the failure branch honest rather
// than unreachable. A channel cannot be marshalled, which is exactly the shape
// of thing that would otherwise be silently dropped from the log.
func TestJSONLineReportsAnEncodingFailure(t *testing.T) {
	got := jsonLine(make(chan int))
	if !strings.Contains(got, `"error"`) {
		t.Fatalf("an unencodable value must report, not vanish: %s", got)
	}
}
