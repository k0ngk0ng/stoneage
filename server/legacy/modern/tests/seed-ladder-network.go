//go:build ignore

// Fixture-only account provisioning for test-ladder-network.py. Build this
// file explicitly; it is not part of any shipped command or runtime.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/k0ngk0ng/stoneage/internal/auth"
)

func main() {
	if len(os.Args) != 2 {
		panic("usage: seed-ladder-network <new fixture directory>")
	}
	root := os.Args[1]
	store, err := auth.Open(filepath.Join(root, "auth.db"))
	must(err)
	defer store.Close()
	must(store.Migrate(context.Background()))
	for i := 0; i < 10; i++ {
		user := fmt.Sprintf("ladderqa%02d", i)
		secret := make([]byte, 6)
		_, err = rand.Read(secret)
		must(err)
		password := []byte(hex.EncodeToString(secret))
		_, err = store.CreateAccount(context.Background(), user, password)
		must(err)
		must(os.WriteFile(filepath.Join(root, user+".password"), password, 0600))
	}
	fmt.Println("Created isolated ladder fixture accounts")
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
