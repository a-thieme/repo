package main

import (
	"fmt"
	"github.com/a-thieme/repo/tlv"

	enc "github.com/named-data/ndnd/std/encoding"
	"github.com/named-data/ndnd/std/engine"
	"github.com/named-data/ndnd/std/log"
	"github.com/named-data/ndnd/std/ndn"
	"github.com/named-data/ndnd/std/object"
	local_storage "github.com/named-data/ndnd/std/object/storage"
	sec "github.com/named-data/ndnd/std/security"
	"github.com/named-data/ndnd/std/security/keychain"
	"github.com/named-data/ndnd/std/security/signer"
)

// BasicSchema allows all data and suggests the first matching key in the keychain.
type BasicSchema struct{}

func (s *BasicSchema) Check(pkt enc.Name, cert enc.Name) bool {
	return true // Trust everything (matching NullSchema behavior)
}

func (s *BasicSchema) Suggest(name enc.Name, kc ndn.KeyChain) ndn.Signer {
	// The specific identity we want
	target, _ := enc.NameFromStr("/ndn/repo.teame.dev")

	// 1. Try to find the specific key
	for _, id := range kc.Identities() {
		// Log found identities to debug
		log.Info(nil, "Keychain has identity", "name", id.Name())

		// Check if identity matches or is a parent of the target
		if id.Name().IsPrefix(target) || target.IsPrefix(id.Name()) {
			if len(id.Keys()) > 0 {
				return id.Keys()[0].Signer()
			}
		}
	}

	// 2. CRITICAL FALLBACK: If specific key not found, use ANY valid key
	// This prevents "key locator is nil" if you have a different key loaded
	if len(kc.Identities()) > 0 {
		firstId := kc.Identities()[0]
		if len(firstId.Keys()) > 0 {
			log.Warn(nil, "Specific key not found; falling back to first available", "using", firstId.Name())
			return firstId.Keys()[0].Signer()
		}
	}

	log.Error(nil, "No identities found in keychain; returning Sha256 (will fail validation)")
	return signer.NewSha256Signer()
}
func main() {
	log.Default().SetLevel(log.LevelTrace)
	fmt.Println("starting")
	engine := engine.NewBasicEngine(engine.NewDefaultFace())
	engine.Start()
	store := local_storage.NewMemoryStore()
	target, _ := enc.NameFromStr("mytarget")
	notify, _ := enc.NameFromStr("/ndn/drepo/notify")
	prefix, _ := enc.NameFromStr("/ndn/repo.teame.dev")

	kc, err := keychain.NewKeyChain("dir:///home/adam/.ndn/keys", store)
	if err != nil {
		return
	}
	schema := &BasicSchema{}
	testbedRootName, _ := enc.NameFromStr("/ndn/KEY/%27%C4%B2%2A%9F%7B%81%27/ndn/v=1651246789556")
	trust, err := sec.NewTrustConfig(kc, schema, []enc.Name{testbedRootName})
	if err != nil {
		return
	}
	trust.UseDataNameFwHint = true

	// new client
	client := object.NewClient(engine, store, trust)
	client.SuggestSigner(prefix)
	log.Debug(nil, "announce", "prefix", prefix)
	client.AnnouncePrefix(ndn.Announcement{
		Name:   prefix,
		Expose: true,
	})
	command := tlv.Command{
		Type:   "testtype",
		Target: target,
	}

	done := make(chan struct{})

	fmt.Println("Sending command...")
	client.ExpressCommand(notify, target, command.Encode(),
		func(w enc.Wire, e error) {
			defer close(done)

			if e != nil {
				fmt.Println("Error:", e.Error())
				return
			}
			sr, err := tlv.ParseStatusResponse(enc.NewWireView(w), false)
			if err != nil {
				fmt.Println("Parse Error:", err.Error())
				return
			}
			fmt.Println("Target:", sr.Target)
			fmt.Println("Status:", sr.Status)
		})

	<-done // blocking
	fmt.Println("finished")
}
