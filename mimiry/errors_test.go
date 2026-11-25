package mimiry

import (
	"strings"
	"testing"
)

// A validation failure names the field and the reason. The CLI prints
// Error(), and it used to print only "Request validation failed".
func TestAPIErrorNamesTheFieldsThatFailed(t *testing.T) {
	e := newAPIError("POST", "/api/compute/v1/sessions", 400, []byte(`{"error":"validation_failed",
		"message":"Request validation failed",
		"errors":[{"field":"image.uri","message":"image URI must be from an allowed registry"},
		          {"field":"gpu","message":"at least one of gpu.types … is required"}]}`))
	got := e.Error()
	for _, want := range []string{
		"validation_failed: Request validation failed",
		"\n  image.uri: image URI must be from an allowed registry",
		"\n  gpu: at least one of gpu.types … is required",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Error() = %q\nwant it to contain %q", got, want)
		}
	}

	e = newAPIError("POST", "/x", 400, []byte(`{"error":"invalid_request","message":"bad","fields":{"zeta":"z","alpha":"a"}}`))
	if got := e.Error(); !strings.HasSuffix(got, "\n  alpha: a\n  zeta: z") {
		t.Errorf("Error() = %q, want the fields in order at the end", got)
	}
}
