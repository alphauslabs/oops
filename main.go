package main

import (
	"log"
	"os"
	"os/exec"
)

func main() {
	log.SetFlags(0)
	log.SetOutput(os.Stdout)

	c := exec.Command("sh", "-c", "/home/f14t/gopath/src/github.com/flowerinthenight/oops/sh")
	c.Env = os.Environ()
	b, err := c.CombinedOutput()
	if err != nil {
		log.Println(err)
	}

	log.Printf("%v <-- val", string(b))

	// c.Env = os.Environ()
	// c.Env = append(c.Env, "CLOUDSDK_CONFIG="+t.cnf.CloudCnfDir)

	// b, err := c.CombinedOutput()
	// if err != nil {
	// 	return b, errors.Wrap(err, "exec output failed")
	// }

	// if out != nil {
	// 	err = json.Unmarshal(b, out)
	// 	if err != nil {
	// 		return b, errors.Wrap(err, "unmarshal failed")
	// 	}
	// }

	// return b, nil
}
