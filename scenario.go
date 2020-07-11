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
	uuid "github.com/satori/go.uuid"
	"gopkg.in/yaml.v2"
)

type Asserts struct {
	Code  int    `yaml:"code"`
	Shell string `yaml:"shell"`
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

type Config struct {
	WorkDir string `yaml:"workdir"`
	Debug   bool   `yaml:"debug"`
}

type Scenario struct {
	Env    map[string]string `yaml:"env"`
	Config *Config           `yaml:"config"`
	Run    []Run             `yaml:"run"`
	Check  string            `yaml:"check"`
}

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

func (s *Scenario) ParseValue(v string) (string, error) {
	var f string
	var err error
	if strings.HasPrefix(v, "#!/") {
		f, err = s.WriteScript(v)
		if err != nil {
			return v, err
		}
	}

	if f == "" {
		return v, nil
	}

	b, err := s.RunScript(f)
	return string(b), err
}

func (s *Scenario) WorkDir() string {
	dir := s.Config.WorkDir
	if dir == "" {
		dir = os.TempDir()
	}

	return dir
}

func (s *Scenario) Write(f string, b []byte) error {
	f = filepath.Join(s.WorkDir(), f)
	return ioutil.WriteFile(f, b, 0644)
}

func (s *Scenario) WriteScript(v string) (string, error) {
	n := filepath.Join(s.WorkDir(), fmt.Sprintf("%v.sh", uuid.NewV4()))
	f, err := os.Create(n)
	if err != nil {
		return "", err
	}

	defer f.Close()
	f.Chmod(os.ModePerm)
	f.Write([]byte(v))
	f.Sync()
	return n, nil
}

// LoggerReporter interface for httpexpect.
func (s Scenario) Logf(fmt string, args ...interface{})       { log.Printf(fmt, args...) }
func (s Scenario) Errorf(message string, args ...interface{}) { log.Printf(message, args...) }

type doScenarioInput struct {
	ScenarioFile string
}

func doScenario(in *doScenarioInput) error {
	yml, err := ioutil.ReadFile(in.ScenarioFile)
	if err != nil {
		return err
	}

	var s Scenario
	err = yaml.Unmarshal(yml, &s)
	if err != nil {
		return err
	}

	for _, run := range s.Run {
		u, err := url.Parse(run.Http.Url)
		if err != nil {
			break
		}

		e := httpexpect.New(s, u.Scheme+"://"+u.Host)
		req := e.Request(run.Http.Method, u.Path)
		for k, v := range run.Http.Headers {
			nv, _ := s.ParseValue(v)
			req = req.WithHeader(k, nv)
			log.Printf("[header] %v: %v", k, nv)
		}

		for k, v := range run.Http.QueryParams {
			nv, _ := s.ParseValue(v)
			req = req.WithQuery(k, nv)
		}

		if run.Http.Payload != "" {
			nv, _ := s.ParseValue(run.Http.Payload)
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

		code := resp.Raw().StatusCode
		if code != run.Http.Asserts.Code {
			log.Printf("[error] asserts.code=%v, expected=%v", code, run.Http.Asserts.Code)
		}

		if run.Http.Asserts.Shell != "" {
			f, _ := s.WriteScript(run.Http.Asserts.Shell)
			s, err := s.RunScript(f)
			if err != nil {
				log.Printf("[error] asserts.shell: %v: %v", err, string(s))
			}
		}
	}

	if s.Check != "" {
		f, _ := s.WriteScript(s.Check)
		s, err := s.RunScript(f)
		if err != nil {
			log.Printf("[error] check: %v: %v", err, string(s))
		}
	}

	return nil
}
