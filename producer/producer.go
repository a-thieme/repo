package main

import (
	"fmt"
	"github.com/a-thieme/repo/tlv"

	enc "github.com/named-data/ndnd/std/encoding"
	"github.com/named-data/ndnd/std/engine"
	"github.com/named-data/ndnd/std/log"
	"github.com/named-data/ndnd/std/object"
	local_storage "github.com/named-data/ndnd/std/object/storage"
)

func main() {
	log.Default().SetLevel(log.LevelTrace)
	fmt.Println("starting")
	engine := engine.NewBasicEngine(engine.NewDefaultFace())
	engine.Start()
	store := local_storage.NewMemoryStore()
	client := object.NewClient(engine, store, nil)
	target, _ := enc.NameFromStr("mytarget")
	notify, _ := enc.NameFromStr("/ndn/drepo/notify")

	command := tlv.Command{
		Type:   "testtype",
		Target: target,
	}

	done := make(chan struct{})

	fmt.Println("Sending command...")
	client.ExpressCommand(notify, target, command.Encode(),
		func(w enc.Wire, e error) {
			// Ensure we signal that we are done processing, regardless of success/fail
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

	// Block here until the callback closes the 'done' channel
	<-done
	fmt.Println("finished")
}
