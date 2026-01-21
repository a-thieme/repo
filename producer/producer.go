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
	notify, _ := enc.NameFromStr("/ndn/repo/notify")

	command := tlv.Command{
		Type:   "testtype",
		Target: target,
	}

	client.ExpressCommand(notify, target, command.Encode(),
		func(w enc.Wire, e error) {
			if e != nil {
				fmt.Println(e.Error())
				return
			}
			sr, err := tlv.ParseStatusResponse(enc.NewWireView(w), false)
			if err != nil {
				fmt.Println(err.Error())
				return
			}
			fmt.Println(sr.Target)
			fmt.Println(sr.Status)

		})
}
