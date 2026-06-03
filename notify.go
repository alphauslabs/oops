package main

import (
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"
)

type ScenarioProgressMessage struct {
	Status           string   `json:"status"`
	Scenario         string   `json:"scenario"`
	RunID            string   `json:"run_id"`
	Data             string   `json:"data"`
	TotalScenarios   string   `json:"total_scenarios"`
	Code             string   `json:"code"`
	TriggerType      string   `json:"trigger_type,omitempty"`
	RerunMode        string   `json:"rerun_mode,omitempty"`
	OverallStatus    string   `json:"overall_status,omitempty"`
	FailedCount      int64    `json:"failed_count,omitempty"`
	FailedScenarios  []string `json:"failed_scenarios,omitempty"`
	CommitSHA        string   `json:"commit_sha,omitempty"`
	Repository       string   `json:"repository,omitempty"`
	RunURL           string   `json:"run_url,omitempty"`
	MissingTestsInPR bool     `json:"missing_tests_in_pr,omitempty"`
	ShouldRunTests   bool     `json:"should_run_tests,omitempty"`
	PRNumber         string   `json:"pr_number,omitempty"`
	ApprovalCount    int      `json:"approval_count,omitempty"`
	Reviewers        string   `json:"reviewers,omitempty"`
}

func notifyRunStarted(title, host, dist, webhook string) {
	payload := SlackMessage{
		Attachments: []SlackAttachment{{
			Color:     "good",
			Title:     title,
			Text:      fmt.Sprintf("from %v through %v", host, dist),
			Footer:    "oops",
			Timestamp: time.Now().Unix(),
		}},
	}
	if err := payload.Notify(webhook); err != nil {
		log.Printf("Notify (slack) %s failed: %v", title, err)
	}
}

func notifyRerunStarted(runID, mode, rerunTotal, repository, webhook string) {
	modeLabel := rerunModeLabel(mode)
	text := fmt.Sprintf("*Run ID:* `%s`\n*Scenarios queued:* %s", runID, rerunTotal)
	if repository != "" {
		text += fmt.Sprintf("\n*Repository:* %s", repository)
	}
	payload := SlackMessage{
		Attachments: []SlackAttachment{{
			Color:     "#439FE0",
			Title:     fmt.Sprintf("Rerun Started — %s", modeLabel),
			Text:      text,
			Footer:    fmt.Sprintf("oops • rerun • runid: %s", runID),
			Timestamp: time.Now().Unix(),
			MrkdwnIn:  []string{"text"},
		}},
	}
	if err := payload.Notify(webhook); err != nil {
		log.Printf("Notify (slack) rerun_started failed: %v", err)
	}
}

func notifyApproval(msg ScenarioProgressMessage, webhook string) {
	reviewerMentions := ""
	if msg.Reviewers != "" {
		var mentions []string
		for _, r := range strings.Split(msg.Reviewers, ",") {
			mentions = append(mentions, "@"+strings.TrimSpace(r))
		}
		reviewerMentions = strings.Join(mentions, " ")
	}

	text := fmt.Sprintf("*Repository:* %s\n*PR:* #%s\n*Reviewers:* %s\n*Approval Count:* %d",
		msg.Repository, msg.PRNumber, reviewerMentions, msg.ApprovalCount)
	if msg.RunURL != "" {
		text += fmt.Sprintf("\n\n<%s|View run>", msg.RunURL)
	}

	payload := SlackMessage{
		Attachments: []SlackAttachment{{
			Color:     "good",
			Title:     "PR Approved",
			Text:      text,
			Footer:    "oops • approval",
			Timestamp: time.Now().Unix(),
			MrkdwnIn:  []string{"text"},
		}},
	}
	if err := payload.Notify(webhook); err != nil {
		log.Printf("Notify (slack) approval failed: %v", err)
	}
}

func notifyCancelled(msg ScenarioProgressMessage, webhook string) {
	isRerun := msg.TriggerType == "rerun"
	title := "Test Run Cancelled"
	if isRerun {
		title = fmt.Sprintf("Rerun Cancelled — %s", rerunModeLabel(msg.RerunMode))
	}

	var text string
	if !isRerun && msg.PRNumber != "" && msg.Repository != "" {
		text = fmt.Sprintf("*PR #%s* in `%s` was closed.\nIn-progress test run `%s` has been cancelled.",
			msg.PRNumber, msg.Repository, msg.RunID)
	} else {
		text = fmt.Sprintf("In-progress test run `%s` has been cancelled.", msg.RunID)
	}
	if msg.RunURL != "" && !isRerun {
		text += fmt.Sprintf("\n<%s|View workflow>", msg.RunURL)
	}

	footer := fmt.Sprintf("oops • pr: %s • sha: %.7s", msg.PRNumber, msg.CommitSHA)
	if msg.PRNumber == "" && msg.CommitSHA == "" {
		footer = "oops • rerun"
	}

	payload := SlackMessage{
		Attachments: []SlackAttachment{{
			Color:     "warning",
			Title:     title,
			Text:      text,
			Footer:    footer,
			Timestamp: time.Now().Unix(),
			MrkdwnIn:  []string{"text"},
		}},
	}
	if err := payload.Notify(webhook); err != nil {
		log.Printf("Notify (slack) cancelled failed: %v", err)
	}
}

func notifyRunComplete(msg ScenarioProgressMessage, webhook string) {
	parts := strings.SplitN(msg.TotalScenarios, "/", 2)
	total := parts[len(parts)-1]
	var totalN int64
	if len(parts) == 2 {
		fmt.Sscanf(parts[1], "%d", &totalN)
	}
	successCount := totalN - msg.FailedCount

	var color, title, text string
	header := fmt.Sprintf("*Environment:* %s\n", envLabel())

	if msg.OverallStatus == "failure" || msg.FailedCount > 0 {
		color = "danger"
		title = "Test Run Complete (With Failures)"
		text = header + runSummaryText(total, successCount, msg.FailedCount, msg.FailedScenarios)
	} else {
		color = "good"
		title = "Test Run Complete"
		text = header + fmt.Sprintf("*Run Summary*\nTotal: %s\nPassed: %s\nFailed: 0", total, total)
	}
	if msg.RunURL != "" {
		text += fmt.Sprintf("\n\n<%s|View run>", msg.RunURL)
	}

	payload := SlackMessage{
		Attachments: []SlackAttachment{{
			Color:     color,
			Title:     title,
			Text:      text,
			Footer:    fmt.Sprintf("oops • runid: %v", msg.RunID),
			Timestamp: time.Now().Unix(),
			MrkdwnIn:  []string{"text"},
		}},
	}
	if err := payload.Notify(webhook); err != nil {
		log.Printf("Notify (slack) run complete failed: %v", err)
	}
}

func notifyRerunComplete(msg ScenarioProgressMessage, webhook string) {
	modeLabel := rerunModeLabel(msg.RerunMode)
	parts := strings.SplitN(msg.TotalScenarios, "/", 2)
	total := parts[len(parts)-1]
	var totalN int64
	if len(parts) == 2 {
		fmt.Sscanf(parts[1], "%d", &totalN)
	}
	successCount := totalN - msg.FailedCount

	var color, title, text string
	header := fmt.Sprintf("*Environment:* %s\n", envLabel())

	failed := msg.OverallStatus == "failure" || msg.FailedCount > 0
	if failed {
		color = "danger"
		title = fmt.Sprintf("Rerun Complete (With Failures) — %s", modeLabel)
	} else {
		color = "good"
		title = fmt.Sprintf("Rerun Complete — %s", modeLabel)
	}

	if msg.RerunMode == "specific" {
		result := "Passed"
		if failed {
			result = "Failed"
		}
		text = header + fmt.Sprintf("*Scenario:* %s\n*Result:*  %s", filepath.Base(msg.Scenario), result)
	} else if failed {
		text = header + runSummaryText(total, successCount, msg.FailedCount, msg.FailedScenarios)
	} else {
		text = header + fmt.Sprintf("*Run Summary*\nTotal: %s\nPassed: %s\nFailed: 0", total, total)
	}

	payload := SlackMessage{
		Attachments: []SlackAttachment{{
			Color:     color,
			Title:     title,
			Text:      text,
			Footer:    "oops • rerun",
			Timestamp: time.Now().Unix(),
			MrkdwnIn:  []string{"text"},
		}},
	}
	if err := payload.Notify(webhook); err != nil {
		log.Printf("Notify (slack) rerun complete failed: %v", err)
	}
}

func rerunModeLabel(mode string) string {
	switch mode {
	case "all":
		return "All Scenarios"
	case "failed":
		return "Failed Scenarios"
	case "specific":
		return "Specific Scenario"
	default:
		return ""
	}
}

func envLabel() string {
	if strings.Contains(pubsub, "prod") {
		return "prod"
	}
	if strings.Contains(pubsub, "next") {
		return "next"
	}
	return "dev"
}

func runSummaryText(total string, successCount, failedCount int64, failedScenarios []string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "*Run Summary*\nTotal: %s\nPassed: %d\nFailed: %d", total, successCount, failedCount)
	if len(failedScenarios) > 0 {
		sb.WriteString("\n\n*Failed scenarios:*")
		for _, name := range failedScenarios {
			fmt.Fprintf(&sb, "\n• %v", name)
		}
	}
	return sb.String()
}
