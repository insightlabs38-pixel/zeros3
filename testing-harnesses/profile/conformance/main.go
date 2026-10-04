// Command conformance runs the ZeroS3 Core Client Profile v1 suite against a
// running S3 endpoint with the AWS SDK for Go v2 and minio-go.
//
//	go run ./profile/conformance -endpoint http://host:9000 -access-key K -secret-key S [-region R]
//	go run ./profile/conformance -managed -bin /path/to/zeros3 [-json] [-v]
//
// -managed starts a throwaway ZeroS3 (portable scenarios plus a restart-
// persistence check). The portable scenarios use only the S3 wire contract,
// so any endpoint claiming the profile can be targeted.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"zeros3-testing/profile/core"
)

type summary struct {
	Profile string        `json:"profile"`
	Passed  int           `json:"passed"`
	Failed  int           `json:"failed"`
	Skipped int           `json:"skipped"`
	Reports []core.Report `json:"reports"`
}

func main() { os.Exit(run()) }

// run returns the exit code so deferred cleanup (fixture shutdown) always runs.
func run() int {
	endpoint := flag.String("endpoint", "", "S3 endpoint URL (path-style); omit with -managed")
	access := flag.String("access-key", core.DefaultAccessKey, "access key")
	secret := flag.String("secret-key", core.DefaultSecretKey, "secret key")
	region := flag.String("region", "us-east-1", "endpoint SigV4 region")
	clients := flag.String("client", "aws,minio", "comma-separated clients: aws, minio")
	scen := flag.String("scenario", "", "comma-separated scenario names (default: all)")
	managed := flag.Bool("managed", false, "start a ZeroS3 fixture from -bin (also runs the restart-persistence check)")
	bin := flag.String("bin", os.Getenv("ZEROS3_BIN"), "zeros3 binary for -managed")
	asJSON := flag.Bool("json", false, "emit the JSON summary only")
	verbose := flag.Bool("v", false, "list every case")
	flag.Parse()

	cfg := core.Config{Endpoint: *endpoint, AccessKey: *access, SecretKey: *secret, Region: *region, Verbose: *verbose}
	var srv *core.Server
	var store string
	if *managed {
		var err error
		if store, err = os.MkdirTemp("", "zeros3-conformance-"); err != nil {
			return fail(err)
		}
		defer os.RemoveAll(store)
		if srv, err = core.StartServer(*bin, store, *region); err != nil {
			return fail(err)
		}
		defer func() { srv.Stop() }()
		cfg.Endpoint = srv.Endpoint
	}
	if cfg.Endpoint == "" {
		return fail(fmt.Errorf("-endpoint or -managed is required"))
	}
	var names []string
	if *scen != "" {
		names = strings.Split(*scen, ",")
	}

	sum := summary{Profile: core.Profile}
	for _, name := range strings.Split(*clients, ",") {
		var c core.Client
		var err error
		switch name {
		case "aws":
			c, err = core.NewAWS(cfg)
		case "minio":
			c, err = core.NewMinio(cfg)
		default:
			err = fmt.Errorf("unknown client %q", name)
		}
		if err != nil {
			return fail(err)
		}
		rep := core.Run(c, cfg, names...)
		sum.Reports = append(sum.Reports, rep)
		sum.Passed, sum.Failed, sum.Skipped = sum.Passed+rep.Passed, sum.Failed+rep.Failed, sum.Skipped+rep.Skipped
	}

	if cli := awsCLISmoke(withPrefix(cfg)); cli.Passed+cli.Failed+cli.Skipped > 0 {
		sum.Reports = append(sum.Reports, cli)
		sum.Passed, sum.Failed, sum.Skipped = sum.Passed+cli.Passed, sum.Failed+cli.Failed, sum.Skipped+cli.Skipped
	}

	if *managed {
		rep := restartPersistence(&srv, *bin, store, cfg)
		sum.Reports = append(sum.Reports, rep)
		sum.Passed, sum.Failed = sum.Passed+rep.Passed, sum.Failed+rep.Failed
	}

	if *asJSON {
		out, _ := json.MarshalIndent(sum, "", "  ")
		fmt.Println(string(out))
	} else {
		for _, r := range sum.Reports {
			fmt.Printf("%-14s %s passed=%d failed=%d skipped=%d\n", r.Client, r.Profile, r.Passed, r.Failed, r.Skipped)
			for _, f := range r.Failures {
				fmt.Printf("  FAIL %s: %s: %s\n", f.Scenario, f.Contract, f.Detail)
			}
			for _, c := range r.Cases {
				fmt.Println("  " + c)
			}
		}
	}
	if sum.Failed > 0 {
		return 1
	}
	return 0
}

// restartPersistence is the ZeroS3-fixture-only part of the run: committed
// objects, metadata and buckets survive a graceful restart on the same store.
func restartPersistence(srv **core.Server, bin, store string, cfg core.Config) core.Report {
	rep := core.Report{Profile: core.Profile, Client: "fixture-restart", Endpoint: cfg.Endpoint}
	fail := func(what string, err any) {
		rep.Failed++
		rep.Failures = append(rep.Failures, core.Failure{Scenario: "restart-persistence", Contract: what, Detail: fmt.Sprint(err)})
	}
	c, err := core.NewAWS(cfg)
	if err != nil {
		fail("client", err)
		return rep
	}
	b, body := "persist-bucket", core.Data(77, 600_000)
	if err := c.CreateBucket(b); err != nil {
		fail("CreateBucket", err)
		return rep
	}
	if _, err := c.Put(b, "k", body, core.PutOpts{ContentType: "x/persist", Meta: map[string]string{"m": "1"}}); err != nil {
		fail("PutObject", err)
		return rep
	}
	(*srv).Stop()
	s2, err := core.StartServer(bin, store, cfg.Region)
	if err != nil {
		fail("restart", err)
		return rep
	}
	*srv = s2
	cfg.Endpoint = s2.Endpoint
	if c, err = core.NewAWS(cfg); err != nil {
		fail("client", err)
		return rep
	}
	g, err := c.Get(b, "k", core.GetOpts{})
	if err != nil || !bytes.Equal(g.Body, body) || g.Meta["m"] != "1" || g.ContentType != "x/persist" {
		fail("object and metadata survive restart", err)
	} else {
		rep.Passed++
	}
	if names, err := c.ListBuckets(); err != nil || !contains(names, b) {
		fail("buckets survive restart", err)
	} else {
		rep.Passed++
	}
	return rep
}

func contains(l []string, s string) bool {
	for _, v := range l {
		if v == s {
			return true
		}
	}
	return false
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "conformance:", err)
	return 2
}

func withPrefix(cfg core.Config) core.Config {
	if cfg.Prefix == "" {
		cfg.Prefix = "core-cli-" + fmt.Sprint(os.Getpid())
	}
	return cfg
}
