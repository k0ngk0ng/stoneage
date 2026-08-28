// Package bridge translates between the preserved client's function-name
// LSSPROTO dialect and the numeric StoneAge 2.5 Linux GMSV dialect.
package bridge

import (
	"bytes"
	"fmt"
	"sync"

	"github.com/k0ngk0ng/stoneage/internal/auth"
	"github.com/k0ngk0ng/stoneage/server/go/namedproto"
	"github.com/k0ngk0ng/stoneage/server/go/protocol"
)

type fieldKind byte

const (
	fieldInt fieldKind = iota
	fieldString
)

type functionSchema struct {
	name   string
	number int
	fields []fieldKind
}

func schema(name string, number int, fields ...fieldKind) functionSchema {
	return functionSchema{name: name, number: number, fields: fields}
}

// These tables are derived from both preserved generated protocol sources:
// server/legacy/source/code_sa_client/SYSTEM/LSSPROTO_CLI.CPP and
// server/legacy/source/2.5/gmsv/lssproto_serv.c. Keeping all known gameplay
// messages here avoids a login-only compatibility hack.
var clientToServerSchemas = []functionSchema{
	schema("W", 0, fieldInt, fieldInt, fieldString),
	schema("w", 1, fieldInt, fieldInt, fieldString),
	schema("EV", 3, fieldInt, fieldInt, fieldInt, fieldInt, fieldInt),
	schema("EN", 5, fieldInt, fieldInt),
	schema("DU", 6, fieldInt, fieldInt),
	schema("EO", 8, fieldInt),
	schema("BU", 9, fieldInt),
	schema("JB", 10, fieldInt, fieldInt),
	schema("LB", 11, fieldInt, fieldInt),
	schema("B", 14, fieldString),
	schema("SKD", 16, fieldInt, fieldInt),
	schema("ID", 17, fieldInt, fieldInt, fieldInt, fieldInt),
	schema("PI", 18, fieldInt, fieldInt, fieldInt),
	schema("DI", 19, fieldInt, fieldInt, fieldInt),
	schema("DG", 20, fieldInt, fieldInt, fieldInt),
	schema("DP", 21, fieldInt, fieldInt, fieldInt),
	schema("MI", 23, fieldInt, fieldInt),
	schema("MSG", 25, fieldInt, fieldString, fieldInt),
	schema("PMSG", 27, fieldInt, fieldInt, fieldInt, fieldString, fieldInt),
	schema("AB", 29),
	schema("DAB", 32, fieldInt),
	schema("AAB", 33, fieldInt, fieldInt),
	schema("L", 34, fieldInt),
	schema("TK", 35, fieldInt, fieldInt, fieldString, fieldInt, fieldInt),
	schema("M", 38, fieldInt, fieldInt, fieldInt, fieldInt, fieldInt),
	schema("C", 40, fieldInt),
	schema("S", 45, fieldString),
	schema("FS", 48, fieldInt),
	schema("HL", 50, fieldInt),
	schema("PR", 52, fieldInt, fieldInt, fieldInt),
	schema("KS", 54, fieldInt),
	schema("AC", 56, fieldInt, fieldInt, fieldInt),
	schema("MU", 57, fieldInt, fieldInt, fieldInt, fieldInt),
	schema("PS", 58, fieldInt, fieldInt, fieldInt, fieldString),
	schema("ST", 60, fieldInt),
	schema("DT", 61, fieldInt),
	schema("FT", 62, fieldString),
	schema("SKUP", 64, fieldInt),
	schema("KN", 65, fieldInt, fieldString),
	schema("WN", 67, fieldInt, fieldInt, fieldInt, fieldInt, fieldInt, fieldString),
	schema("SP", 70, fieldInt, fieldInt, fieldInt),
	schema("ClientLogin", 71, fieldString, fieldString),
	schema("CreateNewChar", 73,
		fieldInt, fieldString, fieldInt, fieldInt, fieldInt, fieldInt, fieldInt,
		fieldInt, fieldInt, fieldInt, fieldInt, fieldInt, fieldInt),
	schema("CharDelete", 75, fieldString),
	schema("CharLogin", 77, fieldString),
	schema("CharList", 79),
	schema("CharLogout", 81),
	schema("ProcGet", 83),
	schema("PlayerNumGet", 85),
	schema("Echo", 87, fieldString),
	schema("Shutdown", 89, fieldString, fieldInt),
	schema("TD", 91, fieldString),
	schema("FM", 94, fieldString),
	schema("PETST", 96, fieldInt, fieldInt),
	schema("MA", 98, fieldInt, fieldInt, fieldInt),
	schema("SPET", 114, fieldInt),
}

var serverToClientSchemas = []functionSchema{
	schema("XYD", 2, fieldInt, fieldInt, fieldInt),
	schema("EV", 4, fieldInt, fieldInt),
	schema("EN", 7, fieldInt, fieldInt),
	schema("RS", 12, fieldString),
	schema("RD", 13, fieldString),
	schema("B", 15, fieldString),
	schema("I", 22, fieldString),
	schema("SI", 24, fieldInt, fieldInt),
	schema("MSG", 26, fieldInt, fieldString, fieldInt),
	schema("PME", 28, fieldInt, fieldInt, fieldInt, fieldInt, fieldInt, fieldInt, fieldInt, fieldString),
	schema("AB", 30, fieldString),
	schema("ABI", 31, fieldInt, fieldString),
	schema("TK", 36, fieldInt, fieldString, fieldInt),
	schema("MC", 37, fieldInt, fieldInt, fieldInt, fieldInt, fieldInt, fieldInt, fieldInt, fieldInt, fieldString),
	schema("M", 39, fieldInt, fieldInt, fieldInt, fieldInt, fieldInt, fieldString),
	schema("C", 41, fieldString),
	schema("CA", 42, fieldString),
	schema("CD", 43, fieldString),
	schema("R", 44, fieldString),
	schema("S", 46, fieldString),
	schema("D", 47, fieldInt, fieldInt, fieldInt, fieldString),
	schema("FS", 49, fieldInt),
	schema("HL", 51, fieldInt),
	schema("PR", 53, fieldInt, fieldInt),
	schema("KS", 55, fieldInt, fieldInt),
	schema("PS", 59, fieldInt, fieldInt, fieldInt, fieldInt),
	schema("SKUP", 63, fieldInt),
	schema("WN", 66, fieldInt, fieldInt, fieldInt, fieldInt, fieldString),
	schema("EF", 68, fieldInt, fieldInt, fieldString),
	schema("SE", 69, fieldInt, fieldInt, fieldInt, fieldInt),
	schema("ClientLogin", 72, fieldString),
	schema("CreateNewChar", 74, fieldString, fieldString),
	schema("CharDelete", 76, fieldString, fieldString),
	schema("CharLogin", 78, fieldString, fieldString),
	schema("CharList", 80, fieldString, fieldString),
	schema("CharLogout", 82, fieldString, fieldString),
	schema("ProcGet", 84, fieldString),
	schema("PlayerNumGet", 86, fieldInt, fieldInt),
	schema("Echo", 88, fieldString),
	schema("NU", 90, fieldInt),
	/* The preserved 2.5 lssproto_TD_send signature still accepts an `index`
	   argument, but its generated body deliberately serializes only `message`.
	   Treating that first encrypted string as an integer made every successful
	   trade handshake fail translation after GMSV had already entered
	   CHAR_TRADE_TRADING. */
	schema("TD", 92, fieldString),
	schema("FM", 93, fieldString),
	schema("WO", 95, fieldInt),
	schema("IC", 100, fieldInt, fieldInt),
	schema("NC", 101, fieldInt),
	schema("PETST", 107, fieldInt, fieldInt),
	schema("SPET", 115, fieldInt, fieldInt),
}

var (
	clientByName   = indexByName(clientToServerSchemas)
	serverByNumber = indexByNumber(serverToClientSchemas)
)

func indexByName(schemas []functionSchema) map[string]functionSchema {
	result := make(map[string]functionSchema, len(schemas))
	for _, value := range schemas {
		if _, exists := result[value.name]; exists {
			panic("duplicate named protocol function " + value.name)
		}
		result[value.name] = value
	}
	return result
}

func indexByNumber(schemas []functionSchema) map[int]functionSchema {
	result := make(map[int]functionSchema, len(schemas))
	for _, value := range schemas {
		if _, exists := result[value.number]; exists {
			panic(fmt.Sprintf("duplicate numeric protocol function %d", value.number))
		}
		result[value.number] = value
	}
	return result
}

// Translator contains only per-connection state. It is safe for one reader in
// each direction, matching the gateway's two network pumps.
type Translator struct {
	mu           sync.RWMutex
	account      string
	nextResponse uint32
	rotation     int
}

func NewTranslator() *Translator {
	return &Translator{nextResponse: 1}
}

func (translator *Translator) Account() string {
	translator.mu.RLock()
	defer translator.mu.RUnlock()
	return translator.account
}

// ClientToServer converts one newline-delimited packet from the old client
// into one numeric GMSV packet.
func (translator *Translator) ClientToServer(packet []byte) ([]byte, string, error) {
	raw, err := namedproto.DecodePacket(packet)
	if err != nil {
		return nil, "", fmt.Errorf("decode named packet: %w", err)
	}
	message, err := namedproto.ParseMessage(raw)
	if err != nil {
		return nil, "", err
	}
	definition, exists := clientByName[message.Function]
	if !exists {
		return nil, message.Function, fmt.Errorf("unsupported client function %q", message.Function)
	}
	// The generated 1999 parser records a token pointer after every space,
	// including the message's final delimiter. Some client builds append two
	// final spaces and therefore expose one harmless empty token beyond the
	// declared function signature. Match that parser's permissive behavior,
	// but never ignore a non-empty extra argument.
	if len(message.Fields) > len(definition.fields) {
		extraFields := message.Fields[len(definition.fields):]
		allEmpty := true
		for _, field := range extraFields {
			if field != "" {
				allEmpty = false
				break
			}
		}
		if allEmpty {
			message.Fields = message.Fields[:len(definition.fields)]
		}
	}
	if len(message.Fields) != len(definition.fields) {
		return nil, message.Function, fmt.Errorf(
			"client function %s has %d fields %q, want %d",
			message.Function, len(message.Fields), message.Fields, len(definition.fields),
		)
	}

	key := translator.sessionKey(definition.number == protocol.FunctionClientLoginRequest)
	fields := protocol.NewFieldEncoder(key)
	var loginAccount string
	for index, kind := range definition.fields {
		switch kind {
		case fieldString:
			value, err := namedproto.DecodeString(message.Fields[index])
			if err != nil {
				return nil, message.Function, fmt.Errorf("decode string field %d: %w", index, err)
			}
			if definition.number == protocol.FunctionClientLoginRequest && index == 0 {
				value = []byte(auth.CanonicalGameUsername(string(value)))
			}
			fields.String(value)
			if definition.number == protocol.FunctionClientLoginRequest && index == 0 {
				loginAccount = string(value)
			}
		case fieldInt:
			value, err := namedproto.DecodeInt(message.Fields[index])
			if err != nil {
				return nil, message.Function, fmt.Errorf("decode integer field %d: %w", index, err)
			}
			fields.Int(value)
		default:
			panic("unknown bridge field kind")
		}
	}
	numericRaw, err := fields.Finish(definition.number)
	if err != nil {
		return nil, message.Function, err
	}

	translator.mu.Lock()
	rotation := translator.rotation
	translator.rotation = (translator.rotation + 1) % 99
	if definition.number == protocol.FunctionClientLoginRequest {
		if len(loginAccount) == 0 || len(loginAccount) > 15 {
			translator.mu.Unlock()
			return nil, message.Function, fmt.Errorf("account must contain 1..15 bytes")
		}
		translator.account = loginAccount
	}
	translator.mu.Unlock()

	translated, err := protocol.EncodeMessage(numericRaw, rotation)
	if err != nil {
		return nil, message.Function, err
	}
	return translated, message.Function, nil
}

// ServerToClient converts one newline-delimited GMSV packet into the old
// client's function-name protocol.
func (translator *Translator) ServerToClient(packet []byte) ([]byte, string, error) {
	raw, _, err := protocol.DecodeMessage(packet)
	if err != nil {
		return nil, "", fmt.Errorf("decode numeric packet: %w", err)
	}
	message, err := protocol.ParseMessage(raw)
	if err != nil {
		return nil, "", err
	}
	definition, exists := serverByNumber[message.Function]
	if !exists {
		return nil, fmt.Sprintf("#%d", message.Function), fmt.Errorf("unsupported server function %d", message.Function)
	}

	key := translator.sessionKey(false)
	fields := protocol.NewFieldDecoder(message, key)
	namedFields := make([]string, 0, len(definition.fields))
	for index, kind := range definition.fields {
		switch kind {
		case fieldString:
			value, err := fields.String()
			if err != nil {
				return nil, definition.name, fmt.Errorf("decode string field %d: %w", index, err)
			}
			if definition.name == "B" && index == 0 {
				value, err = legacyBattleCommand(value)
				if err != nil {
					return nil, definition.name, err
				}
			}
			namedFields = append(namedFields, namedproto.EncodeString(value))
		case fieldInt:
			value, err := fields.Int()
			if err != nil {
				return nil, definition.name, fmt.Errorf("decode integer field %d: %w", index, err)
			}
			namedFields = append(namedFields, namedproto.EncodeInt(value))
		default:
			panic("unknown bridge field kind")
		}
	}
	if err := fields.VerifyChecksum(); err != nil {
		return nil, definition.name, err
	}

	translator.mu.Lock()
	messageID := translator.nextResponse
	translator.nextResponse++
	translator.mu.Unlock()
	namedRaw, err := namedproto.RawMessage(messageID, definition.name, namedFields)
	if err != nil {
		return nil, definition.name, err
	}
	translated, err := namedproto.EncodePacket(namedRaw)
	if err != nil {
		return nil, definition.name, err
	}
	return translated, definition.name, nil
}

// legacyBattleCommand removes the five riding-pet fields appended to each
// character in the Linux 2.5 BC command. The preserved Windows client predates
// that extension and consumes exactly eight fields per character; forwarding
// all thirteen makes it interpret rideflg as the next character's battle ID.
// Other battle commands are byte-for-byte transparent.
func legacyBattleCommand(command []byte) ([]byte, error) {
	if !bytes.HasPrefix(command, []byte("BC|")) {
		return command, nil
	}

	fields := bytes.Split(command, []byte{'|'})
	if len(fields) < 3 || len(fields[len(fields)-1]) != 0 {
		return nil, fmt.Errorf("malformed BC command: missing trailing separator")
	}
	characterFields := fields[2 : len(fields)-1]
	const (
		serverFieldsPerCharacter = 13
		clientFieldsPerCharacter = 8
	)
	if len(characterFields)%serverFieldsPerCharacter != 0 {
		return nil, fmt.Errorf(
			"malformed BC command: %d character fields is not divisible by %d",
			len(characterFields), serverFieldsPerCharacter,
		)
	}

	result := make([]byte, 0, len(command))
	result = append(result, "BC|"...)
	result = append(result, fields[1]...)
	result = append(result, '|')
	for start := 0; start < len(characterFields); start += serverFieldsPerCharacter {
		for offset := 0; offset < clientFieldsPerCharacter; offset++ {
			result = append(result, characterFields[start+offset]...)
			result = append(result, '|')
		}
	}
	return result, nil
}

func (translator *Translator) sessionKey(login bool) string {
	if login {
		return protocol.DefaultKey
	}
	translator.mu.RLock()
	defer translator.mu.RUnlock()
	if translator.account == "" {
		return protocol.DefaultKey
	}
	return translator.account + protocol.RunningKey
}
