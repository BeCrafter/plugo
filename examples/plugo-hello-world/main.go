package main

import (
	"fmt"

	"github.com/BeCrafter/plugo"
)

type Plugin struct {
	plugo.Health
}

func (p *Plugin) SayHello(name string, msg *string) error {
	*msg = fmt.Sprintf("Hello %s", name)
	return nil
}

func main() {
	plugin := &Plugin{}

	plugo.Register(plugin)
	plugo.Run()
}
