package supermicro

import (
	"errors"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
)

// Ticket is the one-time KVM login the BMC hands out in its iKVM JNLP. Tokens are single use and
// short-lived: fetch a new ticket for every connection.
type Ticket struct {
	User, Password string
	Port           int
	TLS            bool // "KVM SSL" firmware; not supported yet
}

// maxTicketToken rejects implausible KVM tokens. The BMC issues 16-character tokens and the
// login carries at most 23 bytes of each (see credentials), so anything this long is not a ticket.
const maxTicketToken = 256

var argumentRE = regexp.MustCompile(`<argument>([^<]*)</argument>`)

// parseTicket reads the JNLP arguments. They are anchored on the argument equal to the host
// (some firmware puts extra arguments first): host, user, password, (unused), port, ..., and
// on TLS firmware a "1" flag followed by the TLS port.
func parseTicket(body, host string) (Ticket, error) {
	if !strings.Contains(body, "<jnlp") {
		return Ticket{}, errors.New("BMC did not return a JNLP (session expired?)")
	}
	var args []string
	for _, m := range argumentRE.FindAllStringSubmatch(body, -1) {
		args = append(args, html.UnescapeString(strings.TrimSpace(m[1])))
	}
	i := 0
	for k, a := range args {
		if a == host {
			i = k
			break
		}
	}
	if len(args) < i+3 {
		return Ticket{}, errors.New("JNLP has no KVM credentials")
	}
	if len(args[i+1]) > maxTicketToken || len(args[i+2]) > maxTicketToken {
		return Ticket{}, fmt.Errorf("JNLP KVM credentials longer than %d bytes are not plausible", maxTicketToken)
	}
	t := Ticket{User: args[i+1], Password: args[i+2], Port: 5900}
	if len(args) > i+4 {
		if p, err := strconv.Atoi(args[i+4]); err == nil && p > 0 && p < 65536 {
			t.Port = p
		}
	}
	if len(args) > i+9 && args[i+8] == "1" {
		t.TLS = true
		if p, err := strconv.Atoi(args[i+9]); err == nil && p > 0 && p < 65536 {
			t.Port = p
		}
	}
	return t, nil
}
