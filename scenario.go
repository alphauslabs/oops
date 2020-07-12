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

	"github.com/gavv/httpexpect/v2"
	"github.com/pkg/errors"
	uuid "github.com/satori/go.uuid"
	"gopkg.in/yaml.v2"
)

type Asserts struct {
	Code         int    `yaml:"status_code"`
	ValidateJSON string `yaml:"validate_json"`
	Shell        string `yaml:"shell"`
}

type RunHttp struct {
	Method      string            `yaml:"method"`
	Url         string            `yaml:"url"`
	Headers     map[string]string `yaml:"headers"`
	QueryParams map[string]string `yaml:"query_params"`
	Payload     string            `yaml:"payload"`
	ResponseOut string            `yaml:"response_out"`
	Asserts     *Asserts          `yaml:"asserts"`
}

type Run struct {
	Http RunHttp `yaml:"http"`
}

// Scenario reprents a single scenario file to run.
type Scenario struct {
	Env   map[string]string `yaml:"env"`
	Run   []Run             `yaml:"run"`
	Check string            `yaml:"check"`

	me   *Scenario
	errs []error
}

// RunScript runs file and returns the combined stdout+stderr result.
func (s *Scenario) RunScript(file string) ([]byte, error) {
	c := exec.Command("sh", "-c", file)
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
	f := fmt.Sprintf("%v.sh", uuid.NewV4())
	f = filepath.Join(os.TempDir(), f)
	if len(file) > 0 {
		f = file[0]
	}

	if strings.HasPrefix(contents, "#!/") {
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
	ScenarioFiles []string
	WorkDir       string
	Verbose       bool
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

		s.me = &s // self-reference for our LoggerReporter functions
		log.Printf("scenario: %v", f)

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

			if run.Http.Asserts.Shell != "" {
				fn := fmt.Sprintf("%v_assertshell", prefix)
				s.WriteScript(fn, run.Http.Asserts.Shell)
				b, err := s.RunScript(fn)
				if err != nil {
					s.errs = append(s.errs, errors.Wrapf(err,
						"assert.shell[%v]:\n%v: %v", i, run.Http.Asserts.Shell, string(b)))
				} else {
					if len(string(b)) > 0 {
						log.Printf("asserts.shell[%v]:\n%v", i, string(b))
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

		log.Printf("errs: %v", s.errs)
	}

	return nil
}
