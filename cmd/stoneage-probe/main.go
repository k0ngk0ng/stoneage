// stoneage-probe verifies StoneAge 2.5 account and character exchanges against
// a live legacy GMSV. Character creation is opt-in because it persists data.
package main

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/k0ngk0ng/stoneage/server/go/protocol"
)

type options struct {
	address   string
	account   string
	password  string
	rotation  int
	timeout   time.Duration
	trace     bool
	create    bool
	enter     bool
	character string
}

func main() {
	var opts options
	flag.StringVar(&opts.address, "address", "127.0.0.1:9065", "GMSV TCP address")
	flag.StringVar(&opts.account, "account", "probe", "local protocol-test account (maximum 15 bytes)")
	flag.StringVar(&opts.password, "password", "local", "local protocol-test password (maximum 15 bytes)")
	flag.IntVar(&opts.rotation, "rotation", 0, "deterministic outer-packet rotation (0..98)")
	flag.DurationVar(&opts.timeout, "timeout", 5*time.Second, "connection and exchange timeout")
	flag.BoolVar(&opts.trace, "trace", false, "print packet bytes and decoded envelopes (includes account, never password text)")
	flag.BoolVar(&opts.create, "create", false, "create the named test character when the account has no characters")
	flag.BoolVar(&opts.enter, "enter", false, "log in the named character and verify initial map data")
	flag.StringVar(&opts.character, "character", "ProbeHero", "legacy character name used by -create or -enter")
	flag.Parse()

	if err := run(opts); err != nil {
		fmt.Fprintf(os.Stderr, "stoneage-probe: %v\n", err)
		os.Exit(1)
	}
}

func run(opts options) error {
	if len(opts.account) == 0 || len(opts.account) > 15 {
		return fmt.Errorf("account must contain 1..15 bytes")
	}
	if len(opts.password) == 0 || len(opts.password) > 15 {
		return fmt.Errorf("password must contain 1..15 bytes")
	}
	if len(opts.character) == 0 || len(opts.character) >= 32 {
		return fmt.Errorf("character must contain 1..31 bytes")
	}

	connection, err := net.DialTimeout("tcp", opts.address, opts.timeout)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", opts.address, err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(opts.timeout)); err != nil {
		return err
	}

	greeting := make([]byte, 2)
	if _, err := io.ReadFull(connection, greeting); err != nil {
		return fmt.Errorf("read server greeting: %w", err)
	}
	if greeting[0] != protocol.Version || greeting[1] != 0 {
		return fmt.Errorf("unexpected server greeting %x; want %02x00", greeting, protocol.Version)
	}
	fmt.Printf("greeting: StoneAge 2.5 version %c\n", greeting[0])

	loginPacket, err := protocol.ClientLoginRequest([]byte(opts.account), []byte(opts.password), opts.rotation)
	if err != nil {
		return fmt.Errorf("build ClientLogin: %w", err)
	}
	if opts.trace {
		fmt.Printf("ClientLogin request: %s\n", hex.EncodeToString(loginPacket))
	}
	if err := writeAll(connection, loginPacket); err != nil {
		return fmt.Errorf("send ClientLogin: %w", err)
	}

	reader := bufio.NewReader(connection)
	line, err := readLine(reader)
	if err != nil {
		return fmt.Errorf("read ClientLogin response: %w", err)
	}
	loginMessage, loginRotation, err := protocol.DecodeResponse(line, protocol.FunctionClientLoginResponse)
	if err != nil {
		return fmt.Errorf("decode ClientLogin response: %w", err)
	}
	if opts.trace {
		traceMessage("ClientLogin response", loginMessage, loginRotation)
	}
	loginFields := protocol.NewFieldDecoder(loginMessage, opts.account+protocol.RunningKey)
	loginResult, err := loginFields.String()
	if err != nil {
		return fmt.Errorf("decode ClientLogin result: %w", err)
	}
	if err := loginFields.VerifyChecksum(); err != nil {
		return fmt.Errorf("verify ClientLogin response: %w", err)
	}
	if !bytes.Equal(loginResult, []byte("ok")) {
		return fmt.Errorf("account login was rejected: %q", loginResult)
	}
	fmt.Println("account login: ok (function 71/72)")

	charListPacket, err := protocol.CharListRequest([]byte(opts.account), opts.rotation)
	if err != nil {
		return fmt.Errorf("build CharList: %w", err)
	}
	if opts.trace {
		fmt.Printf("CharList request: %s\n", hex.EncodeToString(charListPacket))
	}
	if err := writeAll(connection, charListPacket); err != nil {
		return fmt.Errorf("send CharList: %w", err)
	}
	line, err = readLine(reader)
	if err != nil {
		return fmt.Errorf("read CharList response: %w", err)
	}
	charListMessage, charListRotation, err := protocol.DecodeResponse(line, protocol.FunctionCharListResponse)
	if err != nil {
		return fmt.Errorf("decode CharList response: %w", err)
	}
	if opts.trace {
		traceMessage("CharList response", charListMessage, charListRotation)
	}
	charListFields := protocol.NewFieldDecoder(charListMessage, opts.account+protocol.RunningKey)
	charListResult, err := charListFields.String()
	if err != nil {
		return fmt.Errorf("decode CharList result: %w", err)
	}
	charListData, err := charListFields.String()
	if err != nil {
		return fmt.Errorf("decode CharList data: %w", err)
	}
	if err := charListFields.VerifyChecksum(); err != nil {
		return fmt.Errorf("verify CharList response: %w", err)
	}
	if !bytes.Equal(charListResult, []byte("successful")) {
		return fmt.Errorf("character list failed: %q (%q)", charListResult, charListData)
	}
	fmt.Printf("character list: successful, %d legacy data bytes (function 79/80)\n", len(charListData))

	if opts.create {
		if len(charListData) != 0 {
			if !bytes.Contains(charListData, []byte(opts.character)) {
				return fmt.Errorf("account already has character data but not %q; refusing to create into an occupied test account", opts.character)
			}
			fmt.Printf("character create: %q already exists\n", opts.character)
		} else {
			createPacket, err := protocol.CreateCharRequest([]byte(opts.account), protocol.CreateCharOptions{
				DataPlace: 0,
				Name:      []byte(opts.character),
				Image:     100000,
				FaceImage: 30000,
				Vital:     5,
				Strength:  5,
				Toughness: 5,
				Dexterity: 5,
				Earth:     10,
				Hometown:  0,
			}, opts.rotation)
			if err != nil {
				return fmt.Errorf("build CreateNewChar: %w", err)
			}
			if opts.trace {
				fmt.Printf("CreateNewChar request: %s\n", hex.EncodeToString(createPacket))
			}
			if err := writeAll(connection, createPacket); err != nil {
				return fmt.Errorf("send CreateNewChar: %w", err)
			}
			line, err = readLine(reader)
			if err != nil {
				return fmt.Errorf("read CreateNewChar response: %w", err)
			}
			createMessage, createRotation, err := protocol.DecodeResponse(line, protocol.FunctionCreateCharResponse)
			if err != nil {
				return fmt.Errorf("decode CreateNewChar response: %w", err)
			}
			if opts.trace {
				traceMessage("CreateNewChar response", createMessage, createRotation)
			}
			createFields := protocol.NewFieldDecoder(createMessage, opts.account+protocol.RunningKey)
			createResult, err := createFields.String()
			if err != nil {
				return fmt.Errorf("decode CreateNewChar result: %w", err)
			}
			createData, err := createFields.String()
			if err != nil {
				return fmt.Errorf("decode CreateNewChar data: %w", err)
			}
			if err := createFields.VerifyChecksum(); err != nil {
				return fmt.Errorf("verify CreateNewChar response: %w", err)
			}
			if !bytes.Equal(createResult, []byte("successful")) {
				return fmt.Errorf("character creation failed: %q (%q)", createResult, createData)
			}
			fmt.Printf("character create: successful, %q (function 73/74)\n", opts.character)
		}
	}

	if opts.enter {
		loginPacket, err := protocol.CharLoginRequest([]byte(opts.account), []byte(opts.character), opts.rotation)
		if err != nil {
			return fmt.Errorf("build CharLogin: %w", err)
		}
		if opts.trace {
			fmt.Printf("CharLogin request: %s\n", hex.EncodeToString(loginPacket))
		}
		if err := writeAll(connection, loginPacket); err != nil {
			return fmt.Errorf("send CharLogin: %w", err)
		}
		if err := verifyCharacterEntry(reader, opts); err != nil {
			return err
		}
	}
	fmt.Println("compatibility probe: PASS")
	return nil
}

func verifyCharacterEntry(reader *bufio.Reader, opts options) error {
	const maxInitialPackets = 128
	loginSeen := false
	mapSeen := false
	key := opts.account + protocol.RunningKey
	for packetIndex := 0; packetIndex < maxInitialPackets && !(loginSeen && mapSeen); packetIndex++ {
		line, err := readLine(reader)
		if err != nil {
			return fmt.Errorf("read character-entry packet %d: %w", packetIndex+1, err)
		}
		raw, rotation, err := protocol.DecodeMessage(line)
		if err != nil {
			return fmt.Errorf("decode character-entry packet %d: %w", packetIndex+1, err)
		}
		message, err := protocol.ParseMessage(raw)
		if err != nil {
			return fmt.Errorf("parse character-entry packet %d: %w", packetIndex+1, err)
		}
		if opts.trace {
			traceMessage(fmt.Sprintf("entry packet %d", packetIndex+1), message, rotation)
		}
		switch message.Function {
		case protocol.FunctionCharLoginResponse:
			fields := protocol.NewFieldDecoder(message, key)
			result, err := fields.String()
			if err != nil {
				return fmt.Errorf("decode CharLogin result: %w", err)
			}
			data, err := fields.String()
			if err != nil {
				return fmt.Errorf("decode CharLogin data: %w", err)
			}
			if err := fields.VerifyChecksum(); err != nil {
				return fmt.Errorf("verify CharLogin response: %w", err)
			}
			if !bytes.Equal(result, []byte("successful")) {
				return fmt.Errorf("character login failed: %q (%q)", result, data)
			}
			loginSeen = true
			fmt.Printf("character login: successful, %q (function 77/78)\n", opts.character)
		case 37: // MC: initial map rectangle checksums and data.
			fields := protocol.NewFieldDecoder(message, key)
			values := make([]int32, 8)
			for index := range values {
				values[index], err = fields.Int()
				if err != nil {
					return fmt.Errorf("decode initial map integer %d: %w", index, err)
				}
			}
			data, err := fields.String()
			if err != nil {
				return fmt.Errorf("decode initial map data: %w", err)
			}
			if err := fields.VerifyChecksum(); err != nil {
				return fmt.Errorf("verify initial map packet: %w", err)
			}
			if values[0] <= 0 || values[3] <= values[1] || values[4] <= values[2] || len(data) == 0 {
				return fmt.Errorf("invalid initial map packet: values=%v data-bytes=%d", values, len(data))
			}
			mapSeen = true
			fmt.Printf("map initialization: floor=%d rect=(%d,%d)-(%d,%d), %d data bytes (function 37)\n",
				values[0], values[1], values[2], values[3], values[4], len(data))
		}
	}
	if !loginSeen || !mapSeen {
		return fmt.Errorf("character entry incomplete after %d packets: login=%t map=%t", maxInitialPackets, loginSeen, mapSeen)
	}
	return nil
}

func readLine(reader *bufio.Reader) ([]byte, error) {
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	if len(line) > 128*1024 {
		return nil, fmt.Errorf("packet exceeds 128 KiB")
	}
	return line, nil
}

func writeAll(writer io.Writer, value []byte) error {
	for len(value) > 0 {
		written, err := writer.Write(value)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrUnexpectedEOF
		}
		value = value[written:]
	}
	return nil
}

func traceMessage(label string, message protocol.Message, rotation int) {
	fmt.Printf("%s: function=%d rotation=%d encoded-fields=%q\n", label, message.Function, rotation, message.Fields)
}
