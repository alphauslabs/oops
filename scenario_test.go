package main

import "testing"

func Test__getHead(t *testing.T) {
	s := Scenario{}
	b, err := s.getHead("main.go")
	if err != nil {
		t.Fatal(err)
	}

	t.Log(string(b))
}
