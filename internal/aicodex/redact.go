package aicodex

import (
	"regexp"
	"strings"
)

// secretRedactor is intentionally used before data is put into an Event or
// stderr capture.  Keeping only redacted data makes accidental logging by a
// caller harmless.  The pending suffix handles a known secret split across
// two stderr writes.
type secretRedactor struct {
	secrets []string
	pending string
	keep    int
}

func newRedactor(secrets []string) *secretRedactor {
	unique := make([]string, 0, len(secrets))
	seen := make(map[string]struct{}, len(secrets))
	max := 1
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		if _, ok := seen[secret]; ok {
			continue
		}
		seen[secret] = struct{}{}
		unique = append(unique, secret)
		if len(secret) > max {
			max = len(secret)
		}
	}
	return &secretRedactor{secrets: unique, keep: max - 1}
}

// Redact sanitizes a complete line or error string.  It also removes common
// key/token values when a config or tool accidentally prints a credential we
// did not receive through SecretProvider.
func (r *secretRedactor) Redact(value string) string {
	if r == nil {
		return value
	}
	for _, secret := range r.secrets {
		value = strings.ReplaceAll(value, secret, "<redacted>")
	}
	return redactCredentialAssignments(value)
}

// RedactJSON only replaces exact server-known secret values. Applying the
// loose key=value heuristic to JSON could turn an unquoted numeric or null
// field into invalid JSON, so event parsing uses this stricter operation.
func (r *secretRedactor) RedactJSON(value string) string {
	if r == nil {
		return value
	}
	for _, secret := range r.secrets {
		value = strings.ReplaceAll(value, secret, "<redacted>")
	}
	return value
}

var credentialAssignmentPattern = regexp.MustCompile(`(?i)((?:authorization|api[_ -]?key|access[_ -]?token|bearer[_ -]?token|password|secret|token)\s*[:=]\s*(?:bearer\s+)?["']?)([^\s"',;}]*)`)

func redactCredentialAssignments(value string) string {
	return credentialAssignmentPattern.ReplaceAllString(value, `${1}<redacted>`)
}

func (r *secretRedactor) processChunk(value string) string {
	if r == nil || value == "" {
		return value
	}
	combined := r.pending + value
	if r.keep == 0 {
		r.pending = ""
		return r.Redact(combined)
	}
	if len(combined) <= r.keep {
		r.pending = combined
		return ""
	}
	cut := len(combined) - r.keep
	out := combined[:cut]
	r.pending = combined[cut:]
	return r.Redact(out)
}

func (r *secretRedactor) flush() string {
	if r == nil {
		return ""
	}
	out := r.Redact(r.pending)
	r.pending = ""
	return out
}

type boundedCapture struct {
	max       int
	buf       []byte
	truncated bool
	redactor  *secretRedactor
}

func newBoundedCapture(max int, redactor *secretRedactor) *boundedCapture {
	if max <= 0 {
		max = 64 << 10
	}
	return &boundedCapture{max: max, redactor: redactor}
}

func (c *boundedCapture) Write(p []byte) (int, error) {
	if c == nil {
		return len(p), nil
	}
	text := ""
	if c.redactor != nil {
		text = c.redactor.processChunk(string(p))
	} else {
		text = string(p)
	}
	c.append([]byte(text))
	return len(p), nil
}

func (c *boundedCapture) Close() {
	if c == nil || c.redactor == nil {
		return
	}
	c.append([]byte(c.redactor.flush()))
}

func (c *boundedCapture) append(p []byte) {
	if len(p) == 0 {
		return
	}
	if len(p) >= c.max {
		c.buf = append(c.buf[:0], p[len(p)-c.max:]...)
		c.truncated = true
		return
	}
	c.buf = append(c.buf, p...)
	if len(c.buf) > c.max {
		over := len(c.buf) - c.max
		copy(c.buf, c.buf[over:])
		c.buf = c.buf[:c.max]
		c.truncated = true
	}
}

func (c *boundedCapture) String() string {
	if c == nil {
		return ""
	}
	if c.truncated {
		return "<truncated>" + string(c.buf)
	}
	return string(c.buf)
}
