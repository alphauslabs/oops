package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/flowerinthenight/longsub"
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

func runE(cmd *cobra.Command, args []string) error {
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

	return doScenario(&doScenarioInput{
		ScenarioFiles: final,
		Verbose:       verbose,
	})
}

func processSQS(ctx interface{}, data []byte) error {
	log.Printf("%v", string(data))
	return nil
}

func run(ctx context.Context, done chan error) {
	lsu := longsub.NewAWSUtil(region, key, secret, rolearn)
	_, err := lsu.SetupSnsSqsSubscription(snssqs, snssqs)
	if err != nil {
		panic(err)
	}

	log.Printf("%v subscribed to %v", snssqs, snssqs)

	ctx0, _ := context.WithCancel(ctx)
	done0 := make(chan error, 1)
	go func() {
		ls := longsub.NewSqsLongSub(nil, snssqs, processSQS,
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
	log.Print("oops stopped")
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
