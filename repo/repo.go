package main

import (
	_ "embed"
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
	"github.com/named-data/ndnd/std/sync"
)

const NOTIFY = "notify"

var testbedRootName, _ = enc.NameFromStr("/ndn/KEY/%27%C4%B2%2A%9F%7B%81%27/ndn/v=1651246789556")

//go:embed testbed-root.decoded
var testbedRootCert []byte

type Repo struct {
	groupPrefix  enc.Name
	notifyPrefix *enc.Name
	nodePrefix   enc.Name

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
	r.store = local_storage.NewMemoryStore()

	kc, err := keychain.NewKeyChain("dir:///home/adam/.ndn/keys", r.store)
	if err != nil {
		return err
	}

	// Create Trust Config
	schema := &BasicSchema{}

	// We trust the Testbed Root (bootstrapped above) and our own key
	caData, _, err := r.engine.Spec().ReadData(enc.NewBufferView(testbedRootCert))
	if err != nil {
		return err
	}

	trust, err := sec.NewTrustConfig(kc, schema, []enc.Name{caData.Name()})
	if err != nil {
		return err
	}
	trust.UseDataNameFwHint = true

	// ---------------------------------------------------------
	// Application Start
	// ---------------------------------------------------------

	log.Debug(r, "new client")
	r.client = object.NewClient(r.engine, r.store, trust)

	log.Info(r, "starting sync", "group", r.groupPrefix)
	r.groupSync, err = sync.NewSvsALO(sync.SvsAloOpts{
		Name: r.nodePrefix,
		Svs: sync.SvSyncOpts{
			Client:       r.client,
			GroupPrefix:  r.groupPrefix,
			SyncDataName: r.nodePrefix,
		},
		Snapshot: &sync.SnapshotNull{},
	})
	if err != nil {
		return err
	}
	err = r.groupSync.SubscribePublisher(enc.Name{}, r.onGroupSync)
	if err != nil {
		return err
	}

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
	log.Debug(r, "announce", "prefix", r.notifyPrefix)
	r.client.AnnouncePrefix(ndn.Announcement{
		Name:   r.notifyPrefix.Clone(),
		Expose: true,
	})
	log.Debug(r, "attach command handler")
	r.client.AttachCommandHandler(*r.notifyPrefix, r.onCommand)
	return r.client.Start()
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

func (r *Repo) onGroupSync(pub sync.SvsPub) {
	if len(pub.Content) == 0 {
		return
	}
	cmd, err := tlv.ParseCommand(enc.NewWireView(pub.Content), false)
	if err != nil {
		log.Warn(r, "command parse error", "name", pub.DataName)
		return
	}
	log.Info(r, "received command from sync",
		"type", cmd.Type,
		"target", cmd.Target,
		"from", pub.Publisher,
		"seq", pub.SeqNum,
	)
}

// BasicSchema allows all data and suggests the first matching key in the keychain.
type BasicSchema struct{}

func (s *BasicSchema) Check(pkt enc.Name, cert enc.Name) bool {
	return true // Trust everything (matching NullSchema behavior)
}

func (s *BasicSchema) Suggest(name enc.Name, kc ndn.KeyChain) ndn.Signer {
	myname, _ := enc.NameFromStr("/ndn/repo.teame.dev/repo")
	for _, id := range kc.Identities() {
		if id.Name().IsPrefix(myname) {
			if len(id.Keys()) > 0 {
				return id.Keys()[0].Signer()
			}
		}
	}

	return signer.NewSha256Signer()
}
