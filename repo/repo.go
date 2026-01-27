package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/a-thieme/repo/tlv"

	enc "github.com/named-data/ndnd/std/encoding"
	"github.com/named-data/ndnd/std/engine"
	"github.com/named-data/ndnd/std/log"
	"github.com/named-data/ndnd/std/ndn"
	"github.com/named-data/ndnd/std/object"
	local_storage "github.com/named-data/ndnd/std/object/storage"
	sec "github.com/named-data/ndnd/std/security"
	"github.com/named-data/ndnd/std/security/keychain"
	"github.com/named-data/ndnd/std/security/ndncert"
	"github.com/named-data/ndnd/std/security/trust_schema"
	"github.com/named-data/ndnd/std/sync"
)

const NOTIFY = "notify"

type Repo struct {
	groupPrefix  enc.Name
	notifyPrefix *enc.Name

	nodePrefix enc.Name

	// Cloudflare / NDNCERT Configuration
	domain   string
	cfToken  string
	cfZoneID string

	engine ndn.Engine
	store  ndn.Store
	client ndn.Client

	groupSync *sync.SvsALO

	commands []*tlv.Command
}

func NewRepo(groupPrefix string, domain string, cfToken string, cfZoneID string) *Repo {
	gp, _ := enc.NameFromStr(groupPrefix)
	nf := gp.Append(enc.NewGenericComponent(NOTIFY))

	return &Repo{
		groupPrefix:  gp,
		notifyPrefix: &nf,
		domain:       domain,
		cfToken:      cfToken,
		cfZoneID:     cfZoneID,
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

	// ---------------------------------------------------------
	// NDNCERT Bootstrapping
	// ---------------------------------------------------------

	// 1. Fetch the NDN Testbed Root CA Certificate from GitHub (Base64 encoded)
	const trustAnchorURL = "https://raw.githubusercontent.com/named-data/testbed/refs/heads/main/anchors/ndn-testbed-root.ndncert.2204.base64"

	log.Info(r, "fetching testbed CA certificate", "url", trustAnchorURL)
	resp, err := http.Get(trustAnchorURL)
	if err != nil {
		return fmt.Errorf("failed to fetch testbed CA cert: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to fetch testbed CA cert: status %d", resp.StatusCode)
	}

	base64Content, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read testbed CA cert: %w", err)
	}

	caCertBytes, err := base64.StdEncoding.DecodeString(string(base64Content))
	if err != nil {
		return fmt.Errorf("failed to decode base64 CA cert: %w", err)
	}

	// 2. Initialize NDNCERT Client
	certClient, err := ndncert.NewClient(r.engine, caCertBytes)
	if err != nil {
		return fmt.Errorf("failed to create ndncert client: %w", err)
	}

	// 3. Request Certificate via Cloudflare DNS Challenge
	log.Info(r, "requesting certificate via NDNCERT", "domain", r.domain)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	certRes, err := RequestCertWithCloudflare(ctx, certClient, r.domain, r.cfToken, r.cfZoneID)
	if err != nil {
		return fmt.Errorf("certificate request failed: %w", err)
	}
	log.Info(r, "obtained certificate", "name", certRes.CertData.Name())

	// 4. Update Node Prefix based on the obtained identity
	idName, err := sec.GetIdentityFromCertName(certRes.CertData.Name())
	if err != nil {
		return err
	}
	r.nodePrefix = idName
	log.Info(r, "setting node prefix", "prefix", r.nodePrefix)

	// ---------------------------------------------------------
	// Keychain & Trust Setup
	// ---------------------------------------------------------

	kc, err := keychain.NewKeyChain("dir:///home/adam/.ndn/keys", r.store)
	if err != nil {
		return err
	}

	// Import the generated key and certificate into the Keychain
	if err := kc.InsertKey(certRes.Signer); err != nil {
		log.Warn(r, "failed to insert key into keychain (might be memory-only signer)", "err", err)
	}
	if err := kc.InsertCert(certRes.CertData.Content().Join()); err != nil {
		return fmt.Errorf("failed to insert cert into keychain: %w", err)
	}

	// Create Trust Config
	schema := trust_schema.NewNullSchema()

	// We trust the Testbed Root (bootstrapped above) and our own key
	caData, _, err := r.engine.Spec().ReadData(enc.NewBufferView(caCertBytes))
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
			Client:      r.client,
			GroupPrefix: r.groupPrefix,
		},
		Snapshot: &sync.SnapshotNull{},
	})
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
	log.Debug(r, "announce", "prefix", &r.notifyPrefix)
	r.client.AnnouncePrefix(ndn.Announcement{
		Name:   r.notifyPrefix.Clone(),
		Expose: true,
	})
	log.Debug(r, "attach command handler")
	r.client.AttachCommandHandler(*r.notifyPrefix, r.onCommand)

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

	log.Debug(r, "reply")
	reply(response.Encode())

	log.Debug(r, "publish to group")
	r.groupSync.Publish(cmd.Encode())
}

func main() {
	log.Default().SetLevel(log.LevelDebug)

	// Get configuration from Environment Variables
	cfToken := os.Getenv("CF_TOKEN")
	cfZoneID := os.Getenv("CF_ZONE_ID")
	domain := os.Getenv("NDN_DOMAIN")

	if cfToken == "" || cfZoneID == "" || domain == "" {
		log.Fatal(nil, "Missing required environment variables: CF_TOKEN, CF_ZONE_ID, NDN_DOMAIN")
	}

	// Initialize Repo with DNS domain instead of hardcoded node prefix
	repo := NewRepo("/ndn/drepo", domain, cfToken, cfZoneID)

	if err := repo.Start(); err != nil {
		log.Fatal(nil, "Unable to start repo", "err", err)
	}

	// Wait for a signal to quit (like Ctrl+C)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
}
