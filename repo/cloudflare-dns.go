package main

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudflare/cloudflare-go"
	enc "github.com/named-data/ndnd/std/encoding"
	"github.com/named-data/ndnd/std/security/ndncert"
	"github.com/named-data/ndnd/std/security/ndncert/tlv"
)

// RequestCertWithCloudflare automates the NDNCERT DNS challenge using Cloudflare.
// It creates the TXT record, waits for propagation, and deletes it upon completion.
func RequestCertWithCloudflare(ctx context.Context, client *ndncert.Client, domain string, cfToken string, cfZoneID string) (*ndncert.RequestCertResult, error) {
	// Initialize Cloudflare API client
	api, err := cloudflare.NewWithAPIToken(cfToken)
	if err != nil {
		return nil, fmt.Errorf("failed to create cloudflare client: %w", err)
	}

	var recordID string // Track the created record ID for cleanup

	// Define the DNS challenge with automated callbacks
	chal := &ndncert.ChallengeDns{
		// Callback to provide the domain name to the CA
		DomainCallback: func(status string) string {
			return domain
		},
		// Callback to handle the DNS record creation
		ConfirmationCallback: func(recordName, expectedValue, status string) string {
			// If the CA is verifying or we are done, just return ready
			if status != "need-record" && status != "wrong-record" {
				return "ready"
			}

			// If we already created a record but the CA failed to see it (wrong-record),
			// we wait a bit longer and tell the CA to try again.
			if recordID != "" {
				fmt.Println("CA failed to verify record, waiting longer for propagation...")
				time.Sleep(10 * time.Second)
				return "ready"
			}

			fmt.Printf("Creating DNS TXT record: %s -> %s\n", recordName, expectedValue)

			// Create the TXT record on Cloudflare
			// Note: This assumes the standard cloudflare-go library interface.
			rec, err := api.CreateDNSRecord(ctx, cfZoneID, cloudflare.DNSRecord{
				Type:    "TXT",
				Name:    recordName,
				Content: expectedValue,
				TTL:     120, // Short TTL for faster cleanup
			})
			if err != nil {
				fmt.Printf("Failed to create Cloudflare DNS record: %v\n", err)
				// NDNCERT expects "ready" to proceed. Returning anything else will likely
				// cause a local error in the client, which is what we want here.
				return "error"
			}

			// Store ID for cleanup
			recordID = rec.Result.ID

			// Wait for propagation (Cloudflare is fast, but 5-10s ensures global availability)
			fmt.Println("Record created, waiting for propagation...")
			time.Sleep(10 * time.Second)

			return "ready"
		},
	}

	// Defer cleanup of the DNS record
	defer func() {
		if recordID != "" {
			fmt.Printf("Cleaning up DNS record %s...\n", recordID)
			err := api.DeleteDNSRecord(context.Background(), cfZoneID, recordID)
			if err != nil {
				fmt.Printf("Warning: Failed to delete DNS record: %v\n", err)
			}
		}
	}()

	// Execute the certificate request
	// This blocks until the process is complete or fails
	return client.RequestCert(ndncert.RequestCertArgs{
		Challenge: chal,
		// Simple profile callback
		OnProfile: func(profile *tlv.CaProfile) error {
			return nil
		},
		// Provide the domain as a probe parameter if the CA requires it
		OnProbeParam: func(key string) ([]byte, error) {
			if key == ndncert.KwDomain {
				return []byte(domain), nil
			}
			return nil, nil
		},
		// Use the default key choice behavior (first suggestion or generate new)
		OnChooseKey: nil,
		OnKeyChosen: func(keyName enc.Name) error {
			fmt.Printf("Key chosen: %s\n", keyName)
			return nil
		},
	})
}
