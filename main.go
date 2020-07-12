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
	"github.com/flowerinthenight/longsub"
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

	region  string
	key     string
	secret  string
	rolearn string

	files   []string
	dir     string
	snssqs  string
	slack   string
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

type appctx struct {
	mtx      *sync.Mutex
	topicArn *string
}

// Our SQS message processing callback.
func processSQS(ctx interface{}, data []byte) error {
	ac := ctx.(*appctx)
	ac.mtx.Lock()
	defer ac.mtx.Unlock()

	var c cmd
	err := json.Unmarshal(data, &c)
	if err != nil {
		log.Printf("Unmarshal failed: %v", err)
		return err
	}

	switch {
	case c.Code == "start":
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
				TopicArn: ac.topicArn,
				Subject:  &key,
				Message:  aws.String(string(b)),
			}

			_, err = svc.Publish(m)
			if err != nil {
				log.Printf("Publish failed: %v", err)
				continue
			}
		}
	case c.Code == "process":
		log.Printf("process: %+v", c)
		doScenario(&doScenarioInput{
			ScenarioFiles: []string{c.Scenario},
			Verbose:       verbose,
		})
	}

	return nil
}

func run(ctx context.Context, done chan error) {
	lsu := longsub.NewAWSUtil(region, key, secret, rolearn)
	t, err := lsu.SetupSnsSqsSubscription(snssqs, snssqs)
	if err != nil {
		panic(err)
	}

	ac := &appctx{
		mtx:      &sync.Mutex{},
		topicArn: t,
	}

	log.Printf("%v subscribed to %v", snssqs, snssqs)

	ctx0, _ := context.WithCancel(ctx)
	done0 := make(chan error, 1)
	go func() {
		ls := longsub.NewSqsLongSub(ac, snssqs, processSQS,
			longsub.WithRegion(region),
			longsub.WithAccessKeyId(key),
			longsub.WithSecretAccessKey(secret),
			longsub.WithRoleArn(rolearn),
		)

		err := ls.Start(ctx0, done0)
		if err != nil {
			log.Printf("start long processing for %v failed, err=%v", snssqs, err)
		}
	}()

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

	cmd.Flags().StringVar(&snssqs, "sns-sqs", snssqs, "name of the SNS topic, same name will be used in SQS")
	return cmd
}

func init() {
	rootcmd.PersistentFlags().StringVar(&region, "region", os.Getenv("AWS_REGION"), "AWS region")
	rootcmd.PersistentFlags().StringVar(&key, "aws-key", os.Getenv("AWS_ACCESS_KEY_ID"), "AWS access key")
	rootcmd.PersistentFlags().StringVar(&secret, "aws-secret", os.Getenv("AWS_SECRET_ACCESS_KEY"), "AWS secret key")
	rootcmd.PersistentFlags().StringVar(&rolearn, "aws-rolearn", os.Getenv("ROLE_ARN"), "AWS role ARN to assume")
	rootcmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", verbose, "verbose mode")
	rootcmd.PersistentFlags().StringVarP(&dir, "dir", "d", dir, "root directory for scenario file[s]")
	rootcmd.PersistentFlags().StringVar(&slack, "slack-url", slack, "slack url for notification")
	rootcmd.Flags().StringSliceVarP(&files, "scenarios", "s", files, "scenario file[s] to run, comma-separated, or multiple -s")
	rootcmd.AddCommand(runCmd())
}

func main() {
	log.SetFlags(0)
	log.SetOutput(os.Stdout)
	rootcmd.Execute()
}
