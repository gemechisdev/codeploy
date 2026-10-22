// Sample out-of-process extension (T57): verifies HMAC and posts an action with its token.
// Not shipped with the product binary; run with: go run ./test/extensions
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
)

func main() {
	secret := os.Getenv("CODEPLO_EXTENSION_SECRET")
	if secret == "" {
		log.Fatal("set CODEPLO_EXTENSION_SECRET")
	}
	http.HandleFunc("/hook", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sig := r.Header.Get("X-Codeplo-Signature-256")
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		if !hmac.Equal([]byte(sig), []byte(want)) {
			http.Error(w, "bad signature", 401)
			return
		}
		fmt.Printf("event=%s delivery=%s body=%s\n", r.Header.Get("X-Codeplo-Event"), r.Header.Get("X-Codeplo-Delivery"), strings.TrimSpace(string(body)))
		w.WriteHeader(204)
	})
	addr := ":8787"
	if v := os.Getenv("LISTEN"); v != "" {
		addr = v
	}
	log.Println("sample extension listening on", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
