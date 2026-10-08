// Command mockllm runs a fake AI provider (OpenAI, Anthropic and Gemini
// APIs) for end-to-end tests without API keys. It is not part of releases.
package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/officialmelon/betterdecompiler/internal/mockllm"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:5099", "listen address")
	key := flag.String("key", "test-key", "API key clients must send")
	flag.Parse()
	log.Printf("mock AI provider listening on http://%s (key %q)", *addr, *key)
	log.Fatal(http.ListenAndServe(*addr, mockllm.New(*key)))
}
