package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
)

type ScenarioProgressMessage struct {
	Status         string `json:"status"`
	Scenario       string `json:"scenario"`
	RunID          string `json:"run_id"`
	Data           string `json:"data"`
	TotalScenarios string `json:"total_scenarios"`
	Code           string `json:"code"`
	OverallStatus  string `json:"overall_status,omitempty"`
	FailedCount    int64  `json:"failed_count,omitempty"`
	CommitSHA      string `json:"commit_sha,omitempty"`
	Repository     string `json:"repository,omitempty"`
	RunURL         string `json:"run_url,omitempty"`
}

type githubCommitStatus struct {
	State       string `json:"state"`
	TargetURL   string `json:"target_url,omitempty"`
	Description string `json:"description"`
	Context     string `json:"context"`
}

func updateGitHubCommitStatus(token string, msg *ScenarioProgressMessage) error {
	if token == "" {
		return fmt.Errorf("mobingi deployer key is empty, skipping commit status update")
	}

	if msg.CommitSHA == "" || msg.Repository == "" {
		return fmt.Errorf("commit_sha=%q or repository=%q missing, skipping", msg.CommitSHA, msg.Repository)
	}

	state := "success"
	description := fmt.Sprintf("All tests passed (%s)", msg.TotalScenarios)

	if msg.OverallStatus == "failure" || msg.FailedCount > 0 {
		state = "failure"
		description = fmt.Sprintf("%d/%s scenario(s) failed", msg.FailedCount, msg.TotalScenarios)
	}

	payload := githubCommitStatus{
		State:       state,
		TargetURL:   msg.RunURL,
		Description: description,
		Context:     "ci/oopstest",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("json.Marshal commit status: %w", err)
	}

	url := fmt.Sprintf("https://api.github.com/repos/%s/statuses/%s", msg.Repository, msg.CommitSHA)

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

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("github API returned %d: %s", resp.StatusCode, string(respBody))
	}

	log.Printf("GitHub commit status updated: repo=%s sha=%s state=%s description=%s",
		msg.Repository, msg.CommitSHA, state, description)

	return nil
}