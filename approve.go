package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
)

type commitStatusPayload struct {
	State       string `json:"state"`
	TargetURL   string `json:"target_url,omitempty"`
	Description string `json:"description"`
	Context     string `json:"context"`
}

func sendApprovalStatus(token, commitSHA, repository, prNumber, runURL string, approvalCount int) error {
	if token == "" {
		return fmt.Errorf("github token is empty, skipping approval status update")
	}

	current, err := getCurrentOopstestStatus(token, commitSHA, repository)
	if err != nil {
		log.Printf("could not fetch current status, skipping: %v", err)
		return nil
	}

	var state, description string

	if approvalCount >= 2 {
		if current == "success" {
			log.Printf("ci/oopstest already success, skipping (approvals=%d)", approvalCount)
			return nil
		}
		state = "success"
		description = fmt.Sprintf("Overridden by approval — approved by %d reviewers", approvalCount)
		log.Printf("approval threshold met (%d), setting ci/oopstest to success: repo=%s sha=%s",
			approvalCount, repository, commitSHA)
	} else {
		if current != "success" {
			log.Printf("ci/oopstest is '%s', no revert needed (approvals=%d)", current, approvalCount)
			return nil
		}
		state = "failure"
		description = fmt.Sprintf("Approval override removed — approvals dropped to %d", approvalCount)
		log.Printf("approval count dropped (%d), reverting ci/oopstest: repo=%s sha=%s",
			approvalCount, repository, commitSHA)
	}

	return postCommitStatus(token, commitSHA, repository, runURL, state, description)
}

func getCurrentOopstestStatus(token, commitSHA, repository string) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/commits/%s/statuses", repository, commitSHA)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("http.NewRequest: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	var statuses []struct {
		Context string `json:"context"`
		State   string `json:"state"`
	}

	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &statuses); err != nil {
		return "", fmt.Errorf("unmarshal statuses: %w", err)
	}

	for _, s := range statuses {
		if s.Context == "ci/oopstest" {
			return s.State, nil
		}
	}

	return "", nil
}

func postCommitStatus(token, commitSHA, repository, targetURL, state, description string) error {
	payload := commitStatusPayload{
		State:       state,
		TargetURL:   targetURL,
		Description: description,
		Context:     "ci/oopstest",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("json.Marshal: %w", err)
	}

	url := fmt.Sprintf("https://api.github.com/repos/%s/statuses/%s", repository, commitSHA)

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

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("github API returned %d: %s", resp.StatusCode, string(respBody))
	}

	log.Printf("ci/oopstest updated: state=%s repo=%s sha=%s target_url=%s", state, repository, commitSHA, targetURL)
	return nil
}