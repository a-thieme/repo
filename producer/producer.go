package main

import (
	"fmt"
	"github.com/a-thieme/repo/tlv"
	"time"

	enc "github.com/named-data/ndnd/std/encoding"
	"github.com/named-data/ndnd/std/engine"
	"github.com/named-data/ndnd/std/log"
	"github.com/named-data/ndnd/std/ndn"
	spec "github.com/named-data/ndnd/std/ndn/spec_2022"
	"github.com/named-data/ndnd/std/object"
	local_storage "github.com/named-data/ndnd/std/object/storage"
	sec "github.com/named-data/ndnd/std/security"
	"github.com/named-data/ndnd/std/security/keychain"
	"github.com/named-data/ndnd/std/security/signer"
)

func ExpressCommand(c ndn.Client, dest enc.Name, name enc.Name, cmd enc.Wire, callback func(enc.Wire, error)) {
	signer := c.SuggestSigner(name)
	if signer == nil {
		callback(nil, fmt.Errorf("no signer found for command: %s", name))
		return
	}

	dataCfg := ndn.DataConfig{}
	data, err := spec.Spec{}.MakeData(name, &dataCfg, cmd, signer)
	if err != nil {
		callback(nil, fmt.Errorf("failed to make command data: %w", err))
		return
	}

	c.ExpressR(ndn.ExpressRArgs{
		Name: dest,
		Config: &ndn.InterestConfig{
			CanBePrefix: false,
			MustBeFresh: true,
		},
		AppParam: data.Wire,
		Retries:  0,
		Callback: func(args ndn.ExpressCallbackArgs) {
			if args.Result != ndn.InterestResultData {
				callback(nil, fmt.Errorf("command failed: %s", args.Result))
				return
			}
			c.Validate(args.Data, data.Wire, func(valid bool, err error) {
				// if !valid {
				// 	callback(nil, fmt.Errorf("command data validation failed: %w", err))
				// }
				callback(args.Data.Content(), nil)
			})
		},
	})
}

// BasicSchema allows all data and suggests the first matching key in the keychain.
type BasicSchema struct{}

func (s *BasicSchema) Check(pkt enc.Name, cert enc.Name) bool {
	fmt.Println("checking data", pkt.Clone().String())
	return true // Trust everything (matching NullSchema behavior)
}

func (s *BasicSchema) Suggest(name enc.Name, kc ndn.KeyChain) ndn.Signer {
	myname, _ := enc.NameFromStr("/ndn/repo.teame.dev/producer")
	for _, id := range kc.Identities() {
		if id.Name().IsPrefix(myname) {
			if len(id.Keys()) > 0 {
				return id.Keys()[0].Signer()
			}
		}
	}

	return signer.NewSha256Signer()
}
func main() {
	log.Default().SetLevel(log.LevelTrace)
	fmt.Println("starting")
	engine := engine.NewBasicEngine(engine.NewDefaultFace())
	engine.Start()
	store := local_storage.NewMemoryStore()
	target, _ := enc.NameFromStr("/ndn/repo.teame.dev/producer/mytarget/")
	notify, _ := enc.NameFromStr("/ndn/drepo/notify")
	prefix, _ := enc.NameFromStr("/ndn/repo.teame.dev/producer")
	target = target.Append(enc.NewTimestampComponent(uint64(time.Now().Unix())))

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
	log.Debug(nil, "announce", "prefix", prefix)
	client.AnnouncePrefix(ndn.Announcement{
		Name:   prefix,
		Expose: true,
	})
	command := tlv.Command{
		Type:   "INSERT",
		Target: target,
	}

	done := make(chan struct{})

	fmt.Println("Sending command...")
	ExpressCommand(client, notify, target, command.Encode(),
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
