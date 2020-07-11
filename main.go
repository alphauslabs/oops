package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gavv/httpexpect/v2"
	uuid "github.com/satori/go.uuid"
	"gopkg.in/yaml.v2"
)

type asserts struct {
	Code  int    `yaml:"code"`
	Shell string `yaml:"shell"`
}

type runhttp struct {
	Method         string            `yaml:"method"`
	Url            string            `yaml:"url"`
	Headers        map[string]string `yaml:"headers"`
	QueryParams    map[string]string `yaml:"query_params"`
	RequestPayload string            `yaml:"request_payload"`
	ResponseOut    string            `yaml:"response_out"`
	Asserts        asserts           `yaml:"asserts"`
}

type run struct {
	Http runhttp `yaml:"http"`
}

type scenario struct {
	Env     map[string]string `yaml:"env"`
	Run     []run             `yaml:"run"`
	Asserts asserts           `yaml:"asserts"`
}

func (s *scenario) RunScript(file string) ([]byte, error) {
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

func (s *scenario) ParseValue(v string) string {
	f := func() string {
		var n string
		if strings.HasPrefix(v, "#!/") {
			n = filepath.Join(os.TempDir(), fmt.Sprintf("%v.sh", uuid.NewV4()))
			f, err := os.Create(n)
			if err != nil {
				return ""
			}

			defer f.Close()
			f.Chmod(os.ModePerm)
			f.Write([]byte(v))
			f.Sync()
		}

		return n
	}()

	if f == "" {
		return v
	}

	b, _ := s.RunScript(f)
	return string(b)
}

// Logger is used as output backend for Printer.
// testing.TB implements this interface.
type Logger interface {
	// Logf writes message to log.
	Logf(fmt string, args ...interface{})
}

// Reporter is used to report failures.
// testing.TB, AssertReporter, and RequireReporter implement this interface.
type Reporter interface {
	// Errorf reports failure.
	// Allowed to return normally or terminate test using t.FailNow().
	Errorf(message string, args ...interface{})
}

// LoggerReporter combines Logger and Reporter interfaces.
type LoggerReporter interface {
	Logger
	Reporter
}

type rep struct{}

func (r rep) Logf(fmt string, args ...interface{}) {
	log.Printf(fmt, args...)
}
func (r rep) Errorf(message string, args ...interface{}) {
	log.Printf(message, args...)
}

func main() {
	log.SetFlags(0)
	log.SetOutput(os.Stdout)

	yml, err := ioutil.ReadFile("/home/f14t/gopath/src/github.com/flowerinthenight/oops/test.yaml")
	if err != nil {
		log.Fatal(err)
	}

	var s scenario
	err = yaml.Unmarshal(yml, &s)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("%+v", s)
	errrep := rep{}
	for _, run := range s.Run {
		u, err := url.Parse(run.Http.Url)
		if err != nil {
			panic(err)
		}

		e := httpexpect.New(errrep, u.Scheme+"://"+u.Host)
		switch run.Http.Method {
		case http.MethodGet:
			req := e.GET(u.Path)
			for k, v := range run.Http.Headers {
				nv := s.ParseValue(v)
				log.Printf("%v = %v", k, nv)
				req = req.WithHeader(k, s.ParseValue(v))
			}

			for k, v := range run.Http.QueryParams {
				req = req.WithQuery(k, s.ParseValue(v))
			}

			req.Expect()
		}

		log.Printf("val=%v", s.ParseValue(run.Http.Asserts.Shell))
	}
}
