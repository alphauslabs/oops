package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/credentials/stscreds"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/sns"
	"github.com/dchest/uniuri"
	lssqs "github.com/flowerinthenight/longsub/awssqs"
	lspubsub "github.com/flowerinthenight/longsub/gcppubsub"
	yaml "github.com/goccy/go-yaml"
	"github.com/spf13/cobra"
)

var (
	rootcmd = &cobra.Command{
		Use:   "oops",
		Short: "k8s-native testing tool",
		Long:  "Kubernetes-native testing tool.",
		RunE:  runE,
	}

	project string
	pubsub  string

	region  string
	key     string
	secret  string
	rolearn string
	snssqs  string

	files []string
	dir   string
	tags  []string

	repslack  string
	reppubsub string

	verbose bool
)

type cmd struct {
	// Valid values: start | process
	// start = initiate distribution of files in --dir to SNS
	// process = normal processing (one yaml at a time)
	Code string `json:"code"`

	// To identify a batch. Sent by the initiator together with
	// the 'process' code.
	ID string `json:"id"`

	// The file to process. Sent together with the 'process' code.
	Scenario string `json:"scenario"`

	// Optional tags to filter scenarios. Format: ["key=value", "key2=value2"]
	// When provided with 'start' code, only scenarios matching ALL tags will be distributed.
	Tags []string `json:"tags,omitempty"`

	// Metadata for cancellation requests
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

func runE(cmd *cobra.Command, args []string) error {
	return doScenario(&doScenarioInput{
		ScenarioFiles: combineFilesAndDir(),
		ReportSlack:   repslack,
		ReportPubsub:  reppubsub,
		Verbose:       verbose,
	})
}

func combineFilesAndDir() []string {
	tmp := make(map[string]struct{})
	for _, v := range files {
		f, _ := filepath.Abs(v)
		tmp[f] = struct{}{}
	}

	for _, f := range findScenarioFiles(dir) {
		tmp[f] = struct{}{}
	}

	var final []string
	for k := range tmp {
		_, err := os.Stat(k)
		if os.IsNotExist(err) {
			log.Printf("File does not exist: %v", k)
		} else {
			final = append(final, k)
		}
	}

	if len(final) == 0 {
		log.Fatal("No files found. Please recheck directory.")
	}

	return final
}

func findScenarioFiles(root string) []string {
	patterns := []string{
		filepath.Join(root, "services", "*", "scenarios"),
		filepath.Join(root, "cloudrun", "*", "scenarios"),
		filepath.Join(root, "cronjobs", "*", "scenarios"),
		filepath.Join(root, "serverless", "*", "scenarios"),
		filepath.Join(root, "microapps", "*", "scenarios"),
		filepath.Join(root, "cmd", "*", "scenarios"),
		filepath.Join(root, "pkg", "*", "scenarios"),
	}

	var out []string
	for _, p := range patterns {
		dirs, err := filepath.Glob(p)
		if err != nil {
			log.Printf("glob %v: %v", p, err)
			continue
		}
		for _, d := range dirs {
			filepath.Walk(d, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if !info.IsDir() && strings.HasSuffix(path, ".yaml") {
					abs, _ := filepath.Abs(path)
					log.Printf("input: %v", abs)
					out = append(out, abs)
				}
				return nil
			})
		}
	}
	return out
}

func filterScenariosByTags(files []string, tagFilters []string) []string {
	if len(tagFilters) == 0 {
		return files
	}

	var filtered []string
	for _, f := range files {
		yml, err := os.ReadFile(f)
		if err != nil {
			log.Printf("failed to read file %v: %v", f, err)
			continue
		}

		var s Scenario
		err = yaml.Unmarshal(yml, &s)
		if err != nil {
			log.Printf("failed to unmarshal yaml %v: %v", f, err)
			continue
		}

		if isAllowedWithTags(&s, tagFilters) {
			filtered = append(filtered, f)
		} else {
			log.Printf("%v filtered out by tags", f)
		}
	}

	return filtered
}

func isAllowedWithTags(s *Scenario, tagFilters []string) bool {
	if len(tagFilters) == 0 {
		return true
	}

	var matched int
	for _, t := range tagFilters {
		tt := strings.Split(t, "=")
		if len(tt) != 2 {
			continue
		}

		for k, v := range s.Tags {
			if k == tt[0] && v == tt[1] {
				matched++
			}
		}
	}

	return matched == len(tagFilters)
}

func extractAffectedServices(metadata map[string]interface{}) []string {
	ta, ok := metadata["test_analysis"].(map[string]interface{})
	if !ok {
		return nil
	}

	seen := make(map[string]struct{})
	var result []string

	for _, key := range []string{
		"affected_services",
		"affected_cloudrun",
		"affected_microapps",
		"affected_serverless",
		"affected_packages",
		"affected_commands",
	} {
		v, ok := ta[key].(string)
		if !ok || strings.TrimSpace(v) == "" {
			continue
		}
		for _, name := range strings.Split(v, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if _, dup := seen[name]; !dup {
				seen[name] = struct{}{}
				result = append(result, name)
			}
		}
	}
	return result
}

func filterScenariosByAffectedServices(files []string, affectedServices []string) []string {
	if len(affectedServices) == 0 {
		return files
	}

	svcSet := make(map[string]struct{}, len(affectedServices))
	for _, s := range affectedServices {
		svcSet[strings.ToLower(strings.TrimSpace(s))] = struct{}{}
	}

	var out []string
	for _, f := range files {
		parts := strings.Split(filepath.ToSlash(f), "/")
		for _, part := range parts {
			if _, ok := svcSet[strings.ToLower(part)]; ok {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

func distributePubsub(app *appctx, runID string, tagFilters []string, metadata map[string]interface{}) {
	id := runID
	final := combineFilesAndDir()

	affectedServices := extractAffectedServices(metadata)
	if len(affectedServices) > 0 {
		log.Printf("affected services from metadata: %v", affectedServices)
		before := len(final)
		final = filterScenariosByAffectedServices(final, affectedServices)
		log.Printf("service filter: %d/%d scenarios kept", len(final), before)
	}

	filtered := filterScenariosByTags(final, tagFilters)
	log.Printf("distributing %d/%d scenarios matching tags %v", len(filtered), len(final), tagFilters)
	for _, f := range filtered {
		nc := cmd{
			Code:     "process",
			ID:       id,
			Scenario: f,
			Metadata: metadata,
		}

		err := app.pub.Publish(uniuri.NewLen(10), nc)
		if err != nil {
			log.Printf("publish failed: %v ", err)
			continue
		}
	}
}

func distributeSQS(app *appctx, runID string, tagFilters []string, metadata map[string]interface{}) {
	sess, _ := session.NewSession(&aws.Config{
		Region:      aws.String(region),
		Credentials: credentials.NewStaticCredentials(key, secret, ""),
	})

	var svc *sns.SNS
	if rolearn != "" {
		cnf := &aws.Config{Credentials: stscreds.NewCredentials(sess, rolearn)}
		svc = sns.New(sess, cnf)
	} else {
		svc = sns.New(sess)
	}

	id := runID
	final := combineFilesAndDir()

	affectedServices := extractAffectedServices(metadata)
	if len(affectedServices) > 0 {
		log.Printf("affected services from metadata: %v", affectedServices)
		before := len(final)
		final = filterScenariosByAffectedServices(final, affectedServices)
		log.Printf("service filter: %d/%d scenarios kept", len(final), before)
	}

	filtered := filterScenariosByTags(final, tagFilters)
	log.Printf("distributing %d/%d scenarios matching tags %v", len(filtered), len(final), tagFilters)
	for _, f := range filtered {
		nc := cmd{
			Code:     "process",
			ID:       id,
			Scenario: f,
			Metadata: metadata,
		}

		b, _ := json.Marshal(nc)
		key := uniuri.NewLen(10)
		m := &sns.PublishInput{
			TopicArn: app.topicArn,
			Subject:  &key,
			Message:  aws.String(string(b)),
		}

		_, err := svc.Publish(m)
		if err != nil {
			log.Printf("Publish failed: %v", err)
			continue
		}
	}
}

type appctx struct {
	pub      *lspubsub.PubsubPublisher // starter publisher topic
	rpub     *lspubsub.PubsubPublisher // topic to publish reports
	mtx      *sync.Mutex
	topicArn *string
}

// Our message processing callback.
func process(ctx any, data []byte) error {
	app := ctx.(*appctx)
	app.mtx.Lock()
	defer app.mtx.Unlock()

	var c cmd
	err := json.Unmarshal(data, &c)
	if err != nil {
		log.Printf("Unmarshal failed: %v", err)
		return err
	}

	switch c.Code {
	case "start":
		log.Printf("received start command with tags: %v", c.Tags)
		var dist string
		switch {
		case pubsub != "":
			distributePubsub(app, c.ID, c.Tags, c.Metadata)
			dist = fmt.Sprintf("pubsub=%v", pubsub)
		case snssqs != "":
			distributeSQS(app, c.ID, c.Tags, c.Metadata)
			dist = snssqs
			dist = fmt.Sprintf("sns/sqs=%v", snssqs)
		}

		host, _ := os.Hostname()

		// Send to slack, if any.
		if repslack != "" {
			payload := SlackMessage{
				Attachments: []SlackAttachment{
					{
						Color:     "good",
						Title:     "start tests",
						Text:      fmt.Sprintf("from %v through %v", host, dist),
						Footer:    "oops",
						Timestamp: time.Now().Unix(),
					},
				},
			}

			err = payload.Notify(repslack)
			if err != nil {
				log.Printf("Notify (slack) failed: %v", err)
			}
		}
	case "process":
		log.Printf("process: %+v", c)
		doScenario(&doScenarioInput{
			app:           app,
			ScenarioFiles: []string{c.Scenario},
			ReportSlack:   repslack,
			ReportPubsub:  reppubsub,
			Verbose:       verbose,
			Metadata:      c.Metadata,
			RunID:         c.ID,
		})
	}

	return nil
}

func run(ctx context.Context, done chan error) {
	var err error
	if snssqs != "" && pubsub != "" {
		log.Fatal("cannot set both --sns-sqs and --pubsub")
	}

	log.Printf("rootdir: %v", dir)
	log.Printf("report-slack: %v", repslack)
	if pubsub != "" {
		log.Printf("project: %v", project)
		log.Printf("svcacct: %v", os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"))
	}

	if snssqs != "" {
		log.Printf("region: %v", region)
		log.Printf("key: %v", key)
		log.Printf("rolearn: %v", rolearn)
	}

	app := &appctx{
		mtx: &sync.Mutex{},
	}
	ctx0, cancelCtx0 := context.WithCancel(ctx)
	defer cancelCtx0()
	done0 := make(chan error, 1)

	switch {
	case pubsub != "":
		// Setup reports publisher topic, if provided.
		if reppubsub != "" {
			app.rpub, err = lspubsub.NewPubsubPublisher(project, reppubsub)
			if err != nil {
				log.Fatalf("create publisher %v failed: %v", reppubsub, err)
			}
		}

		// Make sure topic/subscription is created. Only used for creating subscription if needed.
		_, t, err := lspubsub.GetPublisher(project, pubsub)
		if err != nil {
			log.Fatalf("publisher get/create for %v failed: %v", pubsub, err)
		}

		app.pub, err = lspubsub.NewPubsubPublisher(project, pubsub)
		if err != nil {
			log.Fatalf("create publisher %v failed: %v", pubsub, err)
		}

		if app.pub == nil {
			log.Fatalf("fatal error, publisher nil")
		}

		_, err = lspubsub.GetSubscription(project, pubsub, t, time.Second*60)
		if err != nil {
			log.Fatalf("subscription get/create for %v failed: %v", pubsub, err)
		}

		go func() {
			// Messages should be payer level. We will subdivide linked accts to separate messages for
			// linked-acct-level processing.
			ls := lspubsub.NewLengthySubscriber(app, project, pubsub, process)
			err = ls.Start(ctx0, done0)
			if err != nil {
				log.Fatalf("listener for export csv failed: %v", err)
			}
		}()
	case snssqs != "":
		lsh := lssqs.NewHelper(region, key, secret, rolearn)
		t, err := lsh.SetupSnsSqsSubscription(snssqs, snssqs)
		if err != nil {
			log.Fatal(err)
		}

		app.topicArn = t
		log.Printf("%v subscribed to %v", snssqs, snssqs)

		go func() {
			ls := lssqs.NewLengthySubscriber(app, snssqs, process,
				lssqs.WithRegion(region),
				lssqs.WithAccessKeyId(key),
				lssqs.WithSecretAccessKey(secret),
				lssqs.WithRoleArn(rolearn),
			)

			err := ls.Start(ctx0, done0)
			if err != nil {
				log.Fatalf("start long processing for %v failed: %v", snssqs, err)
			}
		}()
	}

	<-ctx.Done()
	done <- <-done0
}

func runCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run as service",
		Long:  "Run oops as a long-running service.",
		RunE: func(cmd *cobra.Command, args []string) error {
			defer func(begin time.Time) {
				log.Printf("stop oops after %v", time.Since(begin))
			}(time.Now())

			log.Printf("start oops on %v", time.Now())
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error)
			go run(ctx, done)

			go func() {
				sigch := make(chan os.Signal, 1)
				signal.Notify(sigch, syscall.SIGINT, syscall.SIGTERM)
				log.Println(<-sigch)
				cancel()
			}()

			return <-done
		},
	}

	cmd.Flags().SortFlags = false
	cmd.Flags().StringVar(&snssqs, "snssqs", snssqs, "name of the SNS topic and SQS queue")
	cmd.Flags().StringVar(&pubsub, "pubsub", pubsub, "name of the GCP pubsub and subscription")
	return cmd
}

func init() {
	rootcmd.PersistentFlags().SortFlags = false
	rootcmd.PersistentFlags().StringVar(&project, "project-id", os.Getenv("GCP_PROJECT_ID"), "GCP project id")
	rootcmd.PersistentFlags().StringVar(&region, "region", os.Getenv("AWS_REGION"), "AWS region")
	rootcmd.PersistentFlags().StringVar(&key, "aws-key", os.Getenv("AWS_ACCESS_KEY_ID"), "AWS access key")
	rootcmd.PersistentFlags().StringVar(&secret, "aws-secret", os.Getenv("AWS_SECRET_ACCESS_KEY"), "AWS secret key")
	rootcmd.PersistentFlags().StringVar(&rolearn, "aws-rolearn", os.Getenv("ROLE_ARN"), "AWS role ARN to assume")
	rootcmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", verbose, "verbose mode")
	rootcmd.PersistentFlags().StringVarP(&dir, "dir", "d", dir, "root directory for scenario discovery (services/*/scenarios, cloudrun/*/scenarios, cronjobs/*/scenarios, serverless/*/scenarios, microapps/*/scenarios)")
	rootcmd.PersistentFlags().StringVar(&repslack, "report-slack", repslack, "slack url for notification")
	rootcmd.PersistentFlags().StringVar(&reppubsub, "report-pubsub", reppubsub, "pubsub topic for notification")
	rootcmd.PersistentFlags().StringSliceVarP(&files, "scenarios", "s", files, "scenario file[s] to run, comma-separated, or multiple -s")
	rootcmd.PersistentFlags().StringSliceVarP(&tags, "tags", "t", tags, "key=value labels in scenario files that are allowed to run, empty means all")
	rootcmd.AddCommand(runCmd())
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("[oops] ")
	log.SetOutput(os.Stdout)
	rootcmd.Execute()
}
