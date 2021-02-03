package main

import (
	"bytes"
	"encoding/json"
	"net/http"
)

//SlackAttachment represents Slack Attachment structure for Slack API
type SlackAttachment struct {
	// Fallback is our simple fallback text equivalent.
	Fallback string `json:"fallback"`

	// Color can be 'good', 'warning', 'danger', or any hex color code.
	Color string `json:"color,omitempty"`

	// Pretext is our text above the attachment section.
	Pretext string `json:"pretext,omitempty"`

	// Title is the notification title.
	Title string `json:"title,omitempty"`

	// TitleLink is the url link attached to the title.
	TitleLink string `json:"title_link,omitempty"`

	// Text is the main text in the attachment.
	Text string `json:"text"`

	// Footer is a brief text to help contextualize and identify an attachment.
	// Limited to 300 characters, and may be truncated further when displayed
	// to users in environments with limited screen real estate.
	Footer string `json:"footer,omitempty"`

	// Timestamp is an integer Unix timestamp that is used to related your attachment to
	// a specific time. The attachment will display the additional timestamp value as part
	// of the attachment's footer.
	Timestamp int64 `json:"ts,omitempty"`
}

//SlackMessage represents Slack message structure for Slack API
type SlackMessage struct {
	Attachments []SlackAttachment `json:"attachments"`
}

//Notify post the message via Slack API
func (sn *SlackMessage) Notify(slackURL string) error {
	bp, err := json.Marshal(sn)
	if err != nil {
		return err
	}

	_, err = http.Post(slackURL, "application/json", bytes.NewBuffer(bp))
	if err != nil {
		return err
	}

	return nil
}
