package main

import (
	"context"
	"crypto/elliptic"
	"fmt"
	"strings"
	"time"

	"github.com/cloudflare/cloudflare-go"
	enc "github.com/named-data/ndnd/std/encoding"
	"github.com/named-data/ndnd/std/log"
	"github.com/named-data/ndnd/std/ndn"
	"github.com/named-data/ndnd/std/security"
	"github.com/named-data/ndnd/std/security/ndncert"
	sig "github.com/named-data/ndnd/std/security/signer"
)

func cleanRecords(api cloudflare.API, zoneID string) error {
	ctx := context.Background()

	records, _, err := api.ListDNSRecords(
		ctx,
		cloudflare.ZoneIdentifier(zoneID),
		cloudflare.ListDNSRecordsParams{Type: "TXT"})
	if err != nil {
		return fmt.Errorf("failed to list DNS records: %w", err)
	}

	// potentially dangerous
	for _, record := range records {
		if strings.HasPrefix(record.Name, "_ndncert-challenge") {
			api.DeleteDNSRecord(ctx, cloudflare.ZoneIdentifier(zoneID), record.ID)
		}
	}
	return nil
}

func RequestCertWithCloudflare(app *ndn.Engine, caCert []byte, domain string, cfToken string, cfZoneID string) (*ndncert.RequestCertResult, error) {
	// start with ndncert client
	client, err := ndncert.NewClient(*app, caCert)
	if err != nil {
		log.Fatal(nil, "Failed to create NDNCERT client", "err", err)
	}

	// without this, the client doesn't know what name to request or key to use
	identity := client.CaPrefix().Append(enc.NewGenericComponent(domain))
	log.Info(nil, "requesting cert for", "name", identity)
	signer, err := sig.KeygenEcc(security.MakeKeyName(identity), elliptic.P256())
	if err != nil {
		return nil, fmt.Errorf("failed to generate dns challenge key: %w", err)
	}
	client.SetSigner(signer)

	// connect with cloudflare api
	api, err := cloudflare.NewWithAPIToken(cfToken)
	if err != nil {
		return nil, fmt.Errorf("failed to create cloudflare client: %w", err)
	}
	// remove any previous records
	cleanRecords(*api, cfZoneID)
	var recordID = ""
	// if a new record was added, delete it
	defer func() {
		// don't try to
		if recordID != "" {
			api.DeleteDNSRecord(context.Background(), cloudflare.ZoneIdentifier(cfZoneID), recordID)
		}
	}()

	// with doubling each time, this gives a maximum of 62 seconds which
	// happens to be 2 seconds longer than the worst case scenario with cache
	var sleepTime = time.Second
	chal := &ndncert.ChallengeDns{
		DomainCallback: func(status string) string {
			log.Debug(nil, "domain callback")
			return domain
		},
		ConfirmationCallback: func(recordName, expectedValue, status string) string {
			log.Debug(nil, "confirmation callback", "status", status)
			if status == "ready-for-validation" {
				return "ready"
			} // else, it is from an empty domain, needs record, or wrong record
			// let's assume that domain is not empty

			if recordID != "" {
				sleepTime *= 2
				time.Sleep(sleepTime)
				return "ready"
			}

			log.Info(nil, "Creating DNS TXT record", "name", recordName, "content", expectedValue)
			rec, err := api.CreateDNSRecord(context.Background(), cloudflare.ZoneIdentifier(cfZoneID), cloudflare.CreateDNSRecordParams{
				Type:    "TXT",
				Name:    recordName,
				Content: "\"" + expectedValue + "\"",
				TTL:     60, // lowest amount available by Cloudflare API
			})
			if err != nil {
				log.Error(nil, "Failed to create Cloudflare DNS record", "err", err)
				return "error"
			}
			recordID = rec.ID

			log.Info(nil, "Created", "record", rec.Name, "content", rec.Content)
			time.Sleep(sleepTime)

			return "ready"
		},
	}

	log.Debug(nil, "request cert")
	return client.RequestCert(ndncert.RequestCertArgs{
		Challenge:    chal,
		DisableProbe: true,
	})
}
