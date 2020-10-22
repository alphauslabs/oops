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
	uuid "github.com/satori/go.uuid"
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
	Id string `json:"id"`

	// The file to process. Sent together with the 'process' code.
	Scenario string `json:"scenario"`
}

func runE(cmd *cobra.Command, args []string) error {
	return doScenario(&doScenarioInput{
		ScenarioFiles: combineFilesAndDir(),
		ReportSlack:   repslack,
		Verbose:       verbose,
	})
}

func combineFilesAndDir() []string {
	tmp := make(map[string]struct{})
	for _, v := range files {
		f, _ := filepath.Abs(v)
		tmp[f] = struct{}{}
	}

	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		f, _ := filepath.Abs(path)
		log.Printf("input: %v", f)
		if strings.HasSuffix(f, ".yaml") {
			tmp[f] = struct{}{}
		}

		return nil
	})

	var final []string
	for k, _ := range tmp {
		final = append(final, k)
	}

	return final
}

func distributePubsub(app *appctx) {
	id := fmt.Sprintf("%s", uuid.NewV4())
	final := combineFilesAndDir()
	for _, f := range final {
		nc := cmd{
			Code:     "process",
			Id:       id,
			Scenario: f,
		}

		err := app.pub.Publish(uniuri.NewLen(10), nc)
		if err != nil {
			log.Printf("publish failed: %v ", err)
			continue
		}
	}
}

func distributeSQS(app *appctx) {
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

	id := fmt.Sprintf("%s", uuid.NewV4())
	final := combineFilesAndDir()
	for _, f := range final {
		nc := cmd{
			Code:     "process",
			Id:       id,
			Scenario: f,
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
	pub      *PubsubPublisher
	mtx      *sync.Mutex
	topicArn *string
}

// Our message processing callback.
func process(ctx interface{}, data []byte) error {
	app := ctx.(*appctx)
	app.mtx.Lock()
	defer app.mtx.Unlock()

	var c cmd
	err := json.Unmarshal(data, &c)
	if err != nil {
		log.Printf("Unmarshal failed: %v", err)
		return err
	}

	switch {
	case c.Code == "start":
		var dist string
		switch {
		case pubsub != "":
			distributePubsub(app)
			dist = fmt.Sprintf("pubsub=%v", pubsub)
		case snssqs != "":
			distributeSQS(app)
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
	case c.Code == "process":
		log.Printf("process: %+v", c)
		doScenario(&doScenarioInput{
			ScenarioFiles: []string{c.Scenario},
			ReportSlack:   repslack,
			Verbose:       verbose,
		})
	}

	return nil
}

func run(ctx context.Context, done chan error) {
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

	app := &appctx{mtx: &sync.Mutex{}}
	ctx0, _ := context.WithCancel(ctx)
	done0 := make(chan error, 1)

	switch {
	case pubsub != "":
		// Make sure topic/subscription is created. Only used for creating subscription if needed.
		_, t, err := GetPublisher(project, pubsub)
		if err != nil {
			log.Fatalf("publisher get/create for %v failed: %v", pubsub, err)
		}

		app.pub, err = NewPubsubPublisher(project, pubsub)
		if err != nil {
			log.Fatalf("create publisher %v failed: %v", pubsub, err)
		}

		if app.pub == nil {
			log.Fatalf("fatal error, publisher nil")
		}

		GetSubscription(project, pubsub, t, time.Second*60)
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
				sigch := make(chan os.Signal)
				signal.Notify(sigch, syscall.SIGINT, syscall.SIGTERM)
				log.Println(<-sigch)
				cancel()
			}()

			return <-done
		},
	}

	cmd.Flags().StringVar(&snssqs, "sns-sqs", snssqs, "name of the SNS topic and SQS queue")
	cmd.Flags().StringVar(&pubsub, "pubsub", pubsub, "name of the GCP pubsub and subscription")
	return cmd
}

func init() {
	rootcmd.PersistentFlags().StringVar(&project, "project-id", os.Getenv("GCP_PROJECT_ID"), "GCP project id")
	rootcmd.PersistentFlags().StringVar(&region, "region", os.Getenv("AWS_REGION"), "AWS region")
	rootcmd.PersistentFlags().StringVar(&key, "aws-key", os.Getenv("AWS_ACCESS_KEY_ID"), "AWS access key")
	rootcmd.PersistentFlags().StringVar(&secret, "aws-secret", os.Getenv("AWS_SECRET_ACCESS_KEY"), "AWS secret key")
	rootcmd.PersistentFlags().StringVar(&rolearn, "aws-rolearn", os.Getenv("ROLE_ARN"), "AWS role ARN to assume")
	rootcmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", verbose, "verbose mode")
	rootcmd.PersistentFlags().StringVarP(&dir, "dir", "d", dir, "root directory for scenario file[s]")
	rootcmd.PersistentFlags().StringVar(&repslack, "report-slack", repslack, "slack url for notification")
	rootcmd.PersistentFlags().StringVar(&reppubsub, "report-pubsub", reppubsub, "pubsub topic for notification")
	rootcmd.PersistentFlags().StringSliceVarP(&files, "scenarios", "s", files, "scenario file[s] to run, comma-separated, or multiple -s")
	rootcmd.PersistentFlags().StringSliceVarP(&tags, "tags", "t", tags, "key=value labels in scenario files that are allowed to run, empty means all")
	rootcmd.AddCommand(runCmd())
}

func main() {
	log.SetFlags(0)
	log.SetOutput(os.Stdout)
	rootcmd.Execute()
}
