package sacli

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

// rawFieldKind mirrors aigame's wire kinds for the named protocol.
type rawFieldKind int

const (
	rawInt rawFieldKind = iota
	rawString
)

// rawSchemas mirrors internal/aigame/protocol.go's clientSchemas: the exact
// field list of every client function. `send` validates against it so a
// mistyped call is refused with the signature instead of reaching the server.
var rawSchemas = map[string][]rawFieldKind{
	"EV":           {rawInt, rawInt, rawInt, rawInt, rawInt},
	"EN":           {rawInt, rawInt},
	"DU":           {rawInt, rawInt},
	"EO":           {rawInt},
	"BU":           {rawInt},
	"JB":           {rawInt, rawInt},
	"LB":           {rawInt, rawInt},
	"SKD":          {rawInt, rawInt},
	"PI":           {rawInt, rawInt, rawInt},
	"DI":           {rawInt, rawInt, rawInt},
	"DG":           {rawInt, rawInt, rawInt},
	"DP":           {rawInt, rawInt, rawInt},
	"MI":           {rawInt, rawInt},
	"MSG":          {rawInt, rawString, rawInt},
	"PMSG":         {rawInt, rawInt, rawInt, rawString, rawInt},
	"AB":           {},
	"DAB":          {rawInt},
	"AAB":          {rawInt, rawInt},
	"M":            {rawInt, rawInt, rawInt, rawInt, rawInt},
	"C":            {rawInt},
	"S":            {rawString},
	"HL":           {rawInt},
	"KS":           {rawInt},
	"AC":           {rawInt, rawInt, rawInt},
	"MU":           {rawInt, rawInt, rawInt, rawInt},
	"PS":           {rawInt, rawInt, rawInt, rawString},
	"ST":           {rawInt},
	"DT":           {rawInt},
	"FT":           {rawString},
	"KN":           {rawInt, rawString},
	"SP":           {rawInt, rawInt, rawInt},
	"ProcGet":      {},
	"PlayerNumGet": {},
	"Echo":         {rawString},
	"Shutdown":     {rawString, rawInt},
	"FM":           {rawString},
	"MA":           {rawInt, rawInt, rawInt},
}

// typedFunctions are reachable only through their dedicated command: aigame
// refuses them here to keep validation in one place.
var typedFunctions = map[string]string{
	"W": "sactl walk / sactl goto", "w": "sactl walk",
	"L": "sactl look", "TK": "sactl talk / sactl say",
	"WN": "sactl choose / sactl reply", "B": "sactl battle",
	"PR": "sactl party", "ID": "sactl item use",
	"MI": "sactl item move", "PETST": "sactl pet status",
	"SPET": "sactl pet standby", "SKUP": "sactl alloc",
	"FS": "sactl social", "TD": "trade commands are not exposed yet",
}

// commandFunctions lists the raw-callable functions with their signatures.
func (s *Server) commandFunctions(ctx context.Context, request Request) Response {
	names := make([]string, 0, len(rawSchemas))
	for name := range rawSchemas {
		names = append(names, name)
	}
	sort.Strings(names)
	lines := make([]string, 0, len(names))
	for _, name := range names {
		lines = append(lines, fmt.Sprintf("%s(%s)", name, signatureOf(name)))
	}
	return Response{
		OK:   true,
		Text: fmt.Sprintf("raw-callable functions (int arguments are written #N, others are text):\n  %s\n\nuse: sactl send <FUNC> [args...]", strings.Join(lines, "\n  ")),
	}
}

func signatureOf(name string) string {
	fields := rawSchemas[name]
	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		if field == rawInt {
			parts = append(parts, "int")
		} else {
			parts = append(parts, "text")
		}
	}
	return strings.Join(parts, ", ")
}

// commandSend submits one raw named-protocol request. It exists for the
// operations the client has no dedicated command for yet; the field list is
// still validated against the protocol schema, so it is an escape hatch and
// not arbitrary packet injection.
func (s *Server) commandSend(ctx context.Context, request Request) Response {
	if len(request.Args) == 0 {
		return failure(KindUsage, "usage: sactl send <FUNC> [args...]  (see `sactl functions`)")
	}
	name := request.Args[0]
	if hint, ok := typedFunctions[name]; ok {
		return failure(KindUsage, "send: %s has a dedicated command (%s)", name, hint)
	}
	return s.rawSend(ctx, name, request.Args[1:], "sent "+name)
}

// rawSend validates the arguments against the protocol schema and submits one
// raw action.
func (s *Server) rawSend(ctx context.Context, name string, args []string, describe string) Response {
	schema, known := rawSchemas[name]
	if !known {
		return failure(KindUsage, "send: %s is not an allowed client function; see `sactl functions`", name)
	}
	if len(args) != len(schema) {
		return failure(KindUsage, "send: %s takes %d argument(s) (%s) but %d were given",
			name, len(schema), signatureOf(name), len(args))
	}
	fields := make([]aigame.ActionField, 0, len(schema))
	for index, kind := range schema {
		value := args[index]
		if kind == rawInt {
			number, err := strconv.Atoi(strings.TrimPrefix(value, "#"))
			if err != nil {
				return failure(KindUsage, "send: %s argument %d must be an integer, got %q (write integers as #N)", name, index+1, value)
			}
			integer := int32(number)
			fields = append(fields, aigame.ActionField{Int: &integer})
			continue
		}
		encoded, err := encodeLegacy(value)
		if err != nil {
			return failure(KindUsage, "send: %s argument %d: %v", name, index+1, err)
		}
		fields = append(fields, aigame.ActionField{Text: encoded})
	}
	snapshot, err := s.snapshot(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	if err := requireWorld(snapshot); err != nil {
		return actionFailure(err)
	}
	if err := s.submit(ctx, snapshot.Revision, aigame.RawAction(name, fields...)); err != nil {
		return executeFailure(err)
	}
	after, err := s.settle(ctx, snapshot.Revision, 1500*time.Millisecond)
	if err != nil {
		return sessionFailure(err)
	}
	return Response{
		OK:   true,
		Text: fmt.Sprintf("%s\n%s", describe, s.renderObservation(after)),
		Data: replyJSON(after),
	}
}
