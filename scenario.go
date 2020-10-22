package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/dchest/uniuri"
	"github.com/gavv/httpexpect/v2"
	"github.com/pkg/errors"
	uuid "github.com/satori/go.uuid"
	"gopkg.in/yaml.v2"
)

type Asserts struct {
	Code         int    `yaml:"status_code"`
	ValidateJSON string `yaml:"validate_json"`
	Script       string `yaml:"script"`
}

type RunHttp struct {
	Method      string            `yaml:"method"`
	Url         string            `yaml:"url"`
	Headers     map[string]string `yaml:"headers"`
	QueryParams map[string]string `yaml:"query_params"`
	Forms       map[string]string `yaml:"forms"`
	Payload     string            `yaml:"payload"`
	ResponseOut string            `yaml:"response_out"`
	Asserts     *Asserts          `yaml:"asserts"`
}

type Run struct {
	Http RunHttp `yaml:"http"`
}

type ReportPubsub struct {
	Scenario   string            `json:"scenario"`
	Attributes map[string]string `json:"attributes"` // [status]=success|error
	Data       string            `json:"data"`
}

// Scenario reprents a single scenario file to run.
type Scenario struct {
	Tags    map[string]string `yaml:"tags"`
	Env     map[string]string `yaml:"env"`
	Prepare string            `yaml:"prepare"`
	Run     []Run             `yaml:"run"`
	Check   string            `yaml:"check"`

	me    *Scenario
	input *doScenarioInput
	errs  []error
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
		f := fmt.Sprintf("oops_%v", uuid.NewV4())
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
	return ioutil.WriteFile(file, b, 0644)
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

// LoggerReporter interface for httpexpect.
func (s Scenario) Logf(fmt string, args ...interface{}) {
	log.Printf(fmt, args...)
}

func (s Scenario) Errorf(message string, args ...interface{}) {
	m := fmt.Sprintf(message, args...)
	s.me.errs = append(s.me.errs, fmt.Errorf(m))
	log.Printf(message, args...)
}

type doScenarioInput struct {
	app           *appctx
	ScenarioFiles []string
	WorkDir       string
	ReportSlack   string
	ReportPubsub  string
	Verbose       bool
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
	for _, f := range in.ScenarioFiles {
		yml, err := ioutil.ReadFile(f)
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
		log.Printf("scenario: %v", f)

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
			basef := filepath.Base(f)
			prefix := filepath.Join(os.TempDir(), fmt.Sprintf("%v_run%d", basef, i))

			// Parse url.
			fn := fmt.Sprintf("%v_url", prefix)
			nv, err := s.ParseValue(run.Http.Url, fn)
			if err != nil {
				s.errs = append(s.errs, errors.Wrapf(err, "ParseValue[%v]: %v", i, run.Http.Url))
				continue
			}

			u, err := url.Parse(nv)
			if err != nil {
				s.errs = append(s.errs, errors.Wrapf(err, "url.Parse[%v]", i))
				continue
			}

			e := httpexpect.New(s, u.Scheme+"://"+u.Host)
			req := e.Request(run.Http.Method, u.Path)
			for k, v := range run.Http.Headers {
				fn := fmt.Sprintf("%v_hdr.%v", prefix, k)
				nv, err := s.ParseValue(v, fn)
				if err != nil {
					s.errs = append(s.errs, errors.Wrapf(err, "ParseValue[%v]: %v", i, v))
					continue
				}

				req = req.WithHeader(k, nv)
				log.Printf("[header] %v: %v", k, nv)
			}

			for k, v := range run.Http.QueryParams {
				fn := fmt.Sprintf("%v_qparams.%v", prefix, k)
				nv, _ := s.ParseValue(v, fn)
				req = req.WithQuery(k, nv)
			}

			for k, v := range run.Http.Forms {
				fn := fmt.Sprintf("%v_forms.%v", prefix, k)
				nv, _ := s.ParseValue(v, fn)
				req = req.WithFormField(k, nv)
			}

			if run.Http.Payload != "" {
				fn := fmt.Sprintf("%v_payload", prefix)
				nv, _ := s.ParseValue(run.Http.Payload, fn)
				req = req.WithBytes([]byte(nv))
			}

			resp := req.Expect()
			if run.Http.ResponseOut != "" {
				body := resp.Body().Raw()
				s.Write(run.Http.ResponseOut, []byte(body))
				log.Printf("[response] %v", body)
			}

			if run.Http.Asserts == nil {
				continue
			}

			resp = resp.Status(run.Http.Asserts.Code)

			if run.Http.Asserts.ValidateJSON != "" {
				resp.JSON().Schema(run.Http.Asserts.ValidateJSON)
			}

			if run.Http.Asserts.Script != "" {
				fn := fmt.Sprintf("%v_assertscript", prefix)
				s.WriteScript(fn, run.Http.Asserts.Script)
				b, err := s.RunScript(fn)
				if err != nil {
					s.errs = append(s.errs, errors.Wrapf(err,
						"assert.script[%v]:\n%v: %v", i, run.Http.Asserts.Script, string(b)))
				} else {
					if len(string(b)) > 0 {
						log.Printf("asserts.script[%v]:\n%v", i, string(b))
					}
				}
			}
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

		if len(s.errs) > 0 {
			log.Printf("errs: %v", s.errs)
		}

		switch {
		case in.ReportSlack != "":
			if len(s.errs) > 0 {
				// Send to slack, if any.
				payload := SlackMessage{
					Attachments: []SlackAttachment{
						{
							Color:     "danger",
							Title:     fmt.Sprintf("%v - failure", filepath.Base(f)),
							Text:      fmt.Sprintf("%v", s.errs),
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
		case in.ReportPubsub != "" && in.app != nil:
			status := "success"
			var data string
			if len(s.errs) > 0 {
				status = "error"
				data = fmt.Sprintf("%v", s.errs)
			}

			r := ReportPubsub{
				Scenario: filepath.Base(f),
				Attributes: map[string]string{
					"status": status,
				},
				Data: data,
			}

			err := in.app.rpub.Publish(uniuri.NewLen(10), r)
			if err != nil {
				log.Printf("Publish failed: %v ", err)
			}
		}
	}

	return nil
}
