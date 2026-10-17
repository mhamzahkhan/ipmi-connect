package bmc

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestFormatErrorRedactsURLQuery(t *testing.T) {
	base := &url.Error{Op: "Post", URL: "https://192.0.2.10/data?ST1=deadbeef&set=pwState:1", Err: errors.New("timeout")}

	err := FormatError("Dell PowerControl", base)

	if strings.Contains(err.Error(), "deadbeef") {
		t.Errorf("token leaked: %v", err)
	}
	if !strings.Contains(err.Error(), "https://192.0.2.10/data") {
		t.Errorf("URL path lost: %v", err)
	}
	if !errors.Is(err, base.Err) {
		t.Errorf("underlying error not preserved: %v", err)
	}
	if base.URL != "https://192.0.2.10/data?ST1=deadbeef&set=pwState:1" {
		t.Errorf("caller's error was mutated: %q", base.URL)
	}
}

func TestFormatErrorLeavesOtherErrorsAlone(t *testing.T) {
	err := FormatError("op", errors.New("plain"))
	if err.Error() != "[op] plain" {
		t.Errorf("got %q", err)
	}
}
