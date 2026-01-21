package main

import (
	"github.com/a-thieme/repo/tlv"

	enc "github.com/named-data/ndnd/std/encoding"
	"github.com/named-data/ndnd/std/engine"
	"github.com/named-data/ndnd/std/log"
	"github.com/named-data/ndnd/std/ndn"
	"github.com/named-data/ndnd/std/object"
	local_storage "github.com/named-data/ndnd/std/object/storage"
)

const NOTIFY = "notify"

type Repo struct {
	groupPrefix  enc.Name
	notifyPrefix *enc.Name

	engine ndn.Engine
	store  ndn.Store
	client ndn.Client

	commands []*tlv.Command
}

func NewRepo(groupPrefix string) *Repo {
	gp, _ := enc.NameFromStr(groupPrefix)
	nf := gp.Append(enc.NewGenericComponent(NOTIFY))
	return &Repo{
		groupPrefix:  gp,
		notifyPrefix: &nf,
	}
}

func (r *Repo) String() string {
	return "repo"
}

func (r *Repo) Start() (err error) {
	log.Info(r, "starting")

	log.Debug(r, "make engine")
	r.engine = engine.NewBasicEngine(engine.NewDefaultFace())
	if err = r.engine.Start(); err != nil {
		return err
	}

	// FIXME: use badger store in the deployed version for persistent storage
	// only using memory for testing
	log.Debug(r, "new store")
	local_storage.NewMemoryStore()

	// new client
	r.client = object.NewClient(r.engine, r.store, nil)

	// client notification
	log.Debug(r, "announce", "prefix", &r.notifyPrefix)
	r.client.AnnouncePrefix(ndn.Announcement{
		Name:   r.notifyPrefix.Clone(),
		Expose: true,
	})
	log.Debug(r, "attach command handler")
	r.client.AttachCommandHandler(*r.notifyPrefix, r.onCommand)
	// group svs

	// TODO: get command from client

	// TODO: publish command to group

	return nil
}

// func (r *Repo) onCommand(name enc.Name, content enc.Wire, reply func(enc.Wire) error ) error {
func (r *Repo) onCommand(name enc.Name, content enc.Wire, reply func(wire enc.Wire) error) {
	log.Info(r, "got command")
	cmd, err := tlv.ParseCommand(enc.NewWireView(content), false)
	if err != nil {
		return
	}
	log.Debug(r, "parsed command", "target", cmd.Target)

	response := tlv.StatusResponse{
		Target: cmd.Target,
		Status: "received",
	}

	reply(response.Encode())
}

func main() {
	log.Default().SetLevel(log.LevelTrace)
	repo := NewRepo("/ndn/drepo")
	repo.Start()
}
