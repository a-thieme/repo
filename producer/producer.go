package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/a-thieme/repo/tlv"

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

const (
	defaultRetries   = 3
	retryBaseDelayMs = 100
	maxRetryDelayMs  = 1000
)

func ExpressCommand(c ndn.Client, dest enc.Name, name enc.Name, cmd enc.Wire, maxRetries int, callback func(enc.Wire, error)) {
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

	var mu sync.Mutex
	resultCh := make(chan struct {
		wire enc.Wire
		err  error
	}, maxRetries)
	success := make(chan struct{})

	var attemptExpr func(data *ndn.EncodedData, attempt int)
	attemptExpr = func(thisData *ndn.EncodedData, attempt int) {
		c.ExpressR(ndn.ExpressRArgs{
			Name: dest,
			Config: &ndn.InterestConfig{
				CanBePrefix: false,
				MustBeFresh: true,
			},
			AppParam: thisData.Wire,
			Retries:  0,
			Callback: func(args ndn.ExpressCallbackArgs) {
				if args.Result != ndn.InterestResultData {
					if attempt < maxRetries-1 {
						delayMs := retryBaseDelayMs * (1 << attempt)
						if delayMs > maxRetryDelayMs {
							delayMs = maxRetryDelayMs
						}
						time.Sleep(time.Duration(delayMs) * time.Millisecond)
						attemptExpr(thisData, attempt+1)
					} else {
						resultCh <- struct {
							wire enc.Wire
							err  error
						}{nil, fmt.Errorf("command failed after %d retries: %s", maxRetries, args.Result)}
					}
					return
				}
				c.Validate(args.Data, thisData.Wire, func(valid bool, err error) {
					mu.Lock()
					select {
					case <-success:
						mu.Unlock()
						return
					default:
					}
					close(success)
					mu.Unlock()

					resultCh <- struct {
						wire enc.Wire
						err  error
					}{args.Data.Content(), nil}
				})
			},
		})
	}

	go attemptExpr(data, 0)

	select {
	case result := <-resultCh:
		callback(result.wire, result.err)
	case <-time.After(10 * time.Second):
		mu.Lock()
		select {
		case <-success:
			mu.Unlock()
			return
		default:
		}
		close(success)
		mu.Unlock()
		callback(nil, fmt.Errorf("command timed out after %d retries", maxRetries))
	}
}

type BasicSchema struct{}

func (s *BasicSchema) Check(pkt enc.Name, cert enc.Name) bool {
	fmt.Println("checking data", pkt.Clone().String())
	return true
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
	count := flag.Int("count", 1, "number of commands to send")
	rate := flag.Int("rate", 1, "commands per second")
	timeout := flag.Duration("timeout", 10*time.Second, "command response timeout")
	cmdType := flag.String("type", "insert", "command type: insert, join, or both")
	joinRatio := flag.Float64("join-ratio", 0.5, "ratio of JOIN commands when type is both (0.0-1.0)")
	retries := flag.Int("retries", defaultRetries, "max retries for failed commands")
	flag.Parse()

	validTypes := map[string]bool{"insert": true, "join": true, "both": true}
	if !validTypes[*cmdType] {
		fmt.Println("Error: -type must be 'insert', 'join', or 'both'")
		flag.Usage()
		return
	}

	if *joinRatio < 0.0 || *joinRatio > 1.0 {
		fmt.Println("Error: -join-ratio must be between 0.0 and 1.0")
		flag.Usage()
		return
	}

	log.Default().SetLevel(log.LevelInfo)
	log.Info(nil, "producer_starting", "count", *count, "rate", *rate, "type", *cmdType)
	engine := engine.NewBasicEngine(engine.NewDefaultFace())
	engine.Start()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sig
		engine.Stop()
		os.Exit(1)
	}()

	store := local_storage.NewMemoryStore()
	notify, _ := enc.NameFromStr("/ndn/drepo/notify")
	prefix, _ := enc.NameFromStr("/ndn/repo.teame.dev/producer")

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

	client := object.NewClient(engine, store, trust)
	log.Debug(nil, "announce", "prefix", prefix)
	client.AnnouncePrefix(ndn.Announcement{
		Name:   prefix,
		Expose: true,
	})

	interval := time.Second / time.Duration(*rate)
	for i := 0; i < *count; i++ {
		if i > 0 {
			time.Sleep(interval)
		}

		target, _ := enc.NameFromStr("/ndn/repo.teame.dev/producer/mytarget/")
		target = target.Append(enc.NewTimestampComponent(uint64(time.Now().UnixNano())))

		var commandType string
		switch *cmdType {
		case "insert":
			commandType = "INSERT"
		case "join":
			commandType = "JOIN"
		case "both":
			if float64(i%2) < *joinRatio*2 {
				commandType = "JOIN"
			} else {
				commandType = "INSERT"
			}
		}

		command := tlv.Command{
			Type:   commandType,
			Target: target,
		}

		targetStr := target.String()
		log.Info(nil, "command_issued", "type", commandType, "target", targetStr, "correlationID", targetStr)

		done := make(chan struct{})

		fmt.Printf("Sending command %d/%d (type=%s)...\n", i+1, *count, commandType)
		log.Info(nil, "command_send_started", "attempt", i+1, "total", *count, "correlationID", targetStr)
		ExpressCommand(client, notify, target, command.Encode(), *retries,
			func(w enc.Wire, e error) {
				defer close(done)
				if e != nil {
					log.Error(nil, "command_send_failed", "correlationID", targetStr, "error", e.Error())
					fmt.Println("Error:", e.Error())
					return
				}
				sr, err := tlv.ParseStatusResponse(enc.NewWireView(w), false)
				if err != nil {
					log.Error(nil, "command_response_parse_failed", "correlationID", targetStr, "error", err.Error())
					fmt.Println("Parse Error:", err.Error())
					return
				}
				log.Info(nil, "command_acked", "target", sr.Target.String(), "status", sr.Status, "correlationID", targetStr)
				fmt.Println("Target:", sr.Target)
				fmt.Println("Status:", sr.Status)
			})

		select {
		case <-done:
		case <-time.After(*timeout):
			fmt.Println("Error: command timed out")
			engine.Stop()
			return
		}
	}

	engine.Stop()
	log.Info(nil, "producer_finished", "count", *count)
	fmt.Println("finished")
}
