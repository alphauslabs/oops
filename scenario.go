package main

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/dchest/uniuri"
	"github.com/gavv/httpexpect/v2"
	yaml "github.com/goccy/go-yaml"
	"github.com/google/uuid"
	"github.com/pkg/errors"
)

// Asserts represents acceptance criteria for a test case
type Asserts struct {
	Code         int    `yaml:"status_code"`
	ValidateJSON string `yaml:"validate_json"`
	Script       string `yaml:"script"`
}

// RunHTTP represents configuration on how to run HTTP test
type RunHTTP struct {
	Method      string            `yaml:"method"`
	URL         string            `yaml:"url"`
	Headers     map[string]string `yaml:"headers"`
	QueryParams map[string]string `yaml:"query_params"`
	Files       map[string]string `yaml:"files"`
	Forms       map[string]string `yaml:"forms"`
	Payload     string            `yaml:"payload"`
	ResponseOut string            `yaml:"response_out"`
	Asserts     *Asserts          `yaml:"asserts"`
}

// Run represent methods for testing
type Run struct {
	HTTP RunHTTP `yaml:"http"`
}

// ReportPubsub represents configuration to report to pubsub
type ReportPubsub struct {
	Scenario   string            `json:"scenario"`
	Attributes map[string]string `json:"attributes"` // [status]=success|error
	Status     string            `json:"status"`     // success|error
	Data       string            `json:"data"`
	MessageID  string            `json:"message_id"` // Unique message ID for tracking
	RunID      string            `json:"run_id"`     // GitHub Action run ID for correlation
}

// Scenario represents a single scenario file to run.
type Scenario struct {
	Maintainers []string          `yaml:"maintainers"`
	Tags        map[string]string `yaml:"tags"`
	Env         map[string]string `yaml:"env"`
	Prepare     string            `yaml:"prepare"`
	Run         []Run             `yaml:"run"`
	Check       string            `yaml:"check"`

	me    *Scenario
	input *doScenarioInput
	errs  []error
	ctx   context.Context // For cancellation support
}

func (s Scenario) getHead(file string) ([]byte, error) {
	c := exec.Command("head", "-n", "1", file)
	return c.CombinedOutput()
}

// RunScript runs file and returns the combined stdout+stderr result.
func (s *Scenario) RunScript(file string) ([]byte, error) {
	l1, err := s.getHead(file)
	if err != nil {
		return nil, err
	}

	if !strings.HasPrefix(string(l1), "#!") {
		return nil, fmt.Errorf("unsupported")
	}

	runner := strings.Split(string(l1), " ")[0] // don't support '/usr/bin/env xx'
	runner = strings.Split(runner, "#!")[1]
	runner = strings.Trim(filepath.Base(runner), "\n")

	var c *exec.Cmd
	switch {
	case strings.Contains(runner, "python"):
		c = exec.Command(runner, file)
	default:
		// Assume it's a shell interpreter.
		c = exec.Command(runner, "-c", file)
	}

	c.Env = os.Environ()
	if len(s.Env) > 0 {
		for k, v := range s.Env {
			add := fmt.Sprintf("%v=%v", k, v)
			c.Env = append(c.Env, add)
		}
	}

	return c.CombinedOutput()
}

// ParseValue tries to check if contents is in script form and if it is, writes it
// to disk as an executable, runs it and returns the resulting stream output.
// Otherwise, return the contents as is.
func (s *Scenario) ParseValue(contents string, file ...string) (string, error) {
	if strings.HasPrefix(contents, "#!") {
		f := fmt.Sprintf("oops_%v", uuid.NewString())
		f = filepath.Join(os.TempDir(), f)
		if len(file) > 0 {
			f = file[0]
		}

		_, err := s.WriteScript(f, contents)
		if err != nil {
			return contents, err
		}

		b, err := s.RunScript(f)
		return string(b), err
	}

	return contents, nil
}

// Write writes b to file.
func (s *Scenario) Write(file string, b []byte) error {
	return os.WriteFile(file, b, 0644)
}

// WriteScript writes contents to file as an executable.
func (s *Scenario) WriteScript(file, contents string) (string, error) {
	f, err := os.Create(file)
	if err != nil {
		return file, err
	}

	defer f.Close()
	f.Chmod(os.ModePerm)
	f.Write([]byte(contents))
	err = f.Sync()
	return file, err
}

// Logf interface for httpexpect.
func (s Scenario) Logf(fmt string, args ...any) {
	log.Printf(fmt, args...)
}

// Errorf returns formatted error message
func (s Scenario) Errorf(message string, args ...any) {
	m := fmt.Sprintf(message, args...)
	s.me.errs = append(s.me.errs, fmt.Errorf("%s", m))
	log.Printf(message, args...)
}

type doScenarioInput struct {
	app           *appctx
	ScenarioFiles []string
	WorkDir       string
	ReportSlack   string
	ReportPubsub  string
	Verbose       bool
	Metadata      map[string]interface{}
}

func isAllowed(s *Scenario) bool {
	if len(tags) == 0 {
		return true
	}

	var matched int
	for _, t := range tags {
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

	return matched == len(tags)
}

func doScenario(in *doScenarioInput) error {
	testCtx, cancelTest := context.WithCancel(context.Background())
	defer cancelTest()
	// Register test with appctx if metadata is available
	var testKey string
	if in.app != nil && in.Metadata != nil {
		if prNum, ok := in.Metadata["pr_number"].(string); ok && prNum != "" {
			testKey = fmt.Sprintf("pr_%s", prNum)
		} else if branch, ok := in.Metadata["branch"].(string); ok && branch != "" {
			testKey = fmt.Sprintf("branch_%s", branch)
		}
		
		if testKey != "" {
			in.app.testsMtx.Lock()
			in.app.runningTests[testKey] = cancelTest
			in.app.testsMtx.Unlock()
			
			defer func() {
				in.app.testsMtx.Lock()
				delete(in.app.runningTests, testKey)
				in.app.testsMtx.Unlock()
			}()
			
			log.Printf("Registered test for cancellation: %s", testKey)
		}
	}
	
	for _, f := range in.ScenarioFiles {
		// Check if context is cancelled before processing each file
		select {
		case <-testCtx.Done():
			log.Printf("Test execution cancelled for scenario: %v", f)
			return fmt.Errorf("test cancelled")
		default:
		}
		
		yml, err := os.ReadFile(f)
		if err != nil {
			continue
		}

		var s Scenario
		err = yaml.Unmarshal(yml, &s)
		if err != nil {
			continue
		}

		if !isAllowed(&s) {
			log.Printf("%v is not allowed by tags", f)
			continue
		}

		s.me = &s    // self-reference for our LoggerReporter functions
		s.input = in // our copy
		s.ctx = testCtx // set context for cancellation
		log.Printf("scenario: %v", f)
		select {
		case <-testCtx.Done():
			log.Printf("Test execution cancelled during prepare: %v", f)
			return fmt.Errorf("test cancelled")
		default:
		}

		if s.Prepare != "" {
			basef := filepath.Base(f)
			fn := filepath.Join(os.TempDir(), fmt.Sprintf("%v_prepare", basef))
			fn, _ = s.WriteScript(fn, s.Prepare)
			b, err := s.RunScript(fn)
			if err != nil {
				s.errs = append(s.errs, errors.Wrapf(err,
					"prepare:\n%v: %v", s.Prepare, string(b)))
			} else {
				if len(string(b)) > 0 {
					log.Printf("prepare:\n%v", string(b))
				}
			}
		}

		for i, run := range s.Run {
			// Check if context is cancelled before each run step
			select {
			case <-testCtx.Done():
				log.Printf("Test execution cancelled during run step %d: %v", i, f)
				s.errs = append(s.errs, fmt.Errorf("test cancelled during run step %d", i))
				goto reportResults // Jump to reporting
			default:
			}
			
			basef := filepath.Base(f)
			prefix := filepath.Join(os.TempDir(), fmt.Sprintf("%v_run%d", basef, i))

			// Parse url.
			fn := fmt.Sprintf("%v_url", prefix)
			nv, err := s.ParseValue(run.HTTP.URL, fn)
			if err != nil {
				s.errs = append(s.errs, errors.Wrapf(err, "ParseValue[%v]: %v", i, run.HTTP.URL))
				continue
			}

			u, err := url.Parse(nv)
			if err != nil {
				s.errs = append(s.errs, errors.Wrapf(err, "url.Parse[%v]", i))
				continue
			}

			e := httpexpect.New(s, u.Scheme+"://"+u.Host)
			req := e.Request(run.HTTP.Method, u.Path)
			for k, v := range run.HTTP.Headers {
				fn := fmt.Sprintf("%v_hdr.%v", prefix, k)
				nv, err := s.ParseValue(v, fn)
				if err != nil {
					s.errs = append(s.errs, errors.Wrapf(err, "ParseValue[%v]: %v", i, v))
					continue
				}

				req = req.WithHeader(k, nv)
				log.Printf("[header] %v: %v", k, nv)
			}

			for k, v := range run.HTTP.QueryParams {
				fn := fmt.Sprintf("%v_qparams.%v", prefix, k)
				nv, _ := s.ParseValue(v, fn)
				req = req.WithQuery(k, nv)
			}

			if len(run.HTTP.Files) > 0 {
				req = req.WithMultipart()
			}
			for k, v := range run.HTTP.Files {
				fn := fmt.Sprintf("%v_files.%v", prefix, k)
				nv, _ := s.ParseValue(v, fn)
				req = req.WithFile(k, nv)
			}

			for k, v := range run.HTTP.Forms {
				fn := fmt.Sprintf("%v_forms.%v", prefix, k)
				nv, _ := s.ParseValue(v, fn)
				req = req.WithFormField(k, nv)
			}

			if run.HTTP.Payload != "" {
				fn := fmt.Sprintf("%v_payload", prefix)
				nv, _ := s.ParseValue(run.HTTP.Payload, fn)
				req = req.WithBytes([]byte(nv))
			}

			resp := req.Expect()
			if run.HTTP.ResponseOut != "" {
				body := resp.Body().Raw()
				s.Write(run.HTTP.ResponseOut, []byte(body))
				log.Printf("[response] %v", body)
			}

			if run.HTTP.Asserts == nil {
				continue
			}

			resp = resp.Status(run.HTTP.Asserts.Code)

			if run.HTTP.Asserts.ValidateJSON != "" {
				resp.JSON().Schema(run.HTTP.Asserts.ValidateJSON)
			}

			if run.HTTP.Asserts.Script != "" {
				fn := fmt.Sprintf("%v_assertscript", prefix)
				s.WriteScript(fn, run.HTTP.Asserts.Script)
				b, err := s.RunScript(fn)
				if err != nil {
					s.errs = append(s.errs, errors.Wrapf(err,
						"assert.script[%v]:\n%v: %v", i, run.HTTP.Asserts.Script, string(b)))
				} else {
					if len(string(b)) > 0 {
						log.Printf("asserts.script[%v]:\n%v", i, string(b))
					}
				}
			}
		}
		select {
		case <-testCtx.Done():
			log.Printf("Test execution cancelled before check step: %v", f)
			s.errs = append(s.errs, fmt.Errorf("test cancelled before check step"))
			goto reportResults
		default:
		}

		if s.Check != "" {
			basef := filepath.Base(f)
			fn := filepath.Join(os.TempDir(), fmt.Sprintf("%v_check", basef))
			fn, _ = s.WriteScript(fn, s.Check)
			b, err := s.RunScript(fn)
			if err != nil {
				s.errs = append(s.errs, errors.Wrapf(err,
					"check:\n%v: %v", s.Check, string(b)))
			} else {
				if len(string(b)) > 0 {
					log.Printf("check:\n%v", string(b))
				}
			}
		}

	reportResults:
		if len(s.errs) > 0 {
			log.Printf("errs: %v", s.errs)
		}
		wasCancelled := false
		select {
		case <-testCtx.Done():
			wasCancelled = true
		default:
		}

		if in.ReportSlack != "" {
			if wasCancelled {
				// Send cancellation notification to slack
				payload := SlackMessage{
					Attachments: []SlackAttachment{
						{
							Color:     "warning",
							Title:     fmt.Sprintf("%v - cancelled", filepath.Base(f)),
							Text:      fmt.Sprintf("Test execution was cancelled (PR closed )\nMaintainers: %v", strings.Join(s.Maintainers, ", ")),
							Footer:    "oops",
							Timestamp: time.Now().Unix(),
						},
					},
				}

				err = payload.Notify(in.ReportSlack)
				if err != nil {
					log.Printf("Notify (slack) failed: %v", err)
				}
			} else if len(s.errs) > 0 {
				// Send failure notification to slack
				payload := SlackMessage{
					Attachments: []SlackAttachment{
						{
							Color:     "danger",
							Title:     fmt.Sprintf("%v - failure", filepath.Base(f)),
							Text:      fmt.Sprintf("Maintainers: %v\n%v", strings.Join(s.Maintainers, ", "), s.errs),
							Footer:    "oops",
							Timestamp: time.Now().Unix(),
						},
					},
				}

				err = payload.Notify(in.ReportSlack)
				if err != nil {
					log.Printf("Notify (slack) failed: %v", err)
				}
			} else {
				// Send success notification to slack
				payload := SlackMessage{
					Attachments: []SlackAttachment{
						{
							Color:     "good",
							Title:     fmt.Sprintf("%v - success", filepath.Base(f)),
							Text:      fmt.Sprintf("All tests passed! \nMaintainers: %v", strings.Join(s.Maintainers, ", ")),
							Footer:    "oops",
							Timestamp: time.Now().Unix(),
						},
					},
				}

				err = payload.Notify(in.ReportSlack)
				if err != nil {
					log.Printf("Notify (slack) failed: %v", err)
				}
			}
		}

		if in.ReportPubsub != "" && in.app != nil {
			if in.app.rpub != nil {
				status := "success"
				var data string
				
				// Check if test was cancelled
				wasCancelled := false
				select {
				case <-testCtx.Done():
					wasCancelled = true
				default:
				}
				
				if wasCancelled {
					status = "cancelled"
					data = "Test execution was cancelled (PR closed)"
				} else if len(s.errs) > 0 {
					status = "error"
					data = fmt.Sprintf("%v", s.errs)
				}

				attr := make(map[string]string)
				if snssqs != "" {
					attr["snssqs"] = snssqs
				}

				if pubsub != "" {
					attr["pubsub"] = pubsub
				}
				runID := ""
				if in.Metadata != nil {
					if idVal, ok := in.Metadata["id"]; ok {
						if idStr, ok := idVal.(string); ok {
							runID = idStr
						}
					}
					for k, v := range in.Metadata {
						if k == "test_analysis" {
							continue 
						}
						if str, ok := v.(string); ok && str != "" {
							attr[k] = str
						}
					}

					if testAnalysis, ok := in.Metadata["test_analysis"].(map[string]interface{}); ok {
						for k, v := range testAnalysis {
							attr[k] = fmt.Sprintf("%v", v)
						}
					}
				}
				
				if runID == "" {
					log.Printf("WARNING: run_id not found in metadata for scenario %s", f)
				}

				r := ReportPubsub{
					Scenario:   f,
					Attributes: attr,
					Status:     status,
					Data:       data,
					MessageID:  uniuri.NewLen(10),
					RunID:      runID,
				}

				err := in.app.rpub.Publish(r.MessageID, r)
				if err != nil {
					log.Printf("Publish failed: %v", err)
				}
			}
		}
	}

	return nil
}
