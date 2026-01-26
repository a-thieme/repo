package main

import (
	"github.com/a-thieme/repo/tlv"

	"os"
	"os/signal"
	"syscall"

	enc "github.com/named-data/ndnd/std/encoding"
	"github.com/named-data/ndnd/std/engine"
	"github.com/named-data/ndnd/std/log"
	"github.com/named-data/ndnd/std/ndn"
	"github.com/named-data/ndnd/std/object"
	local_storage "github.com/named-data/ndnd/std/object/storage"
	// "github.com/named-data/ndnd/std/security/signer"
	"github.com/named-data/ndnd/std/sync"

	sec "github.com/named-data/ndnd/std/security"
	"github.com/named-data/ndnd/std/security/keychain"
	"github.com/named-data/ndnd/std/security/trust_schema"
)

const NOTIFY = "notify"

type Repo struct {
	groupPrefix  enc.Name
	notifyPrefix *enc.Name

	nodePrefix enc.Name

	engine ndn.Engine
	store  ndn.Store
	client ndn.Client

	groupSync *sync.SvsALO

	commands []*tlv.Command
}

func NewRepo(groupPrefix string, nodePrefix string) *Repo {
	gp, _ := enc.NameFromStr(groupPrefix)
	np, _ := enc.NameFromStr(nodePrefix)
	nf := gp.Append(enc.NewGenericComponent(NOTIFY))
	return &Repo{
		groupPrefix:  gp,
		notifyPrefix: &nf,
		nodePrefix:   np,
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

	// log.Debug(r, "new keychain")
	log.Debug(r, "new keychain")
	kc, err := keychain.NewKeyChain("dir:///tmp/ndn/repo1/keys", r.store)
	if err != nil {
		return err
	}

	// // TODO: specify a real trust schema
	log.Debug(r, "new null schema")
	schema := trust_schema.NewNullSchema()

	testbedRootName, err := enc.NameFromStr("/ndn/KEY/%27%C4%B2%2A%9F%7B%81%27/ndn/v=1651246789556")
	if err != nil {
		return err
	}

	log.Debug(r, "new trust config")
	trust, err := sec.NewTrustConfig(kc, schema, []enc.Name{testbedRootName})
	if err != nil {
		return err
	}
	//
	// // Attach data name as forwarding hint to cert Interests
	trust.UseDataNameFwHint = true

	// new client
	log.Debug(r, "new client")
	r.client = object.NewClient(r.engine, r.store, trust)
	// group svs

	// publish command to group
	// group svs
	log.Info(r, "starting sync", "group", r.groupPrefix)
	r.groupSync, err = sync.NewSvsALO(sync.SvsAloOpts{
		Name: r.nodePrefix,
		Svs: sync.SvSyncOpts{
			Client:      r.client,
			GroupPrefix: r.groupPrefix,
		},
		Snapshot: &sync.SnapshotNull{},
	})
	err = r.groupSync.Start()
	if err != nil {
		return err
	}

	log.Debug(r, "announce", "prefix", r.groupSync.GroupPrefix())
	r.client.AnnouncePrefix(ndn.Announcement{
		Name:   r.groupSync.GroupPrefix(),
		Expose: true,
	})
	log.Debug(r, "announce", "prefix", r.groupSync.DataPrefix())
	r.client.AnnouncePrefix(ndn.Announcement{
		Name:   r.groupSync.DataPrefix(),
		Expose: true,
	})
	// client notification
	log.Debug(r, "announce", "prefix", &r.notifyPrefix)
	r.client.AnnouncePrefix(ndn.Announcement{
		Name:   r.notifyPrefix.Clone(),
		Expose: true,
	})
	log.Debug(r, "attach command handler")
	r.client.AttachCommandHandler(*r.notifyPrefix, r.onCommand)

	return err
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

	log.Debug(r, "reply")
	reply(response.Encode())

	log.Debug(r, "publish to group")
	r.groupSync.Publish(cmd.Encode())
}

func main() {
	log.Default().SetLevel(log.LevelTrace)
	repo := NewRepo("/ndn/drepo", "/ndn/myrepo1")
	if err := repo.Start(); err != nil {
		log.Fatal(nil, "Unable to start repo", "err", err)
	}

	// Wait for a signal to quit (like Ctrl+C)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
}
