package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

var (
	rootcmd = &cobra.Command{
		Use:   "oops",
		Short: "k8s-native testing tool",
		Long:  "Kubernetes-native testing tool.",
		RunE:  runE,
	}

	files   []string
	verbose bool
)

func runE(cmd *cobra.Command, args []string) error {
	return doScenario(&doScenarioInput{
		ScenarioFiles: files,
		Verbose:       verbose,
	})
}

func run(ctx context.Context, done chan error) {
	<-ctx.Done()
	log.Print("oops stopped")
	done <- nil
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

	return cmd
}

func init() {
	rootcmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", verbose, "verbose mode")
	rootcmd.Flags().StringSliceVarP(&files, "scenarios", "s", files, "scenario file[s] to run, comma-separated, or multiple -s")
	rootcmd.AddCommand(runCmd())
}

func main() {
	log.SetFlags(0)
	log.SetOutput(os.Stdout)
	rootcmd.Execute()
}
