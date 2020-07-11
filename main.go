package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	uuid "github.com/satori/go.uuid"
	"gopkg.in/yaml.v2"
)

type shell struct {
	Shell string `yaml:"shell"`
}

type runhttp struct {
	Method         string            `yaml:"method"`
	Url            string            `yaml:"url"`
	Headers        map[string]string `yaml:"headers"`
	QueryParams    string            `yaml:"query_params"`
	RequestPayload string            `yaml:"request_payload"`
	ResponseOut    string            `yaml:"response_out"`
	Asserts        shell             `yaml:"asserts"`
}

type run struct {
	Http runhttp `yaml:"http"`
}

type scenario struct {
	Env     map[string]string `yaml:"env"`
	Run     []run             `yaml:"run"`
	Asserts shell             `yaml:"asserts"`
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
	if !strings.HasPrefix(v, "#!/") {
		return v
	}

	f := func() string {
		n := filepath.Join(os.TempDir(), fmt.Sprintf("%v.sh", uuid.NewV4()))
		f, err := os.Create(n)
		if err != nil {
			return ""
		}

		defer f.Close()
		f.Chmod(os.ModePerm)
		f.Write([]byte(v))
		f.Sync()
		return n
	}()

	if f == "" {
		return v
	}

	b, _ := s.RunScript(f)
	return string(b)
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
	for _, run := range s.Run {
		log.Printf("val=%v", s.ParseValue(run.Http.Asserts.Shell))
	}
}
