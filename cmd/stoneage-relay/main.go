// stoneage-relay is a byte-transparent TCP relay used to inspect exchanges
// between preserved clients and the local GMSV without modifying either end.
package main

import (
	"bytes"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

func main() {
	listenAddress := flag.String("listen", "127.0.0.1:9066", "relay listen address")
	upstreamAddress := flag.String("upstream", "127.0.0.1:9065", "GMSV address")
	adaptWGSGreeting := flag.Bool(
		"adapt-wgs-greeting",
		false,
		"replace the GMSV L\\0 greeting with the A\\0 greeting expected by the preserved 2.5 WGS state machine",
	)
	flag.Parse()

	listener, err := net.Listen("tcp", *listenAddress)
	if err != nil {
		fatalf("listen on %s: %v", *listenAddress, err)
	}
	defer listener.Close()
	fmt.Printf("relay listening on %s -> %s\n", *listenAddress, *upstreamAddress)

	client, err := listener.Accept()
	if err != nil {
		fatalf("accept client: %v", err)
	}
	defer client.Close()

	upstream, err := net.DialTimeout("tcp", *upstreamAddress, 5*time.Second)
	if err != nil {
		fatalf("connect upstream %s: %v", *upstreamAddress, err)
	}
	defer upstream.Close()
	fmt.Printf("client connected from %s\n", client.RemoteAddr())

	var workers sync.WaitGroup
	workers.Add(2)
	go copyAndTrace(&workers, "client->server", upstream, client, false)
	go copyAndTrace(&workers, "server->client", client, upstream, *adaptWGSGreeting)
	workers.Wait()
}

func copyAndTrace(workers *sync.WaitGroup, direction string, destination net.Conn, source net.Conn, adaptWGSGreeting bool) {
	defer workers.Done()
	buffer := make([]byte, 128*1024)
	firstPayload := true
	for {
		count, err := source.Read(buffer)
		if count > 0 {
			payload := append([]byte(nil), buffer[:count]...)
			if firstPayload && adaptWGSGreeting && bytes.HasPrefix(payload, []byte{'L', 0}) {
				payload[0] = 'A'
				fmt.Printf("%s adapted GMSV greeting L\\0 -> WGS greeting A\\0\n", direction)
			}
			firstPayload = false
			fmt.Printf("%s bytes=%d hex=%s\n", direction, count, hex.EncodeToString(payload))
			if _, writeErr := destination.Write(payload); writeErr != nil {
				fmt.Printf("%s write error: %v\n", direction, writeErr)
				return
			}
		}
		if err != nil {
			if err != io.EOF {
				fmt.Printf("%s read error: %v\n", direction, err)
			}
			if tcp, ok := destination.(*net.TCPConn); ok {
				_ = tcp.CloseWrite()
			}
			return
		}
	}
}

func fatalf(format string, arguments ...any) {
	fmt.Fprintf(os.Stderr, "stoneage-relay: "+format+"\n", arguments...)
	os.Exit(1)
}
