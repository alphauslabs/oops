package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
)

type repositoryDispatchPayload struct {
	EventType     string                 `json:"event_type"`
	ClientPayload map[string]interface{} `json:"client_payload"`
}

func sendRepositoryDispatch(token string, msg *ScenarioProgressMessage) error {
	if token == "" {
		return fmt.Errorf("github token is empty, skipping repository_dispatch")
	}

	if msg.CommitSHA == "" || msg.Repository == "" {
		return fmt.Errorf("commit_sha=%q or repository=%q missing, skipping", msg.CommitSHA, msg.Repository)
	}

	payload := repositoryDispatchPayload{
		EventType: "oopstest-completed",
		ClientPayload: map[string]interface{}{
			"run_id":          msg.RunID,
			"commit_sha":      msg.CommitSHA,
			"repository":      msg.Repository,
			"run_url":         msg.RunURL,
			"overall_status":  msg.OverallStatus,
			"failed_count":    msg.FailedCount,
			"total_scenarios": msg.TotalScenarios,
			"missing_tests_in_pr":  msg.MissingTestsInPR,
            "should_run_tests":     msg.ShouldRunTests,
			"pr_number":           msg.PRNumber,
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("json.Marshal repository_dispatch payload: %w", err)
	}

	url := fmt.Sprintf("https://api.github.com/repos/%s/dispatches", msg.Repository)

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("http.NewRequest: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("github API returned %d: %s", resp.StatusCode, string(respBody))
	}

	log.Printf("repository_dispatch sent: repo=%s sha=%s run_id=%s pr_number=%s overall_status=%s missing_tests_in_pr=%v should_run_tests=%v",
    msg.Repository, msg.CommitSHA, msg.RunID, msg.PRNumber, msg.OverallStatus, msg.MissingTestsInPR, msg.ShouldRunTests)

	return nil
}