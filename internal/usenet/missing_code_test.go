package usenet

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/usenet/nntptest"
)

// A 451 (takedown) and a 430 (absent) both decide "missing" — the retry
// and failover semantics must not change — but the original NNTP code
// has to stay observable on the returned error (errors.As
// *ProtocolError / the error string that logs and health reasons carry),
// instead of collapsing into an indistinguishable "article missing".
func TestMissingHealthErrorDistinguishes451From430(t *testing.T) {
	errs := map[int]error{}
	for _, code := range []int{430, 451} {
		s := nntptest.Start(t, nntptest.Options{})
		addSeg(t, s, "gone@x", []byte("alpha"))
		s.SetFault(func(cmd, arg string) nntptest.Fault {
			if cmd == "BODY" {
				return nntptest.Fault{Code: code, Text: "0 <" + arg + ">"}
			}
			return nntptest.Fault{}
		})
		f := newFetcher(t, []config.UsenetProvider{s.Provider("p", 1)}, Options{})
		_, err := f.Body(context.Background(), "gone@x", nil, core.PriorityLive)
		if err == nil {
			t.Fatalf("code %d: Body succeeded, want missing", code)
		}
		// Decision unchanged for both codes.
		if !errors.Is(err, core.ErrArticleMissing) {
			t.Fatalf("code %d: err = %v, want ErrArticleMissing", code, err)
		}
		if !errors.Is(err, ErrNoSuchArticle) {
			t.Fatalf("code %d: err = %v, want it to still wrap ErrNoSuchArticle", code, err)
		}
		var pe *ProtocolError
		if !errors.As(err, &pe) {
			t.Fatalf("code %d: err = %v, want a wrapped *ProtocolError", code, err)
		}
		if pe.Code != code {
			t.Fatalf("ProtocolError.Code = %d, want %d (err = %v)", pe.Code, code, err)
		}
		if !strings.Contains(err.Error(), strconv.Itoa(code)) {
			t.Fatalf("code %d: error string %q does not carry the NNTP code", code, err)
		}
		errs[code] = err
	}
	if errs[430].Error() == errs[451].Error() {
		t.Fatalf("451 and 430 errors are indistinguishable: %q", errs[451])
	}
}
