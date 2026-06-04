package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"cloud.google.com/go/spanner"
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

	scenariopubsub     string
	githubtoken        string
	secretproject      string
	secretname         string
	spannerdb          string
	spannercanceltable string
	preprocesshook     string
	skipNotif          bool

	verbose bool
)

type cmd struct {
	// Valid values: start | start_all | process
	// start = initiate distribution of files in --dir to SNS
	// start_all = initiate distribution of all files in --dir (tag-filtered only)
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

	// Links the original run and all its reruns together.
	GroupID string `json:"group_id,omitempty"`
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
	var out []string
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(path, ".yaml") && strings.Contains(path, "/scenarios/") {
			abs, _ := filepath.Abs(path)
			log.Printf("input: %v", abs)
			out = append(out, abs)
		}
		return nil
	})
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

func dedupeWithOverlay(overlayDir string, scenarios []string) []string {
	var out []string
	overlayFiles := findScenarioFiles(overlayDir)
	overridden := make(map[string]bool)
	for _, f := range overlayFiles {
		if rel, err := filepath.Rel(overlayDir, f); err == nil {
			overridden[rel] = true
		}
	}
	var deduped []string
	for _, f := range scenarios {
		if rel, err := filepath.Rel(dir, f); err == nil && overridden[rel] {
			continue // baked-in has an overlay counterpart, skip it
		}
		deduped = append(deduped, f)
	}
	out = append(deduped, overlayFiles...)
	return out
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

func distributePubsub(app *appctx, runID string, tagFilters []string, metadata map[string]interface{}, forceAll bool) bool {
	id := runID
	if metadata == nil {
		metadata = make(map[string]interface{})
	}
	final := combineFilesAndDir()
	if !forceAll {
		affectedServices := extractAffectedServices(metadata)
		if len(affectedServices) == 0 {
			log.Printf("no affected services in metadata, skipping distribution")
			return false
		}

		log.Printf("affected services from metadata: %v", affectedServices)
		before := len(final)
		final = filterScenariosByAffectedServices(final, affectedServices)
		log.Printf("service filter: %d/%d scenarios kept", len(final), before)
	}

	// If hook returned an overlay_dir, scan it and drop baked-in files
	// that have an overlay counterpart at the same relative path.
	overlayDir, _ := metadata["overlay_dir"].(string)
	if overlayDir != "" {
		final = dedupeWithOverlay(overlayDir, final)
	}

	filtered := filterScenariosByTags(final, tagFilters)
	log.Printf("distributing %d/%d scenarios matching tags %v", len(filtered), len(final), tagFilters)

	metadata["total_scenarios"] = fmt.Sprintf("%d", len(filtered))

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
	return true
}

func distributeSQS(app *appctx, runID string, tagFilters []string, metadata map[string]interface{}, forceAll bool) bool {
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
	if metadata == nil {
		metadata = make(map[string]interface{})
	}
	final := combineFilesAndDir()
	if !forceAll {
		affectedServices := extractAffectedServices(metadata)
		if len(affectedServices) == 0 {
			log.Printf("no affected services in metadata, skipping distribution")
			return false
		}

		log.Printf("affected services from metadata: %v", affectedServices)
		before := len(final)
		final = filterScenariosByAffectedServices(final, affectedServices)
		log.Printf("service filter: %d/%d scenarios kept", len(final), before)
	}

	// If hook returned an overlay_dir, scan it and drop baked-in files
	// that have an overlay counterpart at the same relative path.
	overlayDir, _ := metadata["overlay_dir"].(string)
	if overlayDir != "" {
		final = dedupeWithOverlay(overlayDir, final)
	}

	filtered := filterScenariosByTags(final, tagFilters)
	log.Printf("distributing %d/%d scenarios matching tags %v", len(filtered), len(final), tagFilters)
	metadata["total_scenarios"] = fmt.Sprintf("%d", len(filtered))

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
	return true
}

type appctx struct {
	pub           *lspubsub.PubsubPublisher // starter publisher topic
	rpub          *lspubsub.PubsubPublisher // topic to publish reports
	mtx           *sync.Mutex
	topicArn      *string
	spannerClient *spanner.Client // Spanner client for cross-pod cancel lookup
}

func (a *appctx) isRunCancelled(runID string, commitSha string) bool {
	if runID == "" && commitSha == "" {
		return false
	}

	if a.spannerClient == nil {
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var stmt spanner.Statement
	if runID != "" {
		stmt = spanner.Statement{
			SQL: fmt.Sprintf(`SELECT run_id FROM %s WHERE run_id = @run_id AND status = 'closed' LIMIT 1`, spannercanceltable),
			Params: map[string]interface{}{
				"run_id": runID,
			},
		}
	} else {
		stmt = spanner.Statement{
			SQL: fmt.Sprintf(`SELECT run_id FROM %s WHERE commit_sha = @commit_sha AND status = 'closed' LIMIT 1`, spannercanceltable),
			Params: map[string]interface{}{
				"commit_sha": commitSha,
			},
		}
	}

	found := false
	_ = a.spannerClient.Single().Query(ctx, stmt).Do(func(row *spanner.Row) error {
		found = true
		return nil
	})
	return found
}

func handleScenarioCompletion(ctx any, data []byte) error {
	var msg ScenarioProgressMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		log.Printf("handleScenarioCompletion: unmarshal failed: %v", err)
		return err
	}

	log.Printf("scenario progress: run_id=%s code=%s progress=%s", msg.RunID, msg.Code, msg.TotalScenarios)

	switch msg.Code {
	case "approve":
		log.Printf("received approve event: repo=%s sha=%s approvals=%d reviewers=%s",
			msg.Repository, msg.CommitSHA, msg.ApprovalCount, msg.Reviewers)

		if msg.CommitSHA == "" || msg.Repository == "" {
			log.Printf("approve: missing commit_sha or repository, skipping")
			return nil
		}

		if err := sendApprovalStatus(githubtoken, msg.CommitSHA, msg.Repository, msg.PRNumber, msg.RunURL, msg.ApprovalCount, msg.Reviewers); err != nil {
			log.Printf("sendApprovalStatus failed: %v", err)
		}

		if repslack != "" {
			notifyApproval(msg, repslack)
		}

	case "cancelled":
		log.Printf("run cancelled: run_id=%s repo=%s sha=%s pr=%s",
			msg.RunID, msg.Repository, msg.CommitSHA, msg.PRNumber)

		if msg.CommitSHA != "" && msg.Repository != "" {
			if err := postCommitStatus(githubtoken, msg.CommitSHA, msg.Repository, msg.RunURL, "failure", "Test run cancelled"); err != nil {
				log.Printf("postCommitStatus (cancelled) failed: %v", err)
			}
		} else {
			log.Printf("cancelled: missing commit_sha or repository, skipping github status update")
		}

		if repslack != "" {
			notifyCancelled(msg, repslack)
		}

	case "completed":
		log.Printf("run completed: run_id=%s overall_status=%s failed=%d repo=%s sha=%s",
			msg.RunID, msg.OverallStatus, msg.FailedCount, msg.Repository, msg.CommitSHA)

		if err := sendRepositoryDispatch(githubtoken, &msg); err != nil {
			log.Printf("sendRepositoryDispatch failed: %v", err)
		}

		if repslack == "" {
			break
		}
		if msg.TriggerType == "rerun" {
			notifyRerunComplete(msg, repslack)
		} else if !skipNotif {
			notifyRunComplete(msg, repslack)
		}

	default:
	}

	return nil
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

	if preprocesshook != "" {
		log.Printf("Running pre-process hook %v for scenario %v", preprocesshook, c.Scenario)

		// Remember the old overlay_dir before the hook potentially generates a new one.
		// This is used below to remap stale overlay scenario paths (e.g. from a rerun
		// where the original /tmp/overlay-... directory no longer exists).
		oldOverlayDir, _ := c.Metadata["overlay_dir"].(string)

		hookInput, _ := json.Marshal(c)
		hookOutput, err := exec.Command(preprocesshook, string(hookInput)).Output()
		if err != nil {
			log.Printf("pre-process hook failed for scenario %v: %v", c.Scenario, err)
			return err
		}

		var hookResult struct {
			OverlayDir string `json:"overlay_dir"`
		}
		if err := json.Unmarshal(hookOutput, &hookResult); err != nil {
			log.Printf("pre-process hook: invalid output for scenario %v: %v", c.Scenario, err)
			return err
		}

		if hookResult.OverlayDir != "" {
			// If the scenario path still points at the OLD (stale) overlay directory,
			// remap it to the freshly generated overlay so the file can actually be read.
			// This happens on reruns: the process message carries a /tmp/overlay-<old>/...
			// path that was persisted in Spanner but has since been cleaned up.
			if oldOverlayDir != "" && strings.HasPrefix(c.Scenario, oldOverlayDir) {
				rel, relErr := filepath.Rel(oldOverlayDir, c.Scenario)
				if relErr == nil {
					newPath := filepath.Join(hookResult.OverlayDir, rel)
					if _, statErr := os.Stat(newPath); statErr == nil {
						log.Printf("pre-process hook: remapped stale overlay path %v -> %v", c.Scenario, newPath)
						c.Scenario = newPath
					} else {
						log.Printf("pre-process hook: remapped path %v does not exist in new overlay, keeping original", newPath)
					}
				}
			}
			c.Metadata["overlay_dir"] = hookResult.OverlayDir
		}
	}

	switch c.Code {
	case "start":
		log.Printf("received start command with tags: %v", c.Tags)
		commitSha, _ := c.Metadata["commit_sha"].(string)
		repository, _ := c.Metadata["repository"].(string)
		prNumber, _ := c.Metadata["pr_number"].(string)
		runURL, _ := c.Metadata["run_url"].(string)

		if app.isRunCancelled(c.ID, commitSha) {
			log.Printf("start: run cancelled for run_id=%s commit_sha=%s — skipping distribution", c.ID, commitSha)

			if commitSha != "" && repository != "" {
				if err := postCommitStatus(
					githubtoken,
					commitSha,
					repository,
					runURL,
					"failure",
					fmt.Sprintf("Test run cancelled"),
				); err != nil {
					log.Printf("postCommitStatus (cancelled) failed: %v", err)
				}
			}

			if repslack != "" {
				payload := SlackMessage{
					Attachments: []SlackAttachment{
						{
							Color: "warning",
							Title: "Test Run Cancelled",
							Text: fmt.Sprintf("*PR #%s* in `%s` was closed.\nIn-progress test run `%s` has been cancelled.\n<%s|View workflow>",
								prNumber, repository, c.ID, runURL),
							Footer:    fmt.Sprintf("oops • pr: %s • sha: %.7s", prNumber, commitSha),
							Timestamp: time.Now().Unix(),
							MrkdwnIn:  []string{"text"},
						},
					},
				}

				if err := payload.Notify(repslack); err != nil {
					log.Printf("Notify (slack) cancelled failed: %v", err)
				}
			}

			break
		}

		var distributed bool
		var dist string
		switch {
		case pubsub != "":
			distributed = distributePubsub(app, c.ID, c.Tags, c.Metadata, false)
			dist = fmt.Sprintf("pubsub=%v", pubsub)
		case snssqs != "":
			distributed = distributeSQS(app, c.ID, c.Tags, c.Metadata, false)
			dist = fmt.Sprintf("sns/sqs=%v", snssqs)
		}

		if !distributed {
			log.Printf("no scenarios distributed, skipping slack notification")
			break
		}

		host, _ := os.Hostname()
		if repslack != "" {
			notifyRunStarted("start tests", host, dist, repslack)
		}
	case "start_all":
		log.Printf("received start_all command with tags: %v", c.Tags)
		var distributed bool
		var dist string
		switch {
		case pubsub != "":
			distributed = distributePubsub(app, c.ID, c.Tags, c.Metadata, true)
			dist = fmt.Sprintf("pubsub=%v", pubsub)
		case snssqs != "":
			distributed = distributeSQS(app, c.ID, c.Tags, c.Metadata, true)
			dist = fmt.Sprintf("sns/sqs=%v", snssqs)
		}

		if !distributed {
			log.Printf("no scenarios distributed, skipping slack notification")
			break
		}

		host, _ := os.Hostname()
		if repslack != "" {
			notifyRunStarted("start all tests", host, dist, repslack)
		}
	case "rerun_started":
		mode, _ := c.Metadata["rerun_mode"].(string)
		rerunTotal, _ := c.Metadata["rerun_total"].(string)
		repository, _ := c.Metadata["repository"].(string)

		log.Printf("rerun started: run_id=%s mode=%s rerun_total=%s repo=%s", c.ID, mode, rerunTotal, repository)

		if repslack != "" {
			notifyRerunStarted(c.ID, mode, rerunTotal, repository, repslack)
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
			GroupID:       c.GroupID,
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

	if spannerdb != "" {
		sc, err := spanner.NewClient(ctx, spannerdb)
		if err != nil {
			log.Printf("WARNING: spanner.NewClient failed, cancel checks will be skipped: %v", err)
		} else {
			app.spannerClient = sc
			defer sc.Close()
			log.Printf("spanner client initialised: %s", spannerdb)
		}
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
	if secretproject != "" {
		val, err := getSecret(ctx, secretproject, secretname)
		if err != nil {
			log.Printf("WARNING: could not fetch %v from Secret Manager: %v", secretname, err)
		} else {
			githubtoken = strings.TrimSpace(val)
			log.Printf("%v loaded from Secret Manager (project=%s)", secretname, secretproject)
		}
	}

	if scenariopubsub != "" && pubsub != "" {
		if githubtoken == "" {
			log.Printf("WARNING: githubtoken is empty; scenario progress listener will run, but GitHub repository_dispatch will be skipped")
		}
		log.Printf("starting scenario progress listener on %v", scenariopubsub)

		_, st, err := lspubsub.GetPublisher(project, scenariopubsub)
		if err != nil {
			log.Fatalf("publisher get/create for %v failed: %v", scenariopubsub, err)
		}

		_, err = lspubsub.GetSubscription(project, scenariopubsub, st, time.Second*60)
		if err != nil {
			log.Fatalf("subscription get/create for %v failed: %v", scenariopubsub, err)
		}

		done1 := make(chan error, 1)
		go func() {
			ls := lspubsub.NewLengthySubscriber(app, project, scenariopubsub, handleScenarioCompletion)
			err := ls.Start(ctx0, done1)
			if err != nil {
				log.Fatalf("listener for scenario progress failed: %v", err)
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
	cmd.Flags().StringVar(&scenariopubsub, "scenario-pubsub", os.Getenv("SCENARIO_PUBSUB"), "pubsub subscription for scenario progress (e.g. oopsnext-scenarios)")
	cmd.Flags().StringVar(&spannerdb, "spanner-db", os.Getenv("SPANNER_DB"), "Spanner DB path for cancel checks")
	cmd.Flags().StringVar(&spannercanceltable, "spanner-cancel-table", os.Getenv("SPANNER_CANCEL_TABLE"), "Spanner table name for cancel checks")
	cmd.Flags().BoolVar(&skipNotif, "skip-result-notif", false, "skip result Slack notification")
	return cmd
}

func init() {
	rootcmd.Flags().SortFlags = false
	rootcmd.PersistentFlags().SortFlags = false
	rootcmd.PersistentFlags().StringVar(&project, "project-id", os.Getenv("GCP_PROJECT_ID"), "GCP project id")
	rootcmd.PersistentFlags().StringVar(&secretproject, "secret-project-id", "", "GCP project id where secrets are stored")
	rootcmd.PersistentFlags().StringVar(&secretname, "secret-name", "", "secret name to fetch from Secret Manager")
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
	rootcmd.PersistentFlags().StringVar(&githubtoken, "github-token", "", "GitHub token for commit status updates")
	rootcmd.PersistentFlags().StringVar(&preprocesshook, "pre-process-hook", preprocesshook, "executable to run before processing each scenario, with the scenario file path as argument")
	rootcmd.PersistentFlags().BoolVar(&skipNotif, "skip-result-notif", false, "skip result Slack notification")
	rootcmd.AddCommand(runCmd())
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("[oops] ")
	log.SetOutput(os.Stdout)
	rootcmd.Execute()
}
